package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dh-kam/simple-agentic-coding/agent"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/bubbletea"
)

// Messages pushed in from the agent goroutine.
type deltaMsg struct{ text string }
type planMsg struct{ plan string }
type toolStartMsg struct {
	id, name string
	args     json.RawMessage
}
type toolEndMsg struct {
	id, name, result string
	isErr            bool
}
type doneMsg struct {
	answer string
	err    error
}
type changeMsg struct {
	path, oldContent, newContent string
}

// Approval flow: the agent's Approver blocks on a per-request channel until the
// user picks allow/deny in the modal. Multiple approvals (parallel tools) queue.
type choice struct {
	allow  bool
	reason string
}
type approvalReq struct {
	id, name string
	args     json.RawMessage
	resp     chan choice
}
type approvalMsg struct{ req approvalReq }

// systemPrompt used by the TUI agent (shared intent with the one-shot CLI).
const systemPrompt = "너는 Go 코드베이스를 돕는 코딩 어시스턴트다. " +
	"필요한 도구를 호출해 단계적으로 작업하고, 마지막에 결과를 간결히 요약해 답한다."

type entryKind int

const (
	kindText entryKind = iota
	kindTool
)

// slashCommands are the TUI commands — used for /help, Tab completion, and the
// live suggestion line.
var slashCommands = []string{"/help", "/clear", "/save", "/resume", "/exit"}

// entry is one rendered line group in the transcript.
type entry struct {
	kind entryKind
	text string     // for kindText (already rendered)
	tool *toolBlock // for kindTool
}

type toolBlock struct {
	name, detail, result string
	done, isErr          bool
}

type model struct {
	client    agent.LLMClient
	agent     *agent.Agent
	modelName string
	base      string
	program   *tea.Program
	ctx       context.Context
	cancel    context.CancelFunc

	width, height int
	input         textarea.Model
	spinner       spinner.Model
	viewport      viewport.Model
	md            mdCache

	entries        []*entry
	active         map[string]*toolBlock
	cur            strings.Builder
	streamRendered string // glamour(cur), refreshed on spinner tick while streaming
	working        bool
	interrupted    bool // set when the user interrupted the current run
	quitting       bool
	askQueue       []approvalReq
	asking         *approvalReq
	approvalSeq    uint64
}

func newModel(client agent.LLMClient, modelName, base string) *model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ta := textarea.New()
	ta.Placeholder = "메시지를 입력하세요…  (Enter 전송 · Ctrl+J 줄바꿈 · /help · Ctrl+C 중단/종료)"
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	// Enter submits (intercepted in handleKey); Ctrl+J inserts a newline.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline"))

	m := &model{
		client:    client,
		modelName: modelName,
		base:      base,
		spinner:   sp,
		input:     ta,
		viewport:  viewport.New(80, 20),
		active:    map[string]*toolBlock{},
	}
	m.entries = append(m.entries, &entry{kind: kindText, text: renderBanner(modelName)})
	return m
}

func (m *model) Init() tea.Cmd {
	return m.input.Focus()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.input.SetWidth(max(20, msg.Width-4))
		m.refresh()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.working {
			m.streamRendered = m.renderStream() // throttled live markdown (~tick rate)
			m.refresh()
			return m, cmd
		}
		return m, nil

	case deltaMsg:
		m.cur.WriteString(msg.text)
		m.refresh()
		return m, nil

	case planMsg:
		m.entries = append(m.entries, &entry{
			kind: kindText,
			text: hintStyle.Render("📝 계획 ") + dimStyle.Render(truncate(msg.plan, 240)),
		})
		m.refresh()
		return m, nil

	case toolStartMsg:
		tb := &toolBlock{name: msg.name, detail: summarizeArgs(msg.args)}
		m.entries = append(m.entries, &entry{kind: kindTool, tool: tb})
		m.active[msg.id] = tb
		m.refresh()
		return m, nil

	case toolEndMsg:
		if tb, ok := m.active[msg.id]; ok {
			tb.done = true
			tb.result = msg.result
			tb.isErr = msg.isErr
			if msg.isErr {
				tb.detail = truncate(msg.result, 72)
			}
			delete(m.active, msg.id)
		}
		m.refresh()
		return m, nil

	case changeMsg:
		// file-modifying tool changed a file → render a diff block
		if d := renderFileDiff(msg.path, msg.oldContent, msg.newContent); d != "" {
			m.entries = append(m.entries, &entry{kind: kindText, text: d})
			m.refresh()
		}
		return m, nil

	case doneMsg:
		// If the user interrupted, the run's late result is expected — swallow it
		// so we don't append a stale error or a partial answer.
		if m.interrupted {
			m.interrupted = false
			m.cur.Reset()
			m.working = false
			m.refresh()
			return m, m.input.Focus()
		}
		if m.cur.Len() > 0 {
			if r, err := m.md.render(m.cur.String(), m.viewport.Width); err == nil && strings.TrimSpace(r) != "" {
				m.entries = append(m.entries, &entry{kind: kindText, text: strings.TrimRight(r, "\n")})
			} else {
				m.entries = append(m.entries, &entry{kind: kindText, text: m.cur.String()})
			}
			m.cur.Reset()
		}
		m.streamRendered = ""
		m.working = false
		if msg.err != nil {
			m.entries = append(m.entries, &entry{kind: kindText, text: errStyle.Render("✗ " + msg.err.Error())})
		}
		m.refresh()
		return m, m.input.Focus()

	case approvalMsg:
		// a tool wants permission; queue and present the first pending one
		m.askQueue = append(m.askQueue, msg.req)
		m.presentNext()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Approval modal takes over key handling while a tool is asking permission.
	if m.asking != nil {
		s := msg.String()
		switch {
		case s == "y" || s == "Y" || msg.Type == tea.KeyEnter:
			m.decide(choice{allow: true})
		case s == "n" || s == "N" || msg.Type == tea.KeyEsc:
			m.decide(choice{allow: false, reason: "사용자가 거부"})
		case msg.Type == tea.KeyCtrlC:
			return m.interruptOrQuit()
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		return m.interruptOrQuit()
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyUp, tea.KeyDown:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	if m.working {
		return m, nil
	}
	// Tab completes a slash command prefix.
	if msg.Type == tea.KeyTab {
		if v := m.input.Value(); strings.HasPrefix(v, "/") {
			for _, c := range slashCommands {
				if strings.HasPrefix(c, v) && c != v {
					m.input.SetValue(c)
					m.input.CursorEnd()
					return m, nil
				}
			}
		}
	}
	if msg.Type == tea.KeyEnter {
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		if strings.HasPrefix(text, "/") {
			return m.handleSlash(text)
		}
		m.entries = append(m.entries, &entry{kind: kindText, text: renderUser(text)})
		return m, m.submit(text)
	}
	// all other keys (typing, backspace, Ctrl+J newline, …) go to the textarea
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refresh() // keep the input box height in sync with its content
	return m, cmd
}

// interruptOrQuit: if the agent is working, cancel the current run and return
// to the prompt (without quitting). If idle, quit. Matches Claude Code's
// "Ctrl+C interrupts; a second one / idle one exits" feel.
func (m *model) interruptOrQuit() (tea.Model, tea.Cmd) {
	m.drainApprovals() // release any tool blocked on permission
	if m.working {
		m.interrupted = true
		if m.cancel != nil {
			m.cancel()
		}
		m.working = false
		m.cur.Reset()
		m.entries = append(m.entries, &entry{kind: kindText, text: errStyle.Render("⏹ 중단됨")})
		m.refresh()
		return m, m.input.Focus()
	}
	m.quitting = true
	if m.cancel != nil {
		m.cancel()
	}
	return m, tea.Quit
}

func (m *model) handleSlash(text string) (tea.Model, tea.Cmd) {
	switch text {
	case "/exit", "/quit":
		m.quitting = true
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "/clear":
		m.entries = []*entry{{kind: kindText, text: renderBanner(m.modelName)}}
		m.active = map[string]*toolBlock{}
		m.refresh()
		return m, nil
	case "/help":
		help := strings.Join([]string{
			titleStyle.Render("agentic — 도움말"),
			hintStyle.Render("명령: /help /clear /save /resume /exit"),
			hintStyle.Render("입력: Enter 전송 · Ctrl+J 줄바꿈 · Tab 명령 자동완성 · Ctrl+C 중단/종료"),
			hintStyle.Render("표시: 도구 호출 ✓/✗ · 파일 변경 diff · 권한 요청 시 y/n"),
		}, "\n")
		m.entries = append(m.entries, &entry{kind: kindText, text: help})
		m.refresh()
		return m, nil
	case "/save":
		if path, err := m.saveSession(); err != nil {
			m.entries = append(m.entries, &entry{kind: kindText, text: errStyle.Render("✗ 저장 실패: " + err.Error())})
		} else {
			m.entries = append(m.entries, &entry{kind: kindText, text: okStyle.Render("💾 세션 저장: ") + dimStyle.Render(path)})
		}
		m.refresh()
		return m, nil
	case "/resume", "/load":
		if path, n, err := m.loadSession(); err != nil {
			m.entries = append(m.entries, &entry{kind: kindText, text: errStyle.Render("✗ 불러오기 실패: " + err.Error())})
		} else {
			m.entries = append(m.entries, &entry{kind: kindText, text: okStyle.Render("📂 세션 복원: ") + dimStyle.Render(fmt.Sprintf("%s (%d messages)", path, n))})
		}
		m.refresh()
		return m, nil
	default:
		m.entries = append(m.entries, &entry{kind: kindText, text: errStyle.Render("알 수 없는 명령: " + text)})
		m.refresh()
		return m, nil
	}
}

// submit kicks off the agent in a goroutine; hooks stream events back as msgs.
func (m *model) submit(text string) tea.Cmd {
	m.working = true
	m.input.Blur()
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	go func() {
		ans, err := m.agent.Run(ctx, text)
		m.send(doneMsg{answer: ans, err: err})
	}()
	return m.spinner.Tick
}

func (m *model) send(msg tea.Msg) {
	if m.quitting || m.program == nil {
		return
	}
	m.program.Send(msg)
}

func (m *model) history() string {
	parts := make([]string, 0, len(m.entries)+1)
	for _, e := range m.entries {
		if e.kind == kindTool {
			parts = append(parts, renderTool(e.tool, m.spinner.View()))
		} else {
			parts = append(parts, e.text)
		}
	}
	if m.cur.Len() > 0 {
		parts = append(parts, m.streamText())
	}
	return strings.Join(parts, "\n\n")
}

// renderStream glamour-renders the in-progress answer (called on the spinner
// tick, so ~10/s regardless of token rate). Falls back to plain text on error.
func (m *model) renderStream() string {
	if m.cur.Len() == 0 {
		return ""
	}
	r, err := m.md.render(m.cur.String(), m.viewport.Width)
	if err != nil || strings.TrimSpace(r) == "" {
		return m.cur.String()
	}
	return strings.TrimRight(r, "\n")
}

// streamText is the live answer shown in the transcript: the rendered answer
// (markdown while streaming) plus a typing cursor.
func (m *model) streamText() string {
	s := m.streamRendered
	if s == "" {
		s = m.cur.String()
	}
	return s + " " + bulletStyle.Render("▋")
}

// updateInputHeight grows/shrinks the textarea with its content (capped).
func (m *model) updateInputHeight() {
	n := strings.Count(m.input.Value(), "\n") + 1
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	m.input.SetHeight(n)
}

func (m *model) refresh() {
	m.updateInputHeight()
	boxH := m.input.Height() + 2 // textarea + border
	if m.height > boxH+1 {
		m.viewport.Height = m.height - boxH - 1
	}
	m.viewport.SetContent(m.history())
	m.viewport.GotoBottom()
}

func (m *model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	var bottom string
	switch {
	case m.asking != nil:
		bottom = renderApproval(m.asking.name, m.asking.args)
	case m.working:
		bottom = dimStyle.Render(m.spinner.View()+" 작업 중…  ") + hintStyle.Render("Ctrl+C 취소")
	default:
		box := boxStyle.Render(m.input.View())
		// live slash-command suggestions while typing a command
		if v := m.input.Value(); strings.HasPrefix(v, "/") {
			var matches []string
			for _, c := range slashCommands {
				if strings.HasPrefix(c, v) {
					matches = append(matches, c)
				}
			}
			if len(matches) > 0 {
				bottom = dimStyle.Render("  "+strings.Join(matches, "   ")) + "\n" + box
			} else {
				bottom = box
			}
		} else {
			bottom = box
		}
	}
	return m.viewport.View() + "\n" + bottom
}

// presentNext shows the next queued approval (if none currently asking).
func (m *model) presentNext() {
	if m.asking != nil || len(m.askQueue) == 0 {
		return
	}
	m.asking = &m.askQueue[0]
	m.askQueue = m.askQueue[1:]
	m.refresh()
}

// decide sends the user's choice to the blocked approver and shows the next.
func (m *model) decide(c choice) {
	if m.asking == nil {
		return
	}
	m.asking.resp <- c
	m.asking = nil
	m.presentNext()
}

// drainApprovals denies all pending/current approvals (on interrupt or quit),
// so blocked tool goroutines don't leak.
func (m *model) drainApprovals() {
	if m.asking != nil {
		m.asking.resp <- choice{allow: false, reason: "취소됨"}
		m.asking = nil
	}
	for _, r := range m.askQueue {
		r.resp <- choice{allow: false, reason: "취소됨"}
	}
	m.askQueue = nil
}

// sessionPath is AGENT_SESSION or the default .agentic/session.json.
func (m *model) sessionPath() string {
	if p := os.Getenv("AGENT_SESSION"); p != "" {
		return p
	}
	return filepath.Join(".agentic", "session.json")
}

// saveSession writes the conversation history to disk.
func (m *model) saveSession() (string, error) {
	if m.agent == nil {
		return "", errors.New("no agent")
	}
	path := m.sessionPath()
	doc := struct {
		Model    string                   `json:"model"`
		Messages []anthropic.MessageParam `json:"messages"`
	}{Model: m.modelName, Messages: m.agent.History()}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// loadSession reads a saved history into the agent (continues the conversation).
func (m *model) loadSession() (string, int, error) {
	if m.agent == nil {
		return "", 0, errors.New("no agent")
	}
	path := m.sessionPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	var doc struct {
		Messages []anthropic.MessageParam `json:"messages"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", 0, err
	}
	m.agent.Resume(doc.Messages)
	return path, len(doc.Messages), nil
}

// renderBanner is the welcome block shown at the top.
func renderBanner(modelName string) string {
	line1 := titleStyle.Render("agentic") + dimStyle.Render("  ·  "+modelName+"  ·  GLM/Anthropic 호환")
	line2 := hintStyle.Render("무엇이든 물어보세요. 코드를 읽고, 고치고, 실행할 수 있습니다.")
	return line1 + "\n" + line2
}
