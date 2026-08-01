package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeClient implements Backend for testing.
type fakeClient struct {
	responses []*ChatResponse
	i         int
	Reqs      []ChatRequest
}

func (f *fakeClient) Chat(_ context.Context, _ ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	if f.i >= len(f.responses) {
		return nil, errors.New("fake client: exhausted")
	}
	resp := f.responses[f.i]
	f.i++
	if onDelta != nil && resp.Content != "" {
		onDelta(resp.Content)
	}
	return resp, nil
}

func toolUseResp(name string, args map[string]any) *ChatResponse {
	argBytes, _ := json.Marshal(args)
	return &ChatResponse{
		ToolCalls:  []ChatToolCall{{ID: "tu1", Name: name, Arguments: argBytes}},
		StopReason: "tool_use",
	}
}

func textResp(text string) *ChatResponse {
	return &ChatResponse{Content: text, StopReason: "end_turn"}
}

func TestRun_loopsThroughTool(t *testing.T) {
	base := t.TempDir()
	os.WriteFile(filepath.Join(base, "hello.txt"), []byte("Hello from fixture"), 0644)

	fc := &fakeClient{responses: []*ChatResponse{
		toolUseResp("read_file", map[string]any{"path": "hello.txt"}),
		textResp("완료했습니다."),
	}}
	ag := New(fc, "m", "s")
	ag.RegisterTool(NewReadFileTool(base))

	out, err := ag.Run(context.Background(), "read hello.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "완료했습니다." {
		t.Errorf("out=%q", out)
	}
	if fc.i != 2 {
		t.Errorf("calls=%d want 2", fc.i)
	}
}

func TestRun_directAnswer(t *testing.T) {
	fc := &fakeClient{responses: []*ChatResponse{textResp("안녕")}}
	ag := New(fc, "m", "s")
	out, err := ag.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "안녕" {
		t.Errorf("out=%q", out)
	}
}

func TestRun_concurrentTools(t *testing.T) {
	fc := &fakeClient{responses: []*ChatResponse{
		{ToolCalls: []ChatToolCall{
			{ID: "t1", Name: "slow", Arguments: json.RawMessage("{}")},
			{ID: "t2", Name: "slow", Arguments: json.RawMessage("{}")},
		}, StopReason: "tool_use"},
		textResp("done"),
	}}
	ag := New(fc, "m", "s")
	ag.RegisterTool(Tool{Name: "slow", InputSchema: map[string]any{}, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		return "ok", nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := ag.Run(ctx, "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Errorf("out=%q", out)
	}
}

func TestRun_compactsWhenOverBudget(t *testing.T) {
	fc := &fakeClient{responses: []*ChatResponse{
		toolUseResp("read_file", map[string]any{"path": "x"}),
		toolUseResp("read_file", map[string]any{"path": "x"}),
		textResp("done"),
	}}
	ag := New(fc, "m", "s",
		WithMaxContextTokens(1),
		WithKeepRecentTurns(1),
		WithSummarizer(func(_ context.Context, _ string, _ []ChatMessage) (string, error) {
			return "SUMMARY", nil
		}),
	)
	ag.RegisterTool(NewReadFileTool(t.TempDir()))
	_, err := ag.Run(context.Background(), "do it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRun_replayWithFakePlanner(t *testing.T) {
	fc := &fakeClient{responses: []*ChatResponse{textResp("done")}}
	fp := &fakePlanner{plan: "1. step"}
	ag := New(fc, "m", "s", WithPlanner(fp))
	out, err := ag.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Errorf("out=%q", out)
	}
	if !fp.called {
		t.Error("planner not called")
	}
}

func TestSafePath(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"hello.txt", false}, {"sub/../a.txt", false},
		{"", true}, {"/etc/passwd", true}, {"../x", true}, {"../../y", true},
	}
	for _, c := range cases {
		_, err := safePath(base, c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("safePath(%q) err=%v want=%v", c.in, err, c.wantErr)
		}
	}
}

func TestPlanCompaction(t *testing.T) {
	for _, c := range []struct{ msgs, keep, want int }{
		{0, 3, 0}, {1, 3, 0}, {3, 1, 0}, {5, 1, 1}, {7, 2, 1}, {4, 1, 0},
	} {
		if got := planCompaction(c.msgs, c.keep); got != c.want {
			t.Errorf("planCompaction(%d,%d)=%d want %d", c.msgs, c.keep, got, c.want)
		}
	}
}

func TestUnregisterTool(t *testing.T) {
	ag := New(&fakeClient{}, "m", "s")
	ag.RegisterTool(Tool{Name: "x", InputSchema: map[string]any{}, Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	ag.UnregisterTool("x")
	defs := ag.toolDefs()
	for _, d := range defs {
		if d.Name == "x" {
			t.Error("x still registered")
		}
	}
}

func TestHistoryResume(t *testing.T) {
	fc := &fakeClient{responses: []*ChatResponse{textResp("ok")}}
	ag := New(fc, "m", "s")
	ag.RegisterTool(NewReadFileTool(t.TempDir()))
	ag.Run(context.Background(), "hello")
	msgs := ag.History()
	if len(msgs) < 2 {
		t.Fatalf("history len=%d", len(msgs))
	}
	b, _ := json.Marshal(msgs)
	var got []ChatMessage
	json.Unmarshal(b, &got)
	ag2 := New(&fakeClient{responses: []*ChatResponse{textResp("ok")}}, "m", "s")
	ag2.Resume(got)
	if len(ag2.History()) != len(got) {
		t.Errorf("resume mismatch")
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

// extractText is used nowhere now but may be referenced by old tests.
// Keeping a stub to avoid import errors.
func extractTextUnused(_ string) string { return "" }
