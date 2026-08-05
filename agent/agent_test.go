package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// pairs builds a history of `rounds` rounds, each an assistant message
// followed by toolsPerRound tool results.
func pairs(rounds, toolsPerRound int) []ChatMessage {
	msgs := []ChatMessage{{Role: "user", Content: "task"}}
	for r := 0; r < rounds; r++ {
		var calls []ChatToolCall
		for k := 0; k < toolsPerRound; k++ {
			calls = append(calls, ChatToolCall{ID: fmt.Sprintf("r%d-%d", r, k)})
		}
		msgs = append(msgs, ChatMessage{Role: "assistant", ToolCalls: calls})
		for k := 0; k < toolsPerRound; k++ {
			msgs = append(msgs, ChatMessage{Role: "tool", ToolCallID: fmt.Sprintf("r%d-%d", r, k)})
		}
	}
	return msgs
}

func TestPlanCompaction(t *testing.T) {
	for _, c := range []struct {
		name          string
		rounds, tools int
		keep, want    int
	}{
		{"nothing to drop", 1, 1, 3, 0},
		{"exactly keep", 3, 1, 3, 0},
		{"single-tool rounds", 5, 1, 3, 5},
		{"two-tool rounds", 5, 2, 2, 10},
		{"parallel tool calls", 4, 3, 3, 5},
	} {
		msgs := pairs(c.rounds, c.tools)
		if got := planCompaction(msgs, c.keep); got != c.want {
			t.Errorf("%s: planCompaction(%d msgs, keep %d)=%d want %d", c.name, len(msgs), c.keep, got, c.want)
		}
	}
}

// A cut in the middle of a round would strand a tool_result whose tool_use has
// been summarized away; both provider APIs reject that history.
func TestPlanCompaction_neverStrandsToolResults(t *testing.T) {
	for tools := 1; tools <= 4; tools++ {
		for rounds := 1; rounds <= 8; rounds++ {
			for keep := 1; keep <= 4; keep++ {
				msgs := pairs(rounds, tools)
				cut := planCompaction(msgs, keep)
				if cut == 0 {
					continue
				}
				if cut >= len(msgs) {
					t.Fatalf("tools=%d rounds=%d keep=%d: cut %d out of range (%d msgs)", tools, rounds, keep, cut, len(msgs))
				}
				if msgs[cut].Role == "tool" {
					t.Errorf("tools=%d rounds=%d keep=%d: cut at %d leaves an orphan tool_result", tools, rounds, keep, cut)
				}
			}
		}
	}
}

// Each compaction must fold the previous summary in. Dropping it meant every
// compaction silently discarded whatever the one before it had condensed.
func TestCompaction_carriesTheRunningSummary(t *testing.T) {
	var sawPrevious []bool
	round := 0
	ag := New(&fakeClient{responses: []*ChatResponse{
		toolUseResp("noop", nil), toolUseResp("noop", nil), toolUseResp("noop", nil),
		toolUseResp("noop", nil), textResp("done"),
	}}, "m", "s",
		// Above what messages[0] costs on its own — below that, compaction
		// correctly declines because it could never get under the limit.
		WithMaxContextTokens(30), WithKeepRecentTurns(1),
		WithSummarizer(func(_ context.Context, initial string, prefix []ChatMessage) (string, error) {
			round++
			found := false
			for _, m := range prefix {
				if strings.Contains(m.Content, "FACT-A") {
					found = true
				}
			}
			sawPrevious = append(sawPrevious, found)
			if initial != "ORIGINAL TASK" {
				t.Errorf("summarizer round %d got initialUser %q, want the original task", round, initial)
			}
			return fmt.Sprintf("FACT-%c", 'A'+round-1), nil
		}),
	)
	ag.RegisterTool(Tool{Name: "noop", InputSchema: map[string]any{},
		Run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }})

	if _, err := ag.Run(context.Background(), "ORIGINAL TASK"); err != nil {
		t.Fatal(err)
	}
	if round < 2 {
		t.Fatalf("expected at least 2 compactions, got %d", round)
	}
	if !sawPrevious[1] {
		t.Error("the second compaction did not receive the first summary; earlier findings are lost")
	}
	if !strings.HasPrefix(ag.messages[0].Content, "ORIGINAL TASK") {
		t.Errorf("messages[0] lost the original task: %q", ag.messages[0].Content)
	}
}

// A compacted session that is saved and resumed must not nest another summary
// header inside what it thinks is the task.
func TestResume_splitsOutAnEarlierSummary(t *testing.T) {
	ag := New(&fakeClient{}, "m", "s")
	ag.Resume([]ChatMessage{
		{Role: "user", Content: "ORIGINAL TASK" + summaryHeader + "S1"},
		{Role: "assistant", Content: "ok"},
	})
	if ag.initialUser != "ORIGINAL TASK" {
		t.Errorf("initialUser = %q, want the task without the summary", ag.initialUser)
	}
	if ag.runningSummary != "S1" {
		t.Errorf("runningSummary = %q, want S1", ag.runningSummary)
	}
	if got := strings.Count(ag.messages[0].Content, summaryHeader); got != 1 {
		t.Errorf("header appears %d times after resume", got)
	}
}

// keepRecentTurns used to be fixed, so a history whose newest rounds alone
// blew the budget never compacted and every iteration re-sent an oversized
// request.
func TestCompaction_shrinksKeepWhenRecentRoundsAreHuge(t *testing.T) {
	msgs := pairs(3, 1) // 1 user + 3 rounds → only 3 round starts
	if got := planCompaction(msgs, 3); got != 0 {
		t.Fatalf("precondition: planCompaction with keep=3 should be a no-op, got %d", got)
	}
	calls := 0
	// Small enough that the history is over budget, large enough that
	// messages[0] on its own is not — otherwise compaction is pointless and
	// correctly declines to run.
	ag := New(&fakeClient{responses: []*ChatResponse{textResp("done")}}, "m", "s",
		WithMaxContextTokens(20), WithKeepRecentTurns(3),
		WithSummarizer(func(context.Context, string, []ChatMessage) (string, error) {
			calls++
			return "S", nil
		}))
	ag.initialUser = "task"
	ag.messages = msgs
	if estimateTokens(msgs[:1]) > 20 || estimateTokens(msgs) <= 20 {
		t.Fatalf("precondition: first=%d total=%d, want first<=20<total",
			estimateTokens(msgs[:1]), estimateTokens(msgs))
	}
	if err := ag.maybeCompact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("compaction silently gave up instead of keeping fewer rounds")
	}
	if len(ag.messages) >= len(msgs) {
		t.Errorf("history did not shrink: %d → %d", len(msgs), len(ag.messages))
	}
	if ag.messages[1].Role == "tool" {
		t.Error("shrinking keepRounds broke tool_use/tool_result pairing")
	}
}

// An opening prompt that alone exceeds the budget can never be compacted under
// it, so paying for a summarizer call on every iteration is pure waste.
func TestCompaction_givesUpWhenTheTaskAloneExceedsTheBudget(t *testing.T) {
	calls := 0
	ag := New(&fakeClient{}, "m", "s",
		WithMaxContextTokens(50), WithKeepRecentTurns(1),
		WithSummarizer(func(context.Context, string, []ChatMessage) (string, error) {
			calls++
			return "S", nil
		}))
	ag.initialUser = strings.Repeat("x", 4000)
	ag.messages = pairs(4, 1)
	ag.messages[0] = ChatMessage{Role: "user", Content: ag.initialUser}

	for i := 0; i < 5; i++ {
		if err := ag.maybeCompact(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Errorf("summarizer ran %d times on a history it cannot shrink under the limit", calls)
	}
	// It must still fold: refusing outright would let the history grow without
	// limit until the provider rejects it.
	if len(ag.messages) >= 9 {
		t.Errorf("history did not shrink at all: %d messages", len(ag.messages))
	}
	if ag.messages[1].Role == "tool" {
		t.Error("summary-free folding broke tool_use/tool_result pairing")
	}
	if !strings.Contains(ag.messages[0].Content, "생략") {
		t.Error("the elision was not disclosed to the model")
	}
}

// Growth must stay bounded even when the budget can never be met.
func TestCompaction_boundedWhenHopeless(t *testing.T) {
	ag := New(&fakeClient{}, "m", "s",
		WithMaxContextTokens(50), WithKeepRecentTurns(2),
		WithSummarizer(func(context.Context, string, []ChatMessage) (string, error) {
			t.Error("summarizer should not be called when it cannot help")
			return "", nil
		}))
	ag.initialUser = strings.Repeat("x", 4000)
	ag.messages = []ChatMessage{{Role: "user", Content: ag.initialUser}}

	for round := 0; round < 40; round++ {
		ag.messages = append(ag.messages,
			ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{{ID: "t"}}},
			ChatMessage{Role: "tool", ToolCallID: "t", Content: strings.Repeat("y", 200)})
		if err := ag.maybeCompact(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(ag.messages) > 8 {
		t.Errorf("history grew to %d messages; compaction is not bounding it", len(ag.messages))
	}
	// Message count is not the whole story: an elision note appended to the
	// running summary on every compaction grew messages[0] a line at a time,
	// which is the unbounded growth this path exists to prevent.
	if got := len(ag.messages[0].Content); got > len(ag.initialUser)+300 {
		t.Errorf("messages[0] grew to %d bytes from a %d-byte task; the elision note is accumulating",
			got, len(ag.initialUser))
	}
	if n := strings.Count(ag.messages[0].Content, "생략"); n > 1 {
		t.Errorf("elision note repeated %d times in messages[0]", n)
	}
}

// Multi-turn histories interleave extra user messages; those start rounds too.
func TestPlanCompaction_multiTurn(t *testing.T) {
	msgs := pairs(2, 2)
	msgs = append(msgs, ChatMessage{Role: "user", Content: "follow-up"})
	msgs = append(msgs, pairs(2, 2)[1:]...)
	cut := planCompaction(msgs, 2)
	if cut == 0 {
		t.Fatal("expected a compaction point")
	}
	if msgs[cut].Role == "tool" {
		t.Errorf("cut at %d lands on a tool result", cut)
	}
}

func TestUnregisterTool(t *testing.T) {
	ag := New(&fakeClient{}, "m", "s")
	ag.RegisterTool(Tool{Name: "x", InputSchema: map[string]any{}, Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	ag.UnregisterTool("x")
	defs := ag.ToolDefs()
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
