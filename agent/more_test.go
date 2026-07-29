package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMultiEditTool(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "f.txt")
	if err := os.WriteFile(path, []byte("aaa bbb aaa\nccc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := NewMultiEditTool(base, nil)

	// batch: unique replace + replace_all, applied in order
	if _, err := call(t, mt, map[string]any{"path": "f.txt", "edits": []map[string]any{
		{"old_string": "bbb", "new_string": "BBB"},
		{"old_string": "aaa", "new_string": "X", "replace_all": true},
	}}); err != nil {
		t.Fatalf("multiedit: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "X BBB X\nccc\n" {
		t.Errorf("result = %q", got)
	}

	// a mid-batch failure must leave the file unchanged (edit0 ok, edit1 missing)
	before, _ := os.ReadFile(path)
	if _, err := call(t, mt, map[string]any{"path": "f.txt", "edits": []map[string]any{
		{"old_string": "BBB", "new_string": "Y"},  // unique, would succeed
		{"old_string": "nope", "new_string": "z"}, // missing -> abort
	}}); err == nil {
		t.Error("expected error on missing old_string")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("file changed despite mid-batch failure:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestNotebookEditTool(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "n.ipynb")
	seed := `{"cells":[{"cell_type":"code","id":"c1","source":["old\n","line"],"outputs":[{"data":"keep"}],"execution_count":1},{"cell_type":"markdown","id":"c2","source":["# Hi"]}],"metadata":{},"nbformat":4,"nbformat_minor":5}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	nt := NewNotebookEditTool(base)

	// replace c1 source; outputs/execution_count must survive the round-trip
	if _, err := call(t, nt, map[string]any{"path": "n.ipynb", "cell_id": "c1", "new_source": "print(1)"}); err != nil {
		t.Fatalf("nb replace: %v", err)
	}
	doc := readJSON(t, path)
	c0 := doc["cells"].([]any)[0].(map[string]any)
	src := c0["source"].([]any)
	if len(src) != 1 || src[0] != "print(1)" {
		t.Errorf("source not replaced: %v", src)
	}
	if c0["outputs"] == nil {
		t.Error("outputs were lost on round-trip")
	}
	if c0["execution_count"] != float64(1) {
		t.Error("execution_count was lost on round-trip")
	}

	// insert a new code cell after c1
	if _, err := call(t, nt, map[string]any{"path": "n.ipynb", "cell_id": "c1", "mode": "insert", "new_source": "x=1", "cell_type": "code"}); err != nil {
		t.Fatalf("nb insert: %v", err)
	}
	doc = readJSON(t, path)
	if got := len(doc["cells"].([]any)); got != 3 {
		t.Errorf("after insert, cells = %d, want 3", got)
	}
}

func TestBackgroundShell(t *testing.T) {
	reg := NewShellRegistry()

	// background a fast command and read its buffered output
	id, err := reg.Start(exec.Command("sh", "-c", "echo hello"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, _, _ := reg.Output(id)
		if strings.Contains(out, "hello") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, _, err := reg.Output(id)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("buffered output = %q, want hello", out)
	}

	// kill a long-running shell
	id2, err := reg.Start(exec.Command("sh", "-c", "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Kill(id2); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// tool wrappers
	bo := NewBashOutputTool(reg)
	ks := NewKillShellTool(reg)
	if _, err := call(t, bo, map[string]any{"shell_id": id}); err != nil {
		t.Errorf("bash_output: %v", err)
	}
	if _, err := call(t, ks, map[string]any{"shell_id": "shell-999"}); err == nil {
		t.Error("expected unknown shell_id error")
	}
}

func TestApprover(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "secret.txt"), []byte("topsecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	tuSecret := strings.ReplaceAll(respToolUse, "hello.txt", "secret.txt")
	fc := &fakeClient{responses: []string{tuSecret, respFinal}}

	seen := false
	ag := New(fc, "m", "s",
		WithApprover(func(name string, _ json.RawMessage) (bool, string) {
			if name == "read_file" {
				seen = true
				return false, "secret files are off-limits"
			}
			return true, ""
		}),
	)
	ag.RegisterTool(NewReadFileTool(base))

	if _, err := ag.Run(context.Background(), "read secret.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !seen {
		t.Error("approver was never consulted for read_file")
	}
	// the denial must be fed back to the model as the tool_result
	b, _ := json.Marshal(fc.params[1])
	if !strings.Contains(string(b), "secret files are off-limits") {
		t.Errorf("denial not fed back to model:\n%s", b)
	}
}

func TestTaskTool(t *testing.T) {
	tt := NewTaskTool(func(_ context.Context, prompt string) (string, error) {
		return "SUB:" + prompt, nil
	})
	out, err := call(t, tt, map[string]any{"prompt": "find x"})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if out != "SUB:find x" {
		t.Errorf("task output = %q", out)
	}
}

func TestNewSubagentRunner_runsLoop(t *testing.T) {
	fc := &fakeClient{responses: []string{respFinal}}
	runner := NewSubagentRunner(fc, "m", "s", []Tool{NewReadFileTool(t.TempDir())})
	out, err := runner(context.Background(), "hi")
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if out != "완료했습니다." {
		t.Errorf("runner output = %q", out)
	}
}

// TestNewSubagentRunner_excludesTask proves the subagent cannot call the parent's
// "task" tool: a tool_use for "task" is answered with unknown-tool, and the
// parent task Run is never invoked.
func TestNewSubagentRunner_excludesTask(t *testing.T) {
	const taskUse = `{"id":"m1","type":"message","role":"assistant","model":"t","content":[{"type":"tool_use","id":"tu1","name":"task","input":{"prompt":"x"}}],"stop_reason":"tool_use"}`
	fc := &fakeClient{responses: []string{taskUse, respFinal}}

	calls := 0
	parentTools := []Tool{
		{Name: "task", Run: func(context.Context, json.RawMessage) (string, error) {
			calls++
			return "should not run", nil
		}},
		NewReadFileTool(t.TempDir()),
	}
	runner := NewSubagentRunner(fc, "m", "s", parentTools)

	out, err := runner(context.Background(), "go")
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if out != "완료했습니다." {
		t.Errorf("runner output = %q", out)
	}
	if calls != 0 {
		t.Errorf("subagent invoked the parent task tool %d time(s); task must be excluded", calls)
	}
}
