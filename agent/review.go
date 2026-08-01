package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func NewReviewTool() Tool {
	return Tool{
		Name:        "code_review",
		Description: "현재 git diff 또는 지정된 파일의 코드 리뷰를 수행한다.",
		InputSchema: map[string]any{
			"target": map[string]any{"type": "string", "description": "리뷰 대상 (git diff 경로 또는 파일명). 비우면 전체 diff."},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Target string `json:"target"`
			}
			json.Unmarshal(args, &in)
			cmd := exec.CommandContext(ctx, "git", "diff", "HEAD")
			if in.Target != "" {
				cmd = exec.CommandContext(ctx, "git", "diff", "HEAD", "--", in.Target)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("git diff: %w", err)
			}
			diff := strings.TrimSpace(string(out))
			if diff == "" {
				return "no changes to review", nil
			}
			return "## Code Review Target\n\n```diff\n" + diff + "\n```\n\n리뷰할 변경사항입니다. 버그, 스타일, 보안, 성능을 확인하세요.", nil
		},
	}
}
