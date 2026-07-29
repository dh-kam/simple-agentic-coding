package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestPlanCompaction(t *testing.T) {
	cases := []struct {
		msgs, keep, want int
	}{
		{0, 3, 0}, // nothing
		{1, 3, 0}, // only the initial user turn
		{3, 3, 0}, // 1 pair, keep 3
		{3, 1, 0}, // 1 pair, keep 1 (pairs <= keep)
		{5, 1, 1}, // 2 pairs, keep 1 -> summarize 1
		{5, 2, 0}, // 2 pairs, keep 2
		{7, 2, 1}, // 3 pairs, keep 2 -> 1
		{9, 2, 2}, // 4 pairs, keep 2 -> 2
		{4, 1, 0}, // odd tail (incomplete pair)
		{2, 1, 0}, // odd tail
	}
	for _, c := range cases {
		if got := planCompaction(c.msgs, c.keep); got != c.want {
			t.Errorf("planCompaction(%d, %d) = %d, want %d", c.msgs, c.keep, got, c.want)
		}
	}
}

// TestRun_compactsWhenOverBudget drives the loop past the token budget with an
// injected summarizer (no extra LLM call) and checks compaction fires.
func TestRun_compactsWhenOverBudget(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{responses: []string{respToolUse, respToolUse, respFinal}}

	sumCalls := 0
	ag := New(fc, "m", "s",
		WithMaxContextTokens(1),
		WithKeepRecentTurns(1),
		WithSummarizer(func(_ context.Context, _ string, _ []anthropic.MessageParam) (string, error) {
			sumCalls++
			return "SUMMARY", nil
		}),
	)
	ag.RegisterTool(NewReadFileTool(base))

	ans, err := ag.Run(context.Background(), "do it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ans != "완료했습니다." {
		t.Errorf("answer = %q, want %q", ans, "완료했습니다.")
	}
	if sumCalls == 0 {
		t.Error("compaction summarizer was never called (budget never enforced)")
	}
}
