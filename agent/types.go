package agent

import (
	"context"
	"encoding/json"
)

// ChatMessage is a provider-agnostic conversation message.
type ChatMessage struct {
	Role       string         `json:"role"`                   // user, assistant, system, tool
	Content    string         `json:"content,omitempty"`      // text content
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`   // when role=assistant
	ToolCallID string         `json:"tool_call_id,omitempty"` // when role=tool
	IsError    bool           `json:"is_error,omitempty"`     // tool result error flag
}

// ChatToolCall is a tool invocation from the assistant.
type ChatToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolDef is a provider-agnostic tool definition sent to the LLM.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ChatRequest is a provider-agnostic LLM request.
type ChatRequest struct {
	Model     string
	MaxTokens int64
	System    string
	Messages  []ChatMessage
	Tools     []ToolDef
}

// ChatResponse is a provider-agnostic LLM response.
type ChatResponse struct {
	Content    string         // text (may be empty if only tool calls)
	ToolCalls  []ChatToolCall // tool invocations (empty if end_turn)
	StopReason string         // "end_turn" | "tool_use"
}

// IsToolUse returns true if the response requests tool execution.
func (r *ChatResponse) IsToolUse() bool { return r.StopReason == "tool_use" }

// ToAssistantMessage converts the response into a ChatMessage for history.
func (r *ChatResponse) ToAssistantMessage() ChatMessage {
	return ChatMessage{
		Role:      "assistant",
		Content:   r.Content,
		ToolCalls: r.ToolCalls,
	}
}

// Backend is the provider-agnostic LLM interface. Each backend (Anthropic,
// OpenAI, etc.) implements this; the agent loop works only with these types.
type Backend interface {
	// Chat sends a request and returns the response. onDelta (if non-nil)
	// receives text chunks as they stream in.
	Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error)
}
