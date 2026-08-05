package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func NewReviewTool(base string) Tool {
	return Tool{
		Name:        "code_review",
		Description: "현재 git diff 또는 지정된 파일의 코드 리뷰 대상을 가져온다.",
		InputSchema: map[string]any{
			"target": map[string]any{"type": "string", "description": "리뷰 대상 (base 기준 상대 경로). 비우면 전체 diff."},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Target string `json:"target"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			// `git diff HEAD` covers the whole repository no matter what cmd.Dir
			// is, so when base is a subdirectory the diff would leak files the
			// rest of the tool surface is sandboxed away from. An explicit "."
			// pathspec confines it to base.
			target := strings.TrimSpace(in.Target)
			if target == "" {
				target = "."
			} else if _, err := safePath(base, target); err != nil {
				return "", err
			} else if strings.HasPrefix(target, ":") {
				// pathspec magic re-anchors at the repository root
				return "", fmt.Errorf("pathspec magic %q not allowed", target)
			}
			cctx, cancel := context.WithTimeout(ctx, commandTimeout())
			defer cancel()
			cmd := exec.CommandContext(cctx, "git", "diff", "HEAD", "--", target)
			cmd.Dir = base
			out, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(string(out)))
			}
			diff := strings.TrimSpace(string(out))
			if diff == "" {
				return "no changes to review", nil
			}
			return "## Code Review Target\n\n```diff\n" + diff + "\n```\n\n리뷰할 변경사항입니다. 버그, 스타일, 보안, 성능을 확인하세요.", nil
		},
	}
}
