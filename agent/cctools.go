package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// This file adds a Claude-Code-style tool surface on top of read_file/run_command
// (tools.go): Write, Edit, Glob, Grep, ListFiles, WebFetch, TodoWrite. All
// filesystem tools are confined to base via safePath (tools.go).

// NewWriteTool creates/overwrites a file under base.
func NewWriteTool(base string, hook ChangeHook) Tool {
	return Tool{
		Name:        "write",
		Description: "base 디렉토리 내에 파일을 생성하거나 덮어쓴다. 디렉토리는 자동 생성된다.",
		InputSchema: map[string]any{
			"path":    map[string]any{"type": "string", "description": "base 기준 상대 경로"},
			"content": map[string]any{"type": "string", "description": "파일에 쓸 전체 내용"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			full, err := safePath(base, in.Path)
			if err != nil {
				return "", err
			}
			old, _ := os.ReadFile(full)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(full, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			if hook != nil {
				hook(in.Path, string(old), in.Content)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
		},
	}
}

// NewEditTool does exact string replacement (Claude Code Edit semantics):
// by default old_string must match exactly once; replace_all replaces every match.
func NewEditTool(base string, hook ChangeHook) Tool {
	return Tool{
		Name:        "edit",
		Description: "파일 내 old_string 을 new_string 으로 치환한다. 기본적으로 정확히 1곳만 매칭되어야 한다(0개거나 2곳 이상이면 에러). replace_all=true 면 모두 치환.",
		InputSchema: map[string]any{
			"path":        map[string]any{"type": "string", "description": "base 기준 상대 경로"},
			"old_string":  map[string]any{"type": "string", "description": "바꿀 대상 문자열(정확히 일치)"},
			"new_string":  map[string]any{"type": "string", "description": "새 문자열"},
			"replace_all": map[string]any{"type": "boolean", "description": "모든 매칭 치환(기본 false)"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path       string `json:"path"`
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if in.OldString == "" {
				return "", errors.New("old_string must not be empty")
			}
			full, err := safePath(base, in.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return "", err
			}
			s := string(data)
			count := strings.Count(s, in.OldString)
			if count == 0 {
				return "", fmt.Errorf("old_string not found in %s", in.Path)
			}
			if !in.ReplaceAll && count > 1 {
				return "", fmt.Errorf("old_string matches %d places in %s; make it unique or set replace_all", count, in.Path)
			}
			var updated string
			if in.ReplaceAll {
				updated = strings.ReplaceAll(s, in.OldString, in.NewString)
			} else {
				updated = strings.Replace(s, in.OldString, in.NewString, 1)
			}
			if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
				return "", err
			}
			if hook != nil {
				hook(in.Path, string(data), updated)
			}
			return fmt.Sprintf("edited %s (%d replacement(s))", in.Path, count), nil
		},
	}
}

// NewGlobTool lists files under base matching a glob pattern (supports **).
func NewGlobTool(base string) Tool {
	return Tool{
		Name:        "glob",
		Description: "base 아래에서 패턴과 일치하는 파일 경로를 나열한다. ** (任意 깊이) 지원. 예: **/*.go",
		InputSchema: map[string]any{
			"pattern": map[string]any{"type": "string", "description": "glob 패턴 (base 기준)"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			cleanBase, err := filepath.Abs(filepath.Clean(base))
			if err != nil {
				return "", err
			}
			var matches []string
			err = filepath.WalkDir(cleanBase, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || d.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(cleanBase, path)
				if err != nil {
					return nil
				}
				if globMatch(in.Pattern, rel) {
					matches = append(matches, filepath.ToSlash(rel))
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			sort.Strings(matches)
			if len(matches) > 200 {
				matches = matches[:200]
			}
			if len(matches) == 0 {
				return "no matches", nil
			}
			return strings.Join(matches, "\n"), nil
		},
	}
}

// NewGrepTool searches file contents under base with a regex.
func NewGrepTool(base string) Tool {
	return Tool{
		Name:        "grep",
		Description: "base 아래 파일 내용을 정규식으로 검색해 file:line:match 행을 반환한다.",
		InputSchema: map[string]any{
			"pattern":          map[string]any{"type": "string", "description": "정규식"},
			"path":             map[string]any{"type": "string", "description": "검색 시작 하위 디렉토리(기본 '.')"},
			"glob":             map[string]any{"type": "string", "description": "파일 필터 예: *.go"},
			"case_insensitive": map[string]any{"type": "boolean", "description": "대소문자 무시(기본 false)"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Pattern         string `json:"pattern"`
				Path            string `json:"path"`
				Glob            string `json:"glob"`
				CaseInsensitive bool   `json:"case_insensitive"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			flags := ""
			if in.CaseInsensitive {
				flags = "(?i)"
			}
			re, err := regexp.Compile(flags + in.Pattern)
			if err != nil {
				return "", fmt.Errorf("bad regex: %w", err)
			}
			cleanBase, err := filepath.Abs(filepath.Clean(base))
			if err != nil {
				return "", err
			}
			root := cleanBase
			if in.Path != "" && in.Path != "." {
				root = filepath.Join(cleanBase, in.Path)
			}
			var out []string
			matchCount := 0
			err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || d.IsDir() {
					return nil
				}
				if in.Glob != "" {
					name := filepath.Base(path)
					if ok, _ := filepath.Match(in.Glob, name); !ok {
						return nil
					}
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				rel, err := filepath.Rel(cleanBase, path)
				if err != nil {
					rel = path
				}
				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if re.MatchString(line) {
						out = append(out, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, strings.TrimRight(line, "\r")))
						matchCount++
						if matchCount >= 50 {
							return filepath.SkipDir // stop early-ish
						}
					}
				}
				return nil
			})
			if err != nil && err != filepath.SkipDir {
				return "", err
			}
			if len(out) == 0 {
				return "no matches", nil
			}
			return strings.Join(out, "\n"), nil
		},
	}
}

// NewListFilesTool lists entries in a directory under base.
func NewListFilesTool(base string) Tool {
	return Tool{
		Name:        "list_files",
		Description: "base 내 디렉토리의 항목을 나열한다(이름 + 파일/디렉토리 표시).",
		InputSchema: map[string]any{
			"path": map[string]any{"type": "string", "description": "base 기준 디렉토리(기본 '.')"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			sub := in.Path
			if sub == "" {
				sub = "."
			}
			full, err := safePath(base, sub)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(full)
			if err != nil {
				return "", err
			}
			var out []string
			for _, e := range entries {
				kind := "file"
				if e.IsDir() {
					kind = "dir"
				}
				out = append(out, fmt.Sprintf("%s\t%s", kind, e.Name()))
			}
			if len(out) == 0 {
				return "(empty)", nil
			}
			sort.Strings(out)
			return strings.Join(out, "\n"), nil
		},
	}
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)
var wsRe = regexp.MustCompile(`\s+`)

// NewWebFetchTool fetches a URL and returns text (HTML is crudely tag-stripped).
func NewWebFetchTool(timeout time.Duration) Tool {
	return Tool{
		Name:        "web_fetch",
		Description: "URL 을 가져와 텍스트로 반환한다(HTML은 태그 제거). 출력은 잘린다.",
		InputSchema: map[string]any{
			"url": map[string]any{"type": "string", "description": "가져올 URL"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if in.URL == "" {
				return "", errors.New("empty url")
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, in.URL, nil)
			if err != nil {
				return "", err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
			if err != nil {
				return "", err
			}
			text := string(body)
			if strings.Contains(strings.ToLower(resp.Header.Get("content-type")), "html") {
				text = htmlTagRe.ReplaceAllString(text, " ")
				text = wsRe.ReplaceAllString(text, " ")
			}
			text = strings.TrimSpace(text)
			if len(text) > 8000 {
				text = text[:8000] + "\n…[truncated]"
			}
			return text, nil
		},
	}
}

// Todo is a single tracked task.
type Todo struct {
	Subject string `json:"subject"`
	Status  string `json:"status,omitempty"` // pending | in_progress | completed
}

// NewTodoTool stores/updates a task list in the returned *[]Todo (so the host
// can render progress). Mirrors Claude Code's TodoWrite.
func NewTodoTool() (Tool, *[]Todo) {
	store := &[]Todo{}
	return Tool{
		Name:        "todo_write",
		Description: "작업 목록을 저장/갱신한다(진행 추적). todos: [{subject, status}]. status: pending|in_progress|completed.",
		InputSchema: map[string]any{
			"todos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"subject": map[string]any{"type": "string"},
						"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
					},
				},
			},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Todos []Todo `json:"todos"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			*store = in.Todos
			return fmt.Sprintf("%d todos stored", len(in.Todos)), nil
		},
	}, store
}

// BuildCodingAssistant builds a coding-assistant Agent with the full tool set.
func BuildCodingAssistant(backend Backend, model, system, base string, extra ...Option) *Agent {
	ag := New(backend, model, system, extra...)
	tools, _, _ := CCTools(base, ag.changeHook)
	for _, t := range tools {
		ag.RegisterTool(t)
	}
	ag.RegisterTool(NewTaskTool(NewSubagentRunner(backend, model, system, tools)))
	return ag
}

// CCTools returns the full Claude-Code-style tool set registered against base,
// plus the shared todo store and background-shell registry. Register each tool
// with Agent.RegisterTool. (Task/subagent is added separately — see NewTaskTool.)
func CCTools(base string, hook ChangeHook) ([]Tool, *[]Todo, *ShellRegistry) {
	todoTool, todos := NewTodoTool()
	reg := NewShellRegistry()
	return []Tool{
		NewReadFileTool(base),
		NewWriteTool(base, hook),
		NewEditTool(base, hook),
		NewMultiEditTool(base, hook),
		NewNotebookEditTool(base),
		NewRunCommandTool(base, 10*time.Second, reg),
		NewBashOutputTool(reg),
		NewKillShellTool(reg),
		NewGlobTool(base),
		NewGrepTool(base),
		NewListFilesTool(base),
		NewWebFetchTool(15 * time.Second),
		NewWebSearchTool(),
		NewGitTool(),
		NewGitCommitTool(),
		todoTool,
	}, todos, reg
}

// globMatch matches a slash-separated pattern against a slash-separated name,
// supporting ** (any depth) and * / ? within a segment via filepath.Match.
func globMatch(pattern, name string) bool {
	pat := strings.Split(filepath.ToSlash(pattern), "/")
	parts := strings.Split(filepath.ToSlash(name), "/")
	return matchSegs(pat, parts)
}

func matchSegs(pat, parts []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 0 && pat[0] == "**" { // collapse runs of **
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true // trailing ** matches everything remaining
			}
			for i := 0; i <= len(parts); i++ {
				if matchSegs(pat, parts[i:]) {
					return true
				}
			}
			return false
		}
		if len(parts) == 0 {
			return false
		}
		ok, err := filepath.Match(pat[0], parts[0])
		if err != nil || !ok {
			return false
		}
		pat, parts = pat[1:], parts[1:]
	}
	return len(parts) == 0
}

// NewMultiEditTool applies a batch of str_replace edits to one file, in order.
// It only writes if every edit succeeds, so a mid-batch failure leaves the file
// unchanged.
func NewMultiEditTool(base string, hook ChangeHook) Tool {
	return Tool{
		Name:        "multi_edit",
		Description: "한 파일에 여러 str_replace 편집을 순서대로 적용한다. edits: [{old_string, new_string, replace_all?}]. 하나라도 실패하면 파일을 변경하지 않는다.",
		InputSchema: map[string]any{
			"path": map[string]any{"type": "string", "description": "base 기준 경로"},
			"edits": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old_string":  map[string]any{"type": "string"},
						"new_string":  map[string]any{"type": "string"},
						"replace_all": map[string]any{"type": "boolean"},
					},
				},
			},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path  string `json:"path"`
				Edits []struct {
					OldString  string `json:"old_string"`
					NewString  string `json:"new_string"`
					ReplaceAll bool   `json:"replace_all"`
				} `json:"edits"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			full, err := safePath(base, in.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return "", err
			}
			s := string(data)
			applied := 0
			for i, e := range in.Edits {
				if e.OldString == "" {
					return "", fmt.Errorf("edit %d: old_string is empty", i)
				}
				count := strings.Count(s, e.OldString)
				if count == 0 {
					return "", fmt.Errorf("edit %d: old_string not found", i)
				}
				if !e.ReplaceAll && count > 1 {
					return "", fmt.Errorf("edit %d: %d matches; make it unique or set replace_all", i, count)
				}
				if e.ReplaceAll {
					s = strings.ReplaceAll(s, e.OldString, e.NewString)
				} else {
					s = strings.Replace(s, e.OldString, e.NewString, 1)
				}
				applied += count
			}
			if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
				return "", err
			}
			if hook != nil {
				hook(in.Path, string(data), s)
			}
			return fmt.Sprintf("applied %d edit(s) to %s", applied, in.Path), nil
		},
	}
}

// NewNotebookEditTool edits a .ipynb cell: replace a cell's source by cell_id,
// or insert a new cell after cell_id (or at the end). It round-trips the
// notebook as generic JSON so unrelated fields (outputs, metadata, ...) are
// preserved.
func NewNotebookEditTool(base string) Tool {
	return Tool{
		Name:        "notebook_edit",
		Description: ".ipynb 노트북의 셀을 편집한다. mode: replace(셀 source 교체, cell_id 필요) / insert(cell_id 뒤에 새 셀 추가, 빈 cell_id면 끝에).",
		InputSchema: map[string]any{
			"path":       map[string]any{"type": "string", "description": "base 기준 .ipynb 경로"},
			"cell_id":    map[string]any{"type": "string", "description": "replace 대상 / insert 기준 셀 id"},
			"mode":       map[string]any{"type": "string", "enum": []string{"replace", "insert"}, "description": "기본 replace"},
			"new_source": map[string]any{"type": "string", "description": "새 셀 내용"},
			"cell_type":  map[string]any{"type": "string", "enum": []string{"code", "markdown"}, "description": "insert 시 셀 타입(기본 code)"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path      string `json:"path"`
				CellID    string `json:"cell_id"`
				Mode      string `json:"mode"`
				NewSource string `json:"new_source"`
				CellType  string `json:"cell_type"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if in.Mode == "" {
				in.Mode = "replace"
			}
			full, err := safePath(base, in.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return "", err
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				return "", fmt.Errorf("parse ipynb: %w", err)
			}
			cellsAny, _ := doc["cells"].([]any)

			switch in.Mode {
			case "replace":
				found := false
				for i, c := range cellsAny {
					cm, ok := c.(map[string]any)
					if !ok {
						continue
					}
					if id, _ := cm["id"].(string); id == in.CellID {
						cm["source"] = splitSource(in.NewSource)
						if in.CellType != "" {
							cm["cell_type"] = in.CellType
						}
						cellsAny[i] = cm
						found = true
						break
					}
				}
				if !found {
					return "", fmt.Errorf("cell_id %q not found", in.CellID)
				}
			case "insert":
				ct := in.CellType
				if ct == "" {
					ct = "code"
				}
				newCell := map[string]any{"cell_type": ct, "source": splitSource(in.NewSource)}
				idx := len(cellsAny)
				for i, c := range cellsAny {
					if cm, ok := c.(map[string]any); ok {
						if id, _ := cm["id"].(string); id == in.CellID {
							idx = i + 1
							break
						}
					}
				}
				tail := append([]any{}, cellsAny[idx:]...)
				cellsAny = append(cellsAny[:idx], newCell)
				cellsAny = append(cellsAny, tail...)
			default:
				return "", fmt.Errorf("unknown mode %q", in.Mode)
			}

			doc["cells"] = cellsAny
			out, err := json.MarshalIndent(doc, "", " ")
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(full, out, 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s: %s done", in.Path, in.Mode), nil
		},
	}
}

// splitSource turns a multiline string into ipynb source lines (each line but
// the last carries a trailing newline), as []any for generic-map round-tripping.
func splitSource(s string) []any {
	if s == "" {
		return []any{}
	}
	lines := strings.Split(s, "\n")
	out := make([]any, len(lines))
	for i, l := range lines {
		if i < len(lines)-1 {
			out[i] = l + "\n"
		} else {
			out[i] = l
		}
	}
	return out
}
