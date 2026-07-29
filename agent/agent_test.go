package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// fakeClient returns canned JSON responses in order and records each request.
// It implements StreamMessage: it surfaces each text block of the canned
// message as a single delta (so streaming can be tested) and returns the full
// message. No network.
type fakeClient struct {
	responses []string
	i         int
	params    []anthropic.MessageNewParams
}

func (f *fakeClient) StreamMessage(ctx context.Context, p anthropic.MessageNewParams, onDelta func(string)) (*anthropic.Message, error) {
	f.params = append(f.params, p)
	if f.i >= len(f.responses) {
		return nil, errors.New("fake client: responses exhausted")
	}
	raw := f.responses[f.i]
	f.i++
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return nil, err
	}
	if onDelta != nil {
		for _, b := range msg.Content {
			if tb, ok := b.AsAny().(anthropic.TextBlock); ok {
				onDelta(tb.Text)
			}
		}
	}
	return &msg, nil
}

// First response: model asks to call read_file on "hello.txt".
const respToolUse = `{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "test",
  "content": [
    {"type": "text", "text": "파일을 읽겠습니다."},
    {"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": {"path": "hello.txt"}}
  ],
  "stop_reason": "tool_use"
}`

// Second response: model is done.
const respFinal = `{
  "id": "msg_2", "type": "message", "role": "assistant", "model": "test",
  "content": [{"type": "text", "text": "완료했습니다."}],
  "stop_reason": "end_turn"
}`

// Response with two tool_use blocks (parallel tool calls).
const respTwoTools = `{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "test",
  "content": [
    {"type": "tool_use", "id": "toolu_1", "name": "slow", "input": {}},
    {"type": "tool_use", "id": "toolu_2", "name": "slow", "input": {}}
  ],
  "stop_reason": "tool_use"
}`

// TestRun_loopsThroughTool verifies the full cycle:
// model requests tool -> agent runs read_file -> result fed back -> model answers.
func TestRun_loopsThroughTool(t *testing.T) {
	base := t.TempDir()
	const content = "Hello from fixture"
	if err := os.WriteFile(filepath.Join(base, "hello.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fc := &fakeClient{responses: []string{respToolUse, respFinal}}
	ag := New(fc, "test-model", "sys")

	// Wrap read_file to observe what the loop dispatched.
	rt := NewReadFileTool(base)
	baseRun := rt.Run
	var (
		calledPath string
		gotOutput  string
	)
	rt.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(args, &in)
		calledPath = in.Path
		out, err := baseRun(ctx, args)
		gotOutput = out
		return out, err
	}
	ag.RegisterTool(rt)

	answer, err := ag.Run(context.Background(), "hello.txt 읽어줘")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answer != "완료했습니다." {
		t.Errorf("answer = %q, want %q", answer, "완료했습니다.")
	}
	if calledPath != "hello.txt" {
		t.Errorf("tool called with path %q, want hello.txt", calledPath)
	}
	if gotOutput != content {
		t.Errorf("tool output = %q, want %q", gotOutput, content)
	}
	if fc.i != 2 {
		t.Errorf("LLM calls = %d, want 2", fc.i)
	}
}

// TestRun_directAnswer verifies the terminal branch when the model needs no tool.
func TestRun_directAnswer(t *testing.T) {
	fc := &fakeClient{responses: []string{respFinal}}
	ag := New(fc, "m", "s")

	ans, err := ag.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ans != "완료했습니다." {
		t.Errorf("answer = %q, want %q", ans, "완료했습니다.")
	}
	if fc.i != 1 {
		t.Errorf("LLM calls = %d, want 1", fc.i)
	}
}

// TestRun_concurrentTools proves the two tool_use blocks run concurrently, not
// sequentially: each "slow" invocation signals it has started and then blocks
// until BOTH have started. If execution were sequential, the first would block
// forever (the second would never start) and the context would time out.
func TestRun_concurrentTools(t *testing.T) {
	fc := &fakeClient{responses: []string{respTwoTools, respFinal}}
	ag := New(fc, "m", "s")

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	ag.RegisterTool(Tool{
		Name:        "slow",
		Description: "blocks until released",
		InputSchema: map[string]any{},
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			started <- struct{}{}
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})

	go func() {
		for i := 0; i < 2; i++ {
			<-started
		}
		close(release)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	answer, err := ag.Run(ctx, "run two")
	if err != nil {
		t.Fatalf("Run: %v (tools likely ran sequentially, not concurrently)", err)
	}
	if answer != "완료했습니다." {
		t.Errorf("answer = %q, want %q", answer, "완료했습니다.")
	}
}

// TestRun_streamingSurfacesDeltas verifies onText receives both the
// intermediate text (from the tool-use turn) and the final answer.
func TestRun_streamingSurfacesDeltas(t *testing.T) {
	fc := &fakeClient{responses: []string{respToolUse, respFinal}}
	var got strings.Builder
	ag := New(fc, "m", "s", WithOnText(func(s string) { got.WriteString(s) }))
	ag.RegisterTool(NewReadFileTool(t.TempDir())) // read_file present; "hello.txt" absent -> error result, still fine

	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := got.String()
	if !strings.Contains(joined, "파일을 읽겠습니다.") {
		t.Errorf("onText missing intermediate text: %q", joined)
	}
	if !strings.Contains(joined, "완료했습니다.") {
		t.Errorf("onText missing final text: %q", joined)
	}
}

// TestRun_explicitPlanning verifies the planner runs first, its plan reaches
// onPlan, and the plan is injected into the executor's request.
func TestRun_explicitPlanning(t *testing.T) {
	fc := &fakeClient{responses: []string{respFinal}} // executor answers directly
	fp := &fakePlanner{plan: "1. 파일 읽기\n2. 요약"}
	var planSeen string
	ag := New(fc, "m", "s",
		WithPlanner(fp),
		WithOnPlan(func(p string) { planSeen = p }),
	)

	answer, err := ag.Run(context.Background(), "hello.txt 요약해줘")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !fp.called {
		t.Error("planner not called")
	}
	if fp.got != "hello.txt 요약해줘" {
		t.Errorf("planner received %q, want the original user input", fp.got)
	}
	if planSeen != fp.plan {
		t.Errorf("onPlan = %q, want %q", planSeen, fp.plan)
	}
	if answer != "완료했습니다." {
		t.Errorf("answer = %q, want %q", answer, "완료했습니다.")
	}
	// fakePlanner returns a canned plan without an LLM call, so only the
	// executor call is recorded — and it must carry the injected plan.
	if len(fc.params) != 1 {
		t.Fatalf("LLM calls = %d, want 1 (executor; planner is canned)", len(fc.params))
	}
	execReq, _ := json.Marshal(fc.params[0])
	// JSON escapes newlines, so check the marker + each (newline-free) plan line.
	for _, want := range []string{"## 실행 계획", "1. 파일 읽기", "2. 요약", "hello.txt 요약해줘"} {
		if !strings.Contains(string(execReq), want) {
			t.Errorf("executor request missing %q:\n%s", want, execReq)
		}
	}
}

type fakePlanner struct {
	plan   string
	called bool
	got    string
}

func (f *fakePlanner) Plan(_ context.Context, in string) (string, error) {
	f.called = true
	f.got = in
	return f.plan, nil
}

// TestSafePath verifies the confinement guard.
func TestSafePath(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"hello.txt", false},
		{"sub/../hello.txt", false}, // stays under base after cleaning
		{"", true},
		{"/etc/passwd", true},      // absolute
		{"../secret", true},        // escapes
		{"../../etc/passwd", true}, // escapes
	}
	for _, tc := range tests {
		got, err := safePath(base, tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("safePath(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && !filepath.IsAbs(got) {
			t.Errorf("safePath(%q) = %q, want absolute under base", tc.in, got)
		}
	}
}
