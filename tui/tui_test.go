package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	// working + Ctrl+C → interrupt, but the run goroutine still owns the
	// Agent, so the model must stay "working" until doneMsg lands.
	m := newModel(nil, "m", ".")
	m.working = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.working {
		t.Error("model left the working state before the run reported back")
	}
	if !m.interrupted {
		t.Error("interrupt not recorded")
	}
	if m.quitting.Load() {
		t.Error("Ctrl+C while working should NOT quit")
	}
	if !strings.Contains(m.history(), "중단 중") {
		t.Error("missing interrupting marker")
	}

	// the run finally returns → now the model is idle again
	m.Update(doneMsg{err: context.Canceled})
	if m.working || m.interrupted {
		t.Error("doneMsg should end the interrupt")
	}
	if !strings.Contains(m.history(), "중단됨") {
		t.Error("missing interrupted marker after doneMsg")
	}

	// second Ctrl+C while still winding down → quit
	m2 := newModel(nil, "m", ".")
	m2.working = true
	m2.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m2.quitting.Load() {
		t.Error("a second Ctrl+C should quit")
	}

	// idle + Ctrl+C → quit
	m3 := newModel(nil, "m", ".")
	m3.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m3.quitting.Load() {
		t.Error("Ctrl+C while idle should quit")
	}
}

// agent.Agent is documented as unsafe for concurrent use. Interrupting used to
// clear `working`, which re-opened the key handler and let the next Enter
// start a second Run over the still-live one.
func TestInterrupt_blocksASecondRunAndHistoryReads(t *testing.T) {
	m := newModel(nil, "m", t.TempDir())
	m.agent = agent.New(stubClient{}, "m", "s")
	m.working = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})

	before := len(m.entries)
	m.input.SetValue("another prompt")
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.entries) != before {
		t.Error("Enter started new work while the previous run was still live")
	}

	// /save and /status read agent.History() and must be blocked too
	for _, cmd := range []string{"/save", "/status"} {
		m.input.SetValue(cmd)
		m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	if len(m.entries) != before {
		t.Error("a slash command reached the Agent while a run was still live")
	}
}

func TestShouldAsk(t *testing.T) {
	// notebook_edit writes files and git/git_commit mutate the repository;
	// all three used to be auto-approved.
	for _, n := range []string{"write", "edit", "multi_edit", "run_command",
		"notebook_edit", "git", "git_commit", "kill_shell"} {
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

// send() runs on the agent's goroutines while Update writes quitting on the
// bubbletea loop. Run under -race.
func TestSend_concurrentWithQuit(t *testing.T) {
	m := newModel(nil, "m", ".")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.send(deltaMsg{text: "x"})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			m.quitting.Store(j%2 == 0)
		}
	}()
	wg.Wait()
}

func TestSlash_costAndUndo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel(nil, "m", dir)
	m.agent = agent.New(stubClient{}, "m", "s")

	// /cost with no usage reported
	m.input.SetValue("/cost")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.history(), "사용량") {
		t.Errorf("/cost produced no usage line:\n%s", m.history())
	}

	// /undo with nothing tracked
	m.input.SetValue("/undo")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.history(), "되돌릴 변경이 없습니다") {
		t.Errorf("/undo should report an empty history:\n%s", m.history())
	}

	// snapshot, then /undo restores the previous content
	m.fileHistory.Snapshot(target, "original content")
	m.input.SetValue("/undo")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original content" {
		t.Errorf("undo did not restore the file, got %q", got)
	}
}

func TestSlash_customCommand(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.agent = agent.New(stubClient{}, "m", "s")
	m.customCmds = []agent.CustomCommand{{Name: "/ship", Description: "release", Prompt: "cut a release"}}

	m.input.SetValue("/help")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.history(), "/ship") {
		t.Errorf("custom command missing from /help:\n%s", m.history())
	}

	m.input.SetValue("/ship now")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.working {
		t.Error("custom command did not start a run")
	}
	if strings.Contains(m.history(), "알 수 없는 명령") {
		t.Error("custom command was treated as unknown")
	}
}

// Drives model.approve — the Approver the agent actually calls — rather than
// re-testing Settings.Decide, so the wiring between the two is covered.
func TestApprover_settingsShortCircuitTheModal(t *testing.T) {
	// full-auto and deny answer without ever reaching the modal, which matters
	// because nothing is draining approvalMsg here: a request that queued would
	// block this test forever.
	m := newModel(nil, "m", ".")
	m.settings = &agent.Settings{Mode: "full-auto"}
	if allow, _ := m.approve("run_command", nil); !allow {
		t.Error("full-auto should allow without prompting")
	}

	m.settings = &agent.Settings{Mode: "plan"}
	allow, reason := m.approve("write", nil)
	if allow {
		t.Error("plan mode should refuse writes")
	}
	if reason == "" {
		t.Error("a refusal must tell the model why")
	}

	m.settings = &agent.Settings{DenyTools: []string{"git_commit"}}
	if allow, _ := m.approve("git_commit", nil); allow {
		t.Error("deny_tools was not applied")
	}
	if allow, _ := m.approve("read_file", nil); !allow {
		t.Error("read-only tools should not prompt")
	}
}

// An undecided tool must reach the modal, and the user's answer must come back
// to the blocked agent goroutine.
func TestApprover_undecidedToolReachesTheModal(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	msgs := make(chan tea.Msg, 4)
	m.send = func(msg tea.Msg) { msgs <- msg }

	done := make(chan bool, 1)
	go func() {
		allow, _ := m.approve("write", json.RawMessage(`{"path":"x.txt"}`))
		done <- allow
	}()

	var req approvalReq
	select {
	case msg := <-msgs:
		am, ok := msg.(approvalMsg)
		if !ok {
			t.Fatalf("approver sent %T, want approvalMsg", msg)
		}
		req = am.req
	case <-time.After(2 * time.Second):
		t.Fatal("approver never asked for permission")
	}

	m.Update(approvalMsg{req: req})
	if m.asking == nil {
		t.Fatal("modal not presented")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	select {
	case allow := <-done:
		if allow {
			t.Error("the user's deny was not delivered back to the agent")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approver stayed blocked after the user answered")
	}
}

// After an interrupt, the remaining parallel tools still call the approver.
// They must be answered, not queued behind a modal the user never asked for.
func TestApprover_autoDeniedAfterInterrupt(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.working = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC}) // sets interrupted

	msgs := make(chan tea.Msg, 4)
	m.send = func(msg tea.Msg) { msgs <- msg }

	done := make(chan bool, 1)
	go func() {
		allow, _ := m.approve("write", nil)
		done <- allow
	}()

	select {
	case msg := <-msgs:
		m.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("approver never sent its request")
	}
	if m.asking != nil {
		t.Error("a modal was presented for a cancelled run")
	}
	select {
	case allow := <-done:
		if allow {
			t.Error("a cancelled run's tool was approved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool goroutine leaked: never answered after interrupt")
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

// submit() used to overwrite the single cancel field, so quitting could no
// longer cancel the root context and every turn leaked a child context.
func TestCancel_rootAndRunAreSeparate(t *testing.T) {
	m := newModel(nil, "m", ".")
	m.agent = agent.New(stubClient{}, "m", "s")

	// Wait for each run to really finish before driving the next turn; feeding
	// doneMsg early would start a second Run over a live one, which is exactly
	// what the model is supposed to prevent.
	msgs := make(chan tea.Msg, 8)
	m.send = func(msg tea.Msg) { msgs <- msg }
	runTurn := func(text string) {
		t.Helper()
		m.input.SetValue(text)
		m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
		if m.runCancel == nil {
			t.Fatal("submit did not set a run cancel")
		}
		if m.ctx.Err() != nil {
			t.Error("starting a run cancelled the root context")
		}
		for {
			select {
			case msg := <-msgs:
				m.Update(msg)
				if _, done := msg.(doneMsg); done {
					return
				}
			case <-time.After(2 * time.Second):
				t.Fatal("run never reported back")
			}
		}
	}

	runTurn("first")
	firstRun := m.runCancel
	runTurn("second")
	if firstRun == nil {
		t.Fatal("no run cancel from the first turn")
	}
	// The previous turn's child context must be released, not just replaced:
	// each WithCancel stays registered on m.ctx until it is cancelled.
	if m.runCancel == nil {
		t.Fatal("no run cancel from the second turn")
	}
	if m.ctx.Err() != nil {
		t.Error("root context died between turns")
	}

	// quitting must tear down the root, not just the current run
	m.input.SetValue("/exit")
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.ctx.Err() == nil {
		t.Error("/exit left the root context alive")
	}
}

// Esc used to be bound to the same handler as Ctrl+C, so a stray Esc at an
// idle prompt quit the session — while the help text only promised Ctrl+C.
func TestEsc_interruptsButNeverQuits(t *testing.T) {
	idle := newModel(nil, "m", ".")
	idle.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if idle.quitting.Load() {
		t.Error("Esc at an idle prompt quit the session")
	}

	busy := newModel(nil, "m", ".")
	busy.working = true
	busy.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !busy.interrupted {
		t.Error("Esc while working should interrupt")
	}
	if busy.quitting.Load() {
		t.Error("Esc while working should not quit")
	}
}

// The session file must carry initialUser and the summary, not force the
// agent to re-derive them by splitting messages[0].
func TestSaveSession_persistsCompactionFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_SESSION", filepath.Join(dir, "s.json"))

	m := newModel(nil, "m", ".")
	m.agent = agent.New(stubClient{}, "m", "s")
	m.agent.Restore(agent.Session{
		Messages:    []agent.ChatMessage{{Role: "user", Content: "task + summary"}},
		InitialUser: "task",
		Summary:     "FACT-A",
	})
	if _, err := m.saveSession(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc agent.Session
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.InitialUser != "task" || doc.Summary != "FACT-A" {
		t.Errorf("session dropped the compaction fields: %+v", doc)
	}

	m2 := newModel(nil, "m", ".")
	m2.agent = agent.New(stubClient{}, "m", "s")
	if _, _, err := m2.loadSession(); err != nil {
		t.Fatal(err)
	}
	if got := m2.agent.Snapshot(); got.InitialUser != "task" || got.Summary != "FACT-A" {
		t.Errorf("round trip lost the fields: %+v", got)
	}
}
