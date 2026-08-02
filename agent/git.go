package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

var gitAllowed = map[string]bool{
	"status": true, "diff": true, "log": true, "branch": true,
	"add": true, "commit": true, "show": true, "stash": true, "remote": true,
	"tag": true, "describe": true, "rev-parse": true, "config": true,
}

func NewGitTool() Tool {
	return Tool{
		Name:        "git",
		Description: "Git 명령을 실행한다. 허용된 subcommand만 실행: status, diff, log, add, commit, branch 등.",
		InputSchema: map[string]any{
			"command": map[string]any{"type": "string", "description": "git subcommand (status, diff, log, add ., commit -m \"msg\")"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Command string `json:"command"`
			}
			json.Unmarshal(args, &in)
			if in.Command == "" {
				return "", fmt.Errorf("command required")
			}
			parts := strings.Fields(strings.TrimSpace(in.Command))
			if len(parts) == 0 {
				return "", fmt.Errorf("empty command")
			}
			if parts[0] == "git" {
				parts = parts[1:]
			}
			if len(parts) == 0 {
				return "", fmt.Errorf("no git subcommand")
			}
			subcmd := parts[0]
			if !gitAllowed[subcmd] {
				return "", fmt.Errorf("git subcommand %q not allowed", subcmd)
			}
			c := exec.CommandContext(ctx, "git", parts...)
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
		Description: "변경사항을 커밋한다. 자동으로 git add -A 후 커밋.",
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
