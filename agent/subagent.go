package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// SubagentRunner runs a self-contained sub-task in its own agent loop and
// returns the final text. Inject a stub in tests; NewSubagentRunner builds one
// that reuses the parent's client + tools (minus the task tool itself, to keep
// delegation one level deep — like Claude Code).
type SubagentRunner func(ctx context.Context, prompt string) (string, error)

// NewTaskTool returns the "task" tool: it delegates a sub-prompt to a
// SubagentRunner and returns that subagent's answer. Use it to fan out
// independent work (read several files, explore, verify) in a separate context.
func NewTaskTool(run SubagentRunner) Tool {
	return Tool{
		Name:        "task",
		Description: "독립적인 하위 작업을 전담하는 서브에이전트를 실행해 그 결과를 반환한다. 병렬/독립 작업에 사용. prompt: 서브에이전트에게 줄 지시.",
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

// NewSubagentRunner builds a SubagentRunner that spins up a fresh Agent with the
// given client/model/system and tools — but WITHOUT the "task" tool, so a
// subagent cannot spawn subagents (one level of delegation).
func NewSubagentRunner(client LLMClient, model, system string, tools []Tool) SubagentRunner {
	return func(ctx context.Context, prompt string) (string, error) {
		sub := New(client, model, system)
		for _, t := range tools {
			if t.Name == "task" {
				continue
			}
			sub.RegisterTool(t)
		}
		return sub.Run(ctx, prompt)
	}
}
