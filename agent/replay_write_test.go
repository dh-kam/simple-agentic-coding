package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_replayGLMWrite replays a captured write+read scenario
// (testdata/glm_write). The replayed write tool_use must actually create the
// file under the test's temp base, and read_file reads it back — proving
// multi-tool capture/replay works on real GLM data, with no network.
func TestRun_replayGLMWrite(t *testing.T) {
	rc, err := NewReplayClient("testdata/glm_write")
	if err != nil {
		t.Skipf("no recorded fixtures — record with AGENT_RECORD_DIR=agent/testdata/glm_write go run . : %v", err)
	}
	base := t.TempDir()
	ag := New(rc, "glm-5.2", "너는 코딩 어시스턴트다.", WithPlanner(NewLLMPlanner(rc, "glm-5.2")))
	ag.RegisterTool(NewWriteTool(base, nil))
	ag.RegisterTool(NewReadFileTool(base))

	ans, err := ag.Run(context.Background(), "write tmp_write.txt then read it back")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(ans) == "" {
		t.Error("empty answer")
	}
	b, rerr := os.ReadFile(filepath.Join(base, "tmp_write.txt"))
	if rerr != nil {
		t.Fatalf("replayed write tool did not create tmp_write.txt: %v", rerr)
	}
	if !strings.Contains(string(b), "hello-from-agent") {
		t.Errorf("file content = %q, want hello-from-agent", b)
	}
	// planner + 3 executor calls (write, read_file, final)
	if got := len(rc.params); got != 4 {
		t.Errorf("LLM calls = %d, want 4 (planner + write + read + final)", got)
	}
}
