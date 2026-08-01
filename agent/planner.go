package agent

import (
	"context"
	"fmt"
)

// Planner produces an explicit plan before the tool loop.
type Planner interface {
	Plan(ctx context.Context, userInput string) (string, error)
}

const DefaultPlannerPrompt = `사용자의 요청을 해결하기 위한 실행 계획을 작성하라.
규칙:
- 도구를 호출하지 말고 텍스트로만 답한다.
- 번호가 매겨진 간결한 단계 목록만 출력한다.
- 각 단계는 구체적이고 실행 가능해야 한다 (무엇을 할지).
- 코드나 셸 명령은 적지 않는다.`

type LLMPlanner struct {
	backend   Backend
	model     string
	maxTokens int64
	prompt    string
}

func NewLLMPlanner(backend Backend, model string) *LLMPlanner {
	return &LLMPlanner{backend: backend, model: model, maxTokens: 1024, prompt: DefaultPlannerPrompt}
}
func (p *LLMPlanner) WithPrompt(s string) *LLMPlanner   { p.prompt = s; return p }
func (p *LLMPlanner) WithMaxTokens(n int64) *LLMPlanner { p.maxTokens = n; return p }

func (p *LLMPlanner) Plan(ctx context.Context, userInput string) (string, error) {
	resp, err := p.backend.Chat(ctx, ChatRequest{
		Model:     p.model,
		MaxTokens: p.maxTokens,
		System:    p.prompt,
		Messages:  []ChatMessage{{Role: "user", Content: userInput}},
		// No tools — planner only writes text
	}, nil)
	if err != nil {
		return "", fmt.Errorf("planner call: %w", err)
	}
	return resp.Content, nil
}
