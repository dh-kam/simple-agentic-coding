package agent

import (
	"context"
	"encoding/json"

	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicBackend implements Backend via the Anthropic Messages API.
type AnthropicBackend struct {
	client *AnthropicClient
}

func NewAnthropicBackend(apiKey, baseURL string, extra ...option.RequestOption) *AnthropicBackend {
	return &AnthropicBackend{client: NewAnthropicClient(apiKey, baseURL, extra...)}
}

func (b *AnthropicBackend) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	params, err := chatReqToAnthropic(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build params: %w", err)
	}
	msg, err := b.client.StreamMessage(ctx, params, onDelta)
	if err != nil {
		return nil, err
	}
	return anthropicMsgToResponse(msg), nil
}

func chatReqToAnthropic(req ChatRequest) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    []anthropic.TextBlockParam{{Text: req.System}},
	}
	for _, m := range req.Messages {
		mp, err := chatMsgToAnthropic(m)
		if err != nil {
			return params, err
		}
		params.Messages = append(params.Messages, mp)
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{Properties: t.InputSchema},
			},
		})
	}
	return params, nil
}

func chatMsgToAnthropic(m ChatMessage) (anthropic.MessageParam, error) {
	var raw map[string]any
	switch {
	case m.ToolCallID != "":
		raw = map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
				"is_error":    m.IsError,
			}},
		}
	case len(m.ToolCalls) > 0:
		var blocks []map[string]any
		if m.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
		}
		for _, tc := range m.ToolCalls {
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Name,
				"input": json.RawMessage(tc.Arguments),
			})
		}
		raw = map[string]any{"role": "assistant", "content": blocks}
	default:
		role := m.Role
		if role == "" {
			role = "user"
		}
		raw = map[string]any{"role": role, "content": m.Content}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return anthropic.MessageParam{}, fmt.Errorf("marshal: %w", err)
	}
	var mp anthropic.MessageParam
	if e := json.Unmarshal(data, &mp); e != nil {
		return anthropic.MessageParam{}, fmt.Errorf("unmarshal: %w", e)
	}
	return mp, nil
}

func anthropicMsgToResponse(msg *anthropic.Message) *ChatResponse {
	resp := &ChatResponse{
		StopReason: string(msg.StopReason),
		// Cache reads/writes are billed as input, so fold them in — otherwise
		// a prompt-cached session reports far fewer input tokens than it used.
		Usage: Usage{
			InputTokens:  msg.Usage.InputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.CacheReadInputTokens,
			OutputTokens: msg.Usage.OutputTokens,
		},
	}
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			resp.Content += b.Text
		case anthropic.ToolUseBlock:
			args, _ := json.Marshal(b.Input)
			resp.ToolCalls = append(resp.ToolCalls, ChatToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: args,
			})
		}
	}
	return resp
}
