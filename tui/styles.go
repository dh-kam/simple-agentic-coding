package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Claude Code-ish palette: warm amber accent, muted grays, green/red status.
var (
	accent  = lipgloss.Color("#D97757") // bullet / headings
	dim     = lipgloss.Color("#6B7280") // args / secondary text
	okGreen = lipgloss.Color("#4E9A06")
	errRed  = lipgloss.Color("#CC3333")
	userC   = lipgloss.Color("#9AA0A6")
	hunkC   = lipgloss.Color("#5B8AB8") // diff @@ hunks
)

var (
	bulletStyle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	nameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E5E5")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(dim)
	okStyle     = lipgloss.NewStyle().Foreground(okGreen)
	errStyle    = lipgloss.NewStyle().Foreground(errRed)
	userStyle   = lipgloss.NewStyle().Foreground(userC)
	hunkStyle   = lipgloss.NewStyle().Foreground(hunkC)
	titleStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	hintStyle   = lipgloss.NewStyle().Foreground(dim).Italic(true)
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	askBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1)
)

// renderUser renders a submitted user turn.
func renderUser(text string) string {
	return userStyle.Render("❯ ") + text
}

// renderTool renders a tool-call block: spinner while running, ✓ on success,
// ✗ on error. args/result are shown dimmed and truncated.
func renderTool(b *toolBlock, spinnerView string) string {
	var icon string
	switch {
	case !b.done:
		icon = bulletStyle.Render(spinnerView)
	case b.isErr:
		icon = errStyle.Render("✗")
	default:
		icon = okStyle.Render("✓")
	}
	name := nameStyle.Render(b.name)
	detail := b.detail
	if detail != "" {
		detail = " " + dimStyle.Render(truncate(detail, 72))
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, icon, " ", name, detail)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// summarizeArgs pulls a short, human-readable hint from a tool's JSON args
// (path / command / pattern / url / prompt / query), compact otherwise.
func summarizeArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return truncate(string(args), 72)
	}
	for _, k := range []string{"path", "command", "pattern", "url", "prompt", "query", "cell_id"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return k + "=" + truncate(s, 60)
			}
		}
	}
	b, _ := json.Marshal(m)
	return truncate(string(b), 60)
}

// markdownRenderer lazily builds a glamour renderer for the given width.
type mdCache struct {
	r     *glamour.TermRenderer
	width int
}

func (c *mdCache) render(md string, width int) (string, error) {
	if c.r == nil || c.width != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return md, err
		}
		c.r, c.width = r, width
	}
	return c.r.Render(md)
}

// renderFileDiff returns a colored unified diff for a file change, or "" if
// nothing changed. Used by the TUI when write/edit/multi_edit modify a file.
func renderFileDiff(path, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	diff := strings.TrimSpace(udiff.Unified("a/"+path, "b/"+path, oldContent, newContent))
	if diff == "" {
		return ""
	}
	const maxLines = 60 // cap so a huge change doesn't flood the transcript
	lines := strings.Split(diff, "\n")
	var b strings.Builder
	b.WriteString(nameStyle.Render("✎ " + path))
	b.WriteByte('\n')
	for i, line := range lines {
		if i >= maxLines {
			b.WriteString(dimStyle.Render(fmt.Sprintf("… (%d more lines, diff truncated)", len(lines)-maxLines)))
			b.WriteByte('\n')
			break
		}
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			b.WriteString(dimStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(okStyle.Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(errStyle.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(hunkStyle.Render(line))
		default:
			b.WriteString(dimStyle.Render(line))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderApproval renders the permission modal shown when a sensitive tool runs.
func renderApproval(name string, args json.RawMessage) string {
	head := errStyle.Render("⚠ 승인 필요") + " " + nameStyle.Render(name)
	if d := summarizeArgs(args); d != "" {
		head += " " + dimStyle.Render(d)
	}
	hint := hintStyle.Render("[y/Enter] 허용   [n/Esc] 거부   [Ctrl+C] 중단")
	return askBoxStyle.Render(head + "\n" + hint)
}
