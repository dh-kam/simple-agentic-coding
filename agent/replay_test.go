package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestRun_replayRealGLM replays responses captured from a real GLM Coding Plan
// run (testdata/glm_hello, recorded via AGENT_RECORD_DIR). It exercises the
// full pipeline — planner call, streaming, read_file tool execution against the
// real hello.txt, multi-turn loop — using real provider data, with no network.
//
// If fixtures are absent the test is skipped with a hint to record them.
func TestRun_replayRealGLM(t *testing.T) {
	rc, err := NewReplayClient("testdata/glm_hello")
	if err != nil {
		t.Skipf("no recorded GLM fixtures — run `AGENT_RECORD_DIR=agent/testdata/glm_hello go run .` first: %v", err)
	}

	ag := New(rc, "glm-5.2", "너는 코딩 어시스턴트다.",
		WithPlanner(NewLLMPlanner(rc, "glm-5.2")),
	)

	// read_file base = repo root (go test runs from the agent/ package dir).
	rt := NewReadFileTool("..")
	baseRun := rt.Run
	var toolOutput string
	rt.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
		out, err := baseRun(ctx, args)
		toolOutput = out
		return out, err
	}
	ag.RegisterTool(rt)

	answer, err := ag.Run(context.Background(), "hello.txt 파일을 읽고, 그 내용을 한 줄로 요약해줘.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(answer) == "" {
		t.Error("empty answer")
	}
	// the replayed tool_use drove the real read_file against the real file
	if !strings.Contains(toolOutput, "Hello, agentic world!") {
		t.Errorf("read_file did not read the real hello.txt; got %q", toolOutput)
	}
	// planner call + 2 executor calls (tool_use turn + final answer)
	if got := len(rc.params); got != 3 {
		t.Errorf("LLM calls = %d, want 3 (planner + 2 executor)", got)
	}
}
