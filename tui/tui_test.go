package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dh-kam/simple-agentic-coding/agent"

	"github.com/charmbracelet/bubbletea"
)

// stubClient is a no-op LLMClient for TUI tests that don't run the agent.
type stubClient struct{}

func (stubClient) Chat(context.Context, agent.ChatRequest, func(string)) (*agent.ChatResponse, error) {
	return &agent.ChatResponse{}, nil
}

// TestModel_streamsAndTools drives the model's Update with the same messages
// the agent goroutine would send, headlessly — no terminal, no network.
func TestModel_streamsAndTools(t *testing.T) {
	m := newModel(nil, "test-model", ".")

	m.Update(deltaMsg{text: "Hello "})
	m.Update(deltaMsg{text: "world"})

	m.Update(toolStartMsg{id: "t1", name: "read_file", args: json.RawMessage(`{"path":"hello.txt"}`)})
	m.Update(toolEndMsg{id: "t1", name: "read_file", result: "file body", isErr: false})

	m.Update(doneMsg{})

	h := m.history()
	if !strings.Contains(h, "Hello world") {
		t.Errorf("streamed text missing in history:\n%s", h)
	}
	if !strings.Contains(h, "read_file") {
		t.Errorf("tool name missing in history:\n%s", h)
	}
	if !strings.Contains(h, "✓") {
		t.Errorf("success marker missing:\n%s", h)
	}
	if !strings.Contains(h, "path=hello.txt") {
		t.Errorf("tool arg summary missing:\n%s", h)
	}
	if m.working {
		t.Error("still working after doneMsg")
	}
}

func TestModel_toolError(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.Update(toolStartMsg{id: "e1", name: "run_command", args: json.RawMessage(`{"command":"bad"}`)})
	m.Update(toolEndMsg{id: "e1", name: "run_command", result: "exit 1", isErr: true})
	h := m.history()
	if !strings.Contains(h, "✗") {
		t.Errorf("error marker missing:\n%s", h)
	}
}

// TestSlash_clearAndHelp drives /clear and /help through the key handler.
func TestSlash_clearAndHelp(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.Update(deltaMsg{text: "some streamed text"})
	m.Update(doneMsg{})

	// /clear resets to banner only
	m.input.SetValue("/clear")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.entries) != 1 {
		t.Errorf("after /clear, entries = %d, want 1 (banner)", len(m.entries))
	}

	// /help adds a hint line
	m.input.SetValue("/help")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h := m.history()
	if !strings.Contains(h, "명령") {
		t.Errorf("help hint missing:\n%s", h)
	}

	// unknown command
	m.input.SetValue("/bogus")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h = m.history()
	if !strings.Contains(h, "알 수 없는 명령") {
		t.Errorf("unknown-command message missing:\n%s", h)
	}
}

func TestSummarizeArgs(t *testing.T) {
	if got := summarizeArgs(json.RawMessage(`{"path":"a/b.txt"}`)); got != "path=a/b.txt" {
		t.Errorf("summarizeArgs = %q", got)
	}
	if got := summarizeArgs(json.RawMessage(`{}`)); got == "" {
		t.Error("empty args should fall back to compact json, got empty")
	}
}

func TestRenderFileDiff(t *testing.T) {
	d := renderFileDiff("f.txt", "a\nb\n", "a\nc\n")
	if !strings.Contains(d, "✎ f.txt") {
		t.Errorf("missing header:\n%s", d)
	}
	if !strings.Contains(d, "-b") {
		t.Errorf("missing removed line:\n%s", d)
	}
	if !strings.Contains(d, "+c") {
		t.Errorf("missing added line:\n%s", d)
	}
	// no change → empty
	if renderFileDiff("f", "x", "x") != "" {
		t.Error("expected empty diff for unchanged content")
	}
}

func TestRenderFileDiff_truncated(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	d := renderFileDiff("big.txt", "", sb.String())
	if !strings.Contains(d, "diff truncated") {
		t.Error("large diff should be truncated")
	}
}

func TestInterruptOrQuit(t *testing.T) {
	// working + Ctrl+C → interrupt (stop, do not quit)
	m := newModel(nil, "m", ".")
	m.working = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.working {
		t.Error("Ctrl+C while working should stop working")
	}
	if m.quitting {
		t.Error("Ctrl+C while working should NOT quit")
	}
	if !strings.Contains(m.history(), "중단됨") {
		t.Error("missing interrupted marker")
	}

	// idle + Ctrl+C → quit
	m2 := newModel(nil, "m", ".")
	m2.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m2.quitting {
		t.Error("Ctrl+C while idle should quit")
	}
}

func TestShouldAsk(t *testing.T) {
	for _, n := range []string{"write", "edit", "multi_edit", "run_command"} {
		if !shouldAsk(n) {
			t.Errorf("shouldAsk(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"read_file", "glob", "grep", "list_files", "web_fetch", "todo_write", "task"} {
		if shouldAsk(n) {
			t.Errorf("shouldAsk(%q) = true, want false", n)
		}
	}
}

func TestApprovalFlow(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24}) // enable View

	// a write tool asks permission
	resp := make(chan choice, 1)
	req := approvalReq{id: "a1", name: "write", args: json.RawMessage(`{"path":"x.txt"}`), resp: resp}
	m.Update(approvalMsg{req: req})
	if m.asking == nil {
		t.Fatal("asking not set after approvalMsg")
	}
	if !strings.Contains(m.View(), "승인 필요") {
		t.Error("approval modal not rendered")
	}

	// allow via Enter
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.asking != nil {
		t.Error("asking not cleared after allow")
	}
	c := <-resp
	if !c.allow {
		t.Error("expected allow")
	}

	// deny via 'n'
	resp2 := make(chan choice, 1)
	req2 := approvalReq{id: "a2", name: "run_command", args: json.RawMessage(`{"command":"rm"}`), resp: resp2}
	m.Update(approvalMsg{req: req2})
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	c2 := <-resp2
	if c2.allow {
		t.Error("expected deny")
	}
	if c2.reason == "" {
		t.Error("deny should carry a reason")
	}
}

func TestSaveResumeSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_SESSION", filepath.Join(dir, "s.json"))

	m := newModel(nil, "m", ".")
	m.agent = agent.New(stubClient{}, "m", "s")
	m.agent.Resume([]agent.ChatMessage{{Role: "user", Content: "hello"}})

	if _, err := m.saveSession(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s.json")); err != nil {
		t.Fatalf("session file not written: %v", err)
	}

	// fresh model + agent, load the session
	m2 := newModel(nil, "m", ".")
	m2.agent = agent.New(stubClient{}, "m", "s")
	if _, n, err := m2.loadSession(); err != nil {
		t.Fatalf("load: %v", err)
	} else if n != 1 {
		t.Errorf("loaded %d messages, want 1", n)
	}
	if len(m2.agent.History()) != 1 {
		t.Errorf("history not restored: %d", len(m2.agent.History()))
	}
}

func TestStreamRendering(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(deltaMsg{text: "hello"})
	m.streamRendered = m.renderStream() // simulate a spinner tick

	s := m.streamText()
	if !strings.Contains(s, "hello") {
		t.Errorf("streamed text missing: %q", s)
	}
	if !strings.Contains(s, "▋") {
		t.Errorf("typing cursor missing: %q", s)
	}
}

func TestTabCompletion(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.input.SetValue("/sa")
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "/save" {
		t.Errorf("Tab: value = %q, want /save", m.input.Value())
	}
	// no matching command → unchanged
	m.input.SetValue("/zzz")
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "/zzz" {
		t.Errorf("Tab no-match: value = %q, want /zzz", m.input.Value())
	}
}
