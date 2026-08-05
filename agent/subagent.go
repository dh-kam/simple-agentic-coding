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

// NewSubagentRunner builds the runner behind the `task` tool.
//
// opts carries the parent's approval gate and lifecycle hooks — see
// Agent.subagentOptions. A subagent that did not inherit them would be a hole
// straight through the user's permission prompt, since it gets the same tools.
//
// tools is a function, not a slice, for the same reason: callers unregister
// tools (config.disable_tools) *after* the task tool is built, and a captured
// slice would hand the subagent a tool the operator had switched off.
func NewSubagentRunner(backend Backend, model, system string, tools func() []Tool, opts ...Option) SubagentRunner {
	return func(ctx context.Context, prompt string) (string, error) {
		sub := New(backend, model, system, opts...)
		for _, t := range tools() {
			if t.Name == "task" {
				continue // one level of delegation only
			}
			sub.RegisterTool(t)
		}
		return sub.Run(ctx, prompt)
	}
}
