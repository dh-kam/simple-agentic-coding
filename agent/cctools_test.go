package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// call invokes a tool's Run with marshaled args.
func call(t *testing.T, tool Tool, args any) (string, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Run(context.Background(), b)
}

func TestWriteTool(t *testing.T) {
	base := t.TempDir()
	wt := NewWriteTool(base, nil)

	if _, err := call(t, wt, map[string]any{"path": "sub/a.txt", "content": "hello"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(base, "sub", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Errorf("content = %q, want hello", b)
	}
	if _, err := call(t, wt, map[string]any{"path": "../escape.txt", "content": "x"}); err == nil {
		t.Error("expected traversal error, got nil")
	}
}

func TestEditTool(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "f.txt")
	if err := os.WriteFile(path, []byte("foo bar foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	et := NewEditTool(base, nil)

	// unique replacement
	if _, err := call(t, et, map[string]any{"path": "f.txt", "old_string": "bar", "new_string": "baz"}); err != nil {
		t.Fatalf("unique edit: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "foo baz foo" {
		t.Errorf("after unique edit = %q", got)
	}
	// not found
	if _, err := call(t, et, map[string]any{"path": "f.txt", "old_string": "nope", "new_string": "x"}); err == nil {
		t.Error("expected not-found error")
	}
	// ambiguous (2 matches) without replace_all
	if _, err := call(t, et, map[string]any{"path": "f.txt", "old_string": "foo", "new_string": "qux"}); err == nil {
		t.Error("expected multiple-match error")
	}
	// replace_all
	if _, err := call(t, et, map[string]any{"path": "f.txt", "old_string": "foo", "new_string": "qux", "replace_all": true}); err != nil {
		t.Fatalf("replace_all: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "qux baz qux" {
		t.Errorf("after replace_all = %q", got)
	}
}

func TestGlobTool(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(base, "a", "b", "x.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(base, "a", "y.go"), []byte("y"), 0o644)
	os.WriteFile(filepath.Join(base, "z.txt"), []byte("z"), 0o644)

	out, err := call(t, NewGlobTool(base), map[string]any{"pattern": "**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a/b/x.go") || !strings.Contains(out, "a/y.go") {
		t.Errorf("missing .go matches: %s", out)
	}
	if strings.Contains(out, "z.txt") {
		t.Errorf("non-matching file included: %s", out)
	}
}

func TestGrepTool(t *testing.T) {
	base := t.TempDir()
	os.WriteFile(filepath.Join(base, "f.go"), []byte("package main\nfunc foo() {}\n"), 0o644)
	os.WriteFile(filepath.Join(base, "g.md"), []byte("# Title\nfoo here\n"), 0o644)

	out, err := call(t, NewGrepTool(base), map[string]any{"pattern": "foo", "glob": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "f.go:2:") || !strings.Contains(out, "func foo()") {
		t.Errorf("missing match: %s", out)
	}
	if strings.Contains(out, "g.md") {
		t.Errorf("glob filter failed: %s", out)
	}
}

func TestListFilesTool(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "d"), 0o755)
	os.WriteFile(filepath.Join(base, "f.txt"), []byte("x"), 0o644)

	out, err := call(t, NewListFilesTool(base), map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "file\tf.txt") || !strings.Contains(out, "dir\td") {
		t.Errorf("unexpected listing: %s", out)
	}
}

func TestTodoTool(t *testing.T) {
	tool, store := NewTodoTool()
	if _, err := call(t, tool, map[string]any{
		"todos": []map[string]any{
			{"subject": "a", "status": "pending"},
			{"subject": "b", "status": "in_progress"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(*store) != 2 {
		t.Fatalf("store len = %d, want 2", len(*store))
	}
	if (*store)[1].Subject != "b" {
		t.Errorf("second todo = %+v", (*store)[1])
	}
}

func TestWebFetchTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><p>Hello <b>world</b></p></body></html>")
	}))
	defer srv.Close()

	out, err := call(t, NewWebFetchTool(5*time.Second), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(out, "Hello world") {
		t.Errorf("missing text: %s", out)
	}
	if strings.Contains(out, "<") {
		t.Errorf("html tags not stripped: %s", out)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "a.txt", false},
		{"*.go", "x.go", true},
		{"*.go", "a/x.go", false},
		{"src/**/*.go", "src/a/b/c.go", true},
		{"src/**/*.go", "other/x.go", false},
		{"**", "anything/here", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q,%q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestCCToolsBundle(t *testing.T) {
	tools, todos, reg := CCTools(t.TempDir(), nil)
	names := map[string]bool{}
	for _, t := range tools {
		names[t.Name] = true
	}
	for _, want := range []string{
		"read_file", "write", "edit", "multi_edit", "notebook_edit",
		"run_command", "bash_output", "kill_shell",
		"glob", "grep", "list_files", "web_fetch", "todo_write",
	} {
		if !names[want] {
			t.Errorf("CCTools missing %q", want)
		}
	}
	if todos == nil {
		t.Error("todos store is nil")
	}
	if reg == nil {
		t.Error("shell registry is nil")
	}
}

func TestChangeHook(t *testing.T) {
	base := t.TempDir()
	var gotPath, gotOld, gotNew string

	// write creates a new file: old="", new=content
	wt := NewWriteTool(base, func(p, o, n string) { gotPath, gotOld, gotNew = p, o, n })
	if _, err := call(t, wt, map[string]any{"path": "a.txt", "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "a.txt" || gotOld != "" || gotNew != "hello" {
		t.Errorf("write hook: path=%q old=%q new=%q", gotPath, gotOld, gotNew)
	}

	// edit: old=existing content, new=updated
	et := NewEditTool(base, func(p, o, n string) { gotPath, gotOld, gotNew = p, o, n })
	if _, err := call(t, et, map[string]any{"path": "a.txt", "old_string": "hello", "new_string": "world"}); err != nil {
		t.Fatal(err)
	}
	if gotOld != "hello" || gotNew != "world" {
		t.Errorf("edit hook: old=%q new=%q", gotOld, gotNew)
	}
}
