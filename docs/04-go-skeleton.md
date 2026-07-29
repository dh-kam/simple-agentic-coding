# 4단계: Go 스켈레톤 코드

[01-architecture.md](./01-architecture.md)의 다이어그램이 그대로 코드가 된다.
구조체와 루프만 잡으면 나머지는 채우기다.

> 아래는 **개념 스케치**다. 동작하는 실제 구현은 프로젝트 루트의 Go 코드
> (`main.go`, `agent/`)를 본다. 그 구현은 공식 **Anthropic Go SDK**
> (`github.com/anthropics/anthropic-sdk-go`) 기반이며, base_url / api_key / model을
> 환경변수로 주입받는다 → [06-provider-config.md](./06-provider-config.md).
> (Go용 "Claude Agent SDK"는 없다. Go에서는 Messages API + tool use 루프를 직접 돌린다.)

---

## 전체 구조

```go
package agent

import (
	"context"
	"errors"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// messages[] 를 구성하는 단위. 역할에 따라 쓰는 필드가 다르다.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`       // 답변 텍스트
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`     // assistant가 도구를 호출할 때
	ToolCallID string     `json:"tool_call_id,omitempty"`   // role=tool 일 때 짝 ID
}

type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"arguments"` // JSON 문자열 그대로
}

// LLM에게 노출하는 도구 정의
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// Client 는 OpenAI / Anthropic 등 provider를 추상화한 인터페이스
type Client interface {
	Chat(ctx context.Context, system string, msgs []Message, tools []Tool) (*ChatResponse, error)
}

type ChatResponse struct {
	Message Message // role=assistant 인 메시지
}

type Agent struct {
	client       Client
	systemPrompt string
	tools        map[string]func(args string) (string, error) // name -> 실행 함수
	messages     []Message
}

func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	// ② 컨텍스트 초기화: 사용자 메시지 append
	a.messages = append(a.messages, Message{Role: RoleUser, Content: userInput})

	for iter := 0; iter < 25; iter++ { // 안전장치: 최대 반복 횟수
		// ④ LLM 호출
		resp, err := a.client.Chat(ctx, a.systemPrompt, a.messages, a.toolDefs())
		if err != nil {
			return "", err
		}

		// ⑤ 응답을 그대로 히스토리에 넣는다 (assistant 메시지)
		a.messages = append(a.messages, resp.Message)

		// ⑥ 종료 조건: tool_calls 가 없으면 최종 답변
		if len(resp.Message.ToolCalls) == 0 {
			return resp.Message.Content, nil
		}

		// ⑧⑨ 각 도구 호출 실행 → 결과를 role=tool 로 append
		for _, tc := range resp.Message.ToolCalls {
			result, err := a.tools[tc.Name](tc.Args)
			if err != nil {
				result = "ERROR: " + err.Error() // 실패해도 결과로 넣어 LLM이 회복하게 함
			}
			a.messages = append(a.messages, Message{
				Role:       RoleTool,
				Content:    result,
				ToolCallID: tc.ID, // ★ 짝 맞추기
			})
		}
	}
	return "", errors.New("max iterations reached")
}

// toolDefs 는 a.tools 를 LLM에 보낼 Tool 슬라이스로 변환한다 (구현 생략).
func (a *Agent) toolDefs() []Tool { /* ... */ return nil }
```

---

## 코드 ↔ 다이어그램 대응

| 코드 | 다이어그램 단계 |
|---|---|
| `a.messages = append(...)` (초기) | ② 컨텍스트 초기화 |
| `for iter := 0; iter < 25; iter++` | ③ 루프 반복 |
| `a.client.Chat(...)` | ④ LLM 호출 |
| `a.messages = append(a.messages, resp.Message)` | ⑤ 응답 파싱 |
| `if len(...ToolCalls) == 0 { return ... }` | ⑥⑦ 분기 + 종료 |
| `a.tools[tc.Name](tc.Args)` | ⑧ 도구 실행 |
| `append(..., Message{Role: RoleTool, ...})` | ⑨ 결과 append |

> `Run` 안의 `for` 루프가 곧 다이어그램의 ③~⑨다.
> 30줄 안에 에이전트의 본질이 다 들어간다.
