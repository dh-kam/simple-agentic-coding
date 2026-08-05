package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// commandTimeout is the wall-clock budget for a foreground run_command. The
// old 10s default killed anything real — a build, a test run, a dependency
// install — so the agent could only ever run trivial commands.
func commandTimeout() time.Duration {
	if v := os.Getenv("AGENT_COMMAND_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 120 * time.Second
}

// NewReadFileTool returns a tool that reads a text file under base.
// Paths are confined to base — model output is untrusted, so traversal
// (".."), absolute paths, and symlinks that escape base are rejected.
func NewReadFileTool(base string) Tool {
	return Tool{
		Name:        "read_file",
		Description: "base 디렉토리 내의 텍스트 파일을 읽어 내용을 반환한다. path는 base 기준 상대 경로.",
		InputSchema: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "읽을 파일의 경로 (base 디렉토리 기준 상대 경로)",
			},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			full, err := safePath(base, in.Path)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(full)
			if err != nil {
				return "", err
			}
			// Reading a FIFO or a device blocks forever; only regular files.
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s is not a regular file", in.Path)
			}
			if info.Size() > 10*1024*1024 {
				return "", fmt.Errorf("file too large: %d bytes (max 10MB)", info.Size())
			}
			b, err := os.ReadFile(full)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}
}

// NewRunCommandTool returns a tool that runs a shell command in base.
//
// SECURITY: the command string is model-generated and therefore untrusted.
// This runs it via `sh -c` with a timeout, which fits a personal coding
// assistant the operator drives themselves. For multi-tenant or untrusted
// callers, replace this with an allowlist-based executor (the raw `sh -c`
// here lets the model run anything, by design — like Claude Code's bash).
func NewRunCommandTool(base string, timeout time.Duration, reg *ShellRegistry) Tool {
	return Tool{
		Name:        "run_command",
		Description: "base 디렉토리에서 셸 명령을 실행하고 stdout과 stderr를 합쳐 반환한다. background=true 면 백그라운드로 시작해 shell_id 를 반환(bash_output/kill_shell 사용).",
		InputSchema: map[string]any{
			"command":    map[string]any{"type": "string", "description": "실행할 셸 명령"},
			"background": map[string]any{"type": "boolean", "description": "백그라운드 실행(기본 false)"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Command    string `json:"command"`
				Background bool   `json:"background"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if in.Background {
				if reg == nil {
					return "", errors.New("background shells not available (no registry)")
				}
				cmd := exec.Command("sh", "-c", in.Command)
				cmd.Dir = base
				id, err := reg.Start(cmd)
				if err != nil {
					return "", err
				}
				return "started background shell " + id, nil
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(cctx, "sh", "-c", in.Command)
			cmd.Dir = base
			out, err := cmd.CombinedOutput()
			body := string(out)
			if err != nil {
				// Distinguish a timeout from a non-zero exit: the raw error is
				// "signal: killed", which tells the model nothing about why.
				if cctx.Err() == context.DeadlineExceeded {
					err = fmt.Errorf("command timed out after %s (raise AGENT_COMMAND_TIMEOUT, or use background=true)", timeout)
				}
				// surface output + error so the model can recover; marked is_error
				return body + "\n" + err.Error(), err
			}
			return body, nil
		},
	}
}

// safePath confines p under base, rejecting empty paths, absolute paths,
// traversal, and symlinks that escape base.
func safePath(base, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("absolute path %q not allowed", p)
	}
	cleanBase, err := baseRoot(base)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(cleanBase, p))
	if err != nil {
		return "", err
	}
	if !within(cleanBase, abs) {
		return "", fmt.Errorf("path %q escapes base", p)
	}
	// Resolve symlinks on the full path — catches intermediate directory
	// symlinks, not just the final component.
	resolved, err := resolvePath(abs)
	if err != nil {
		return "", err
	}
	if !within(cleanBase, resolved) {
		return "", fmt.Errorf("path %q resolves outside base", p)
	}
	return resolved, nil
}

// baseRoot normalizes base to an absolute, symlink-resolved directory. Without
// resolving it, a base that is itself a symlink would reject every path, since
// EvalSymlinks(abs) resolves through the link while a raw Clean(base) does not.
func baseRoot(base string) (string, error) {
	cleanBase, err := filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", err
	}
	if rb, err := filepath.EvalSymlinks(cleanBase); err == nil {
		cleanBase = rb
	}
	return cleanBase, nil
}

// resolvePath resolves symlinks in abs. EvalSymlinks fails outright when the
// final component does not exist yet, which is the normal case for creating a
// file — so fall back to resolving the deepest ancestor that does exist and
// re-attaching the missing tail. Without that fallback a symlinked parent
// directory is never checked and writing a new file escapes base through it.
//
// EvalSymlinks fails for a second reason though: a component that *is* a
// symlink whose target does not exist. Treating that as "not created yet"
// would let a dangling link committed to the repository redirect a write to
// any path that does not exist yet — ~/.ssh/authorized_keys, an autostart
// entry — so every such component is refused explicitly.
func resolvePath(abs string) (string, error) {
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r, nil
	}
	if dangling(abs) {
		return "", fmt.Errorf("path %q is a symlink with an unresolvable target", abs)
	}
	var tail []string
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("cannot resolve %q", abs)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r, nil
		}
		// An ancestor that exists but will not resolve is a broken link; do
		// not walk past it, or the check skips the component that redirects.
		if dangling(cur) {
			return "", fmt.Errorf("path component %q is an unresolvable symlink", cur)
		}
	}
}

// dangling reports whether p exists as a symlink that cannot be resolved.
func dangling(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// protectedDirs are directories no tool may write into, at any depth.
//
//   - .git: hooks, config and refs are an execution and exfiltration
//     primitive. The git tool's allowlist rejects `git config` for exactly
//     that reason, and a permission prompt reading "write
//     .git/hooks/pre-commit" is easy to wave through.
//   - .agentic: settings.json holds the permission rules themselves, and
//     skills/ and commands/ are injected into the next session's system
//     prompt. Writing here is the agent granting itself full-auto.
var protectedDirs = []string{".git", ".agentic"}

// safeWritePath is safePath plus a refusal to touch a protected directory.
func safeWritePath(base, p string) (string, error) {
	full, err := safePath(base, p)
	if err != nil {
		return "", err
	}
	root, err := baseRoot(base)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	// Check the requested path as well as the resolved one. safePath resolves
	// symlinks, so a link named .git pointing at an ordinary directory inside
	// base loses the protected segment on the way through.
	for _, candidate := range []string{rel, p} {
		if prot := protectedSegment(candidate); prot != "" {
			return "", fmt.Errorf("refusing to modify %q: %s is off limits", p, prot)
		}
	}
	return full, nil
}

// protectedSegment returns the protected directory a path traverses, or "".
func protectedSegment(path string) string {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		// Compare case-insensitively and ignore trailing dots and spaces:
		// ".GIT" hits .git on macOS's default case-folding APFS, and Windows
		// strips a trailing "." or " " before the filesystem ever sees it.
		seg = strings.TrimRight(seg, ". ")
		for _, prot := range protectedDirs {
			if strings.EqualFold(seg, prot) {
				return prot
			}
		}
	}
	return ""
}

// writeFileNoFollow writes data to path, refusing to follow a symlink at the
// final component.
//
// safePath resolves symlinks, but resolution and the write are two separate
// syscalls, and the agent runs a turn's tool calls concurrently — so the model
// can issue a run_command that swaps in a symlink alongside the write that
// races it. O_NOFOLLOW closes that window: the parent directories are already
// resolved by safePath, and the final component can no longer be redirected.
func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|oNoFollow, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// within reports whether path is base itself or lives under it.
func within(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
