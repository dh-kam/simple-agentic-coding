package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// SubagentRunner runs a sub-task in its own agent loop.
type SubagentRunner func(ctx context.Context, prompt string) (string, error)

func NewTaskTool(run SubagentRunner) Tool {
	return Tool{
		Name:        "task",
		Description: "독립적인 하위 작업을 전담하는 서브에이전트를 실행해 그 결과를 반환한다.",
		InputSchema: map[string]any{
			"prompt": map[string]any{"type": "string", "description": "서브에이전트가 수행할 작업 설명"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Prompt string `json:"prompt"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			return run(ctx, in.Prompt)
		},
	}
}

func NewSubagentRunner(backend Backend, model, system string, tools []Tool) SubagentRunner {
	return func(ctx context.Context, prompt string) (string, error) {
		sub := New(backend, model, system)
		for _, t := range tools {
			if t.Name == "task" {
				continue
			}
			sub.RegisterTool(t)
		}
		return sub.Run(ctx, prompt)
	}
}
