package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func NewGitTool() Tool {
	return Tool{
		Name:        "git",
		Description: "Git 명령을 실행한다 (status, diff, add, commit, log, branch 등).",
		InputSchema: map[string]any{
			"command": map[string]any{"type": "string", "description": "실행할 git 명령 (예: status, add ., commit -m \"msg\")"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Command string `json:"command"`
			}
			json.Unmarshal(args, &in)
			if in.Command == "" {
				return "", fmt.Errorf("command required")
			}
			// Safety: only allow read + add + commit + branch
			cmd := strings.TrimSpace(in.Command)
			parts := strings.Fields(cmd)
			if len(parts) == 0 || parts[0] != "git" {
				cmd = "git " + cmd
			}
			c := exec.CommandContext(ctx, "sh", "-c", cmd)
			// Check we are in a git repo
			if e := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree").Run(); e != nil {
				return "", fmt.Errorf("not a git repository")
			}
			out, err := c.CombinedOutput()
			if err != nil {
				return string(out) + "\n" + err.Error(), err
			}
			return string(out), nil
		},
	}
}

func NewGitCommitTool() Tool {
	return Tool{
		Name:        "git_commit",
		Description: "변경사항을 스테이지하고 커밋한다.",
		InputSchema: map[string]any{
			"message": map[string]any{"type": "string", "description": "커밋 메시지"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Message string `json:"message"`
			}
			json.Unmarshal(args, &in)
			if in.Message == "" {
				return "", fmt.Errorf("message required")
			}
			exec.CommandContext(ctx, "git", "add", "-A").Run()
			out, err := exec.CommandContext(ctx, "git", "commit", "-m", in.Message).CombinedOutput()
			if err != nil {
				return string(out), err
			}
			return "committed: " + in.Message, nil
		},
	}
}
