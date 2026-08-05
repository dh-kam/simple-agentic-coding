package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
				// surface output + error so the model can recover; marked is_error
				return body + "\n" + err.Error(), err
			}
			return body, nil
		},
	}
}

// safePath confines p under base, rejecting empty paths, absolute paths,
// and traversal that escapes base.
// safePath confines p under base, rejecting empty paths, absolute paths,
// traversal, and symlinks that escape base.
func safePath(base, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("absolute path %q not allowed", p)
	}
	cleanBase, err := filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", err
	}
	// Normalize base symlinks so the Rel() comparison below stays consistent.
	// Without this, a base that is itself a symlink rejects every path
	// (EvalSymlinks(abs) resolves through the link while cleanBase does not).
	if rb, err := filepath.EvalSymlinks(cleanBase); err == nil {
		cleanBase = rb
	}
	abs, err := filepath.Abs(filepath.Join(cleanBase, p))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cleanBase, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes base", p)
	}
	// Always resolve symlinks on the full path — catches intermediate
	// directory symlinks (not just the final component).
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		rrel, err := filepath.Rel(cleanBase, resolved)
		if err != nil || rrel == ".." || strings.HasPrefix(rrel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q resolves outside base", p)
		}
		return resolved, nil
	}
	return abs, nil
}
