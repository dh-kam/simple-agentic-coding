package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
			full, err := safeWritePath(base, in.Path)
			if err != nil {
				return "", err
			}
			old, _ := os.ReadFile(full)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return "", err
			}
			if err := writeFileNoFollow(full, []byte(in.Content), 0o644); err != nil {
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
			full, err := safeWritePath(base, in.Path)
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
			if err := writeFileNoFollow(full, []byte(updated), 0o644); err != nil {
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

const (
	maxGrepMatches   = 50
	maxGrepFileBytes = 5 << 20 // 5 MiB
)

// errStopWalk aborts a filepath.WalkDir early. filepath.SkipDir cannot do this
// from a file callback — it only skips the remaining entries of the containing
// directory, so a per-match cap enforced with SkipDir leaks in proportion to
// the number of directories walked.
var errStopWalk = errors.New("stop walk")

// skipDirName lists directories never worth searching: huge, generated, and
// full of false positives.
func skipDirName(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", ".venv", "__pycache__":
		return true
	}
	return false
}

// isBinary reports whether data looks like a binary file. A NUL byte in the
// first 8 KiB is the same heuristic grep itself uses.
func isBinary(data []byte) bool {
	if len(data) > 8192 {
		data = data[:8192]
	}
	return bytes.IndexByte(data, 0) >= 0
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
			if rb, err := filepath.EvalSymlinks(cleanBase); err == nil {
				cleanBase = rb
			}
			root := cleanBase
			if in.Path != "" && in.Path != "." {
				// Confine the search root to base. A model-supplied path like
				// "../" must not read files outside base.
				root, err = safePath(base, in.Path)
				if err != nil {
					return "", err
				}
			}
			var out []string
			truncated := false
			err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				if d.IsDir() {
					if skipDirName(d.Name()) {
						return filepath.SkipDir
					}
					return nil
				}
				if in.Glob != "" {
					name := filepath.Base(path)
					if ok, _ := filepath.Match(in.Glob, name); !ok {
						return nil
					}
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				// Never follow a symlink: os.ReadFile would open the target,
				// which may sit outside base. read_file is confined by
				// safePath, and grep must not be the weaker door.
				//
				// IsRegular also rules out FIFOs and devices, where a read
				// would block the tool forever. The size cap matters because
				// grep loads whole files, and read_file already rejects >10MB.
				if !info.Mode().IsRegular() || info.Size() > maxGrepFileBytes {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil || isBinary(data) {
					return nil
				}
				rel, err := filepath.Rel(cleanBase, path)
				if err != nil {
					rel = path
				}
				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if !re.MatchString(line) {
						continue
					}
					out = append(out, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, TruncRunes(strings.TrimRight(line, "\r"), 300)))
					if len(out) >= maxGrepMatches {
						// Returning SkipDir here only skips the rest of *this*
						// directory — the walk continues elsewhere. Only a real
						// error stops it, so use a sentinel.
						truncated = true
						return errStopWalk
					}
				}
				return nil
			})
			if err != nil && !errors.Is(err, errStopWalk) {
				return "", err
			}
			if len(out) == 0 {
				return "no matches", nil
			}
			if truncated {
				out = append(out, fmt.Sprintf("…[%d개에서 잘림 — 패턴을 좁히거나 path/glob으로 범위를 지정하세요]", maxGrepMatches))
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

// pinnedIPs holds the addresses web_fetch is allowed to dial. It is mutated by
// CheckRedirect and read by DialContext, which run on different goroutines
// inside net/http.
type pinnedIPs struct {
	mu  sync.Mutex
	ips []net.IP
}

func (p *pinnedIPs) get() []net.IP {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ips
}

func (p *pinnedIPs) set(ips []net.IP) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ips = ips
}

// maxRedirects bounds a web_fetch redirect chain.
const maxRedirects = 5

// checkRedirect validates every hop and re-pins the dial to its addresses.
// Without it each hop would be dialled at the *first* host's IPs: safe against
// SSRF but silently wrong, since the request lands on a server that was never
// the redirect target — and a hop pointing at an internal address would never
// be examined at all.
func checkRedirect(pin *pinnedIPs) func(*http.Request, []*http.Request) error {
	return func(r *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("too many redirects (%d)", len(via))
		}
		hopIPs, err := validateURL(r.Context(), r.URL.String())
		if err != nil {
			return fmt.Errorf("redirect to %s: %w", redactURL(r.URL), err)
		}
		pin.set(hopIPs)
		return nil
	}
}

// redactURL strips query and userinfo before a URL reaches an error message:
// redirect targets routinely carry tokens, and tool errors go into the
// transcript and the HAR log.
func redactURL(u *url.URL) string {
	c := *u
	c.RawQuery = ""
	c.User = nil
	c.Fragment = ""
	return c.String()
}

// webFetchText turns a fetched body into the text handed to the model: HTML is
// crudely tag-stripped, and the result is capped. The cap counts runes, not
// bytes — slicing bytes split a character on any non-ASCII page.
func webFetchText(contentType string, body []byte) string {
	text := string(body)
	if strings.Contains(strings.ToLower(contentType), "html") {
		text = htmlTagRe.ReplaceAllString(text, " ")
		text = wsRe.ReplaceAllString(text, " ")
	}
	return TruncRunes(strings.TrimSpace(text), 8000)
}

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
			// SSRF protection: resolve the host and require every resolved IP
			// to be public. Returns validated IPs so we can pin the dial and
			// defeat DNS rebinding between validation and connection.
			ips, err := validateURL(ctx, in.URL)
			if err != nil {
				return "", err
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, in.URL, nil)
			if err != nil {
				return "", err
			}
			// Pin the dial to a validated IP so the host cannot re-resolve to
			// a different (private) address after validation.
			pin := &pinnedIPs{ips: ips}
			transport := &http.Transport{
				// Each redirect hop re-pins; a pooled connection keyed by the
				// previous host would sidestep that.
				DisableKeepAlives: true,
				DialContext: func(dctx context.Context, _, addr string) (net.Conn, error) {
					_, port, err := net.SplitHostPort(addr)
					if err != nil {
						return nil, err
					}
					var lastErr error
					for _, ip := range pin.get() {
						c, derr := (&net.Dialer{}).DialContext(dctx, "tcp", net.JoinHostPort(ip.String(), port))
						if derr == nil {
							return c, nil
						}
						lastErr = derr
					}
					if lastErr != nil {
						return nil, lastErr
					}
					return nil, fmt.Errorf("no validated IP to dial")
				},
			}
			httpClient := &http.Client{
				Timeout:       timeout,
				Transport:     transport,
				CheckRedirect: checkRedirect(pin),
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
			if err != nil {
				return "", err
			}
			return webFetchText(resp.Header.Get("content-type"), body), nil
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
	tools, _, reg := CCTools(base, ag.changeHook)
	for _, t := range tools {
		ag.RegisterTool(t)
	}
	ag.shells = reg
	// ag.backend (not the raw backend) so subagent tokens roll up into
	// TotalUsage; ag.Tools resolved at call time so UnregisterTool reaches the
	// subagent too; subagentOptions so it inherits the approval gate.
	ag.RegisterTool(NewTaskTool(NewSubagentRunner(ag.backend, model, system, ag.Tools, ag.subagentOptions()...)))
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
		NewRunCommandTool(base, commandTimeout(), reg),
		NewBashOutputTool(reg),
		NewKillShellTool(reg),
		NewGlobTool(base),
		NewGrepTool(base),
		NewListFilesTool(base),
		NewWebFetchTool(15 * time.Second),
		NewWebSearchTool(),
		NewGitTool(base),
		NewGitCommitTool(base),
		NewReviewTool(base),
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
			full, err := safeWritePath(base, in.Path)
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
			if err := writeFileNoFollow(full, []byte(s), 0o644); err != nil {
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
			full, err := safeWritePath(base, in.Path)
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
					id, idOk := cm["id"].(string)
					if !idOk || id == "" {
						continue
					}
					if id == in.CellID {
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
						id, idOk := cm["id"].(string)
						if !idOk || id == "" {
							continue
						}
						if id == in.CellID {
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
			if err := writeFileNoFollow(full, out, 0o644); err != nil {
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
