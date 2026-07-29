package agent

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// Planner produces an explicit, step-by-step plan for a request before the
// tool loop runs (docs/05-design-details.md → 명시적 planning). The Agent
// injects the returned plan into the execution context. Return "" to skip.
type Planner interface {
	Plan(ctx context.Context, userInput string) (string, error)
}

// DefaultPlannerPrompt asks the model for a concise, numbered plan with no
// tool calls and no code — just "what to do, in order".
const DefaultPlannerPrompt = `사용자의 요청을 해결하기 위한 실행 계획을 작성하라.
규칙:
- 도구를 호출하지 말고 텍스트로만 답한다.
- 번호가 매겨진 간결한 단계 목록만 출력한다.
- 각 단계는 구체적이고 실행 가능해야 한다 (무엇을 할지).
- 코드나 셸 명령은 적지 않는다.`

// LLMPlanner implements Planner with a single no-tools LLM call. It shares the
// Agent's LLMClient + model so it works on any Anthropic-compatible endpoint
// (GLM included) — no structured-output beta required.
type LLMPlanner struct {
	client    LLMClient
	model     string
	maxTokens int64
	prompt    string
}

// NewLLMPlanner builds a planner using the given client and model.
func NewLLMPlanner(client LLMClient, model string) *LLMPlanner {
	return &LLMPlanner{
		client:    client,
		model:     model,
		maxTokens: 1024,
		prompt:    DefaultPlannerPrompt,
	}
}

// WithPrompt overrides the planning system prompt.
func (p *LLMPlanner) WithPrompt(s string) *LLMPlanner { p.prompt = s; return p }

// WithMaxTokens overrides the plan output cap.
func (p *LLMPlanner) WithMaxTokens(n int64) *LLMPlanner { p.maxTokens = n; return p }

func (p *LLMPlanner) Plan(ctx context.Context, userInput string) (string, error) {
	// No Tools field -> the model can only answer with text, so this is one call.
	resp, err := p.client.StreamMessage(ctx, anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: p.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: p.prompt}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userInput)),
		},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("planner call: %w", err)
	}
	return extractText(resp), nil
}
