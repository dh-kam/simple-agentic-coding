package agent

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient adapts the official Anthropic Go SDK to the LLMClient seam.
// Point it at any Anthropic Messages-API compatible endpoint via baseURL —
// first-party Anthropic (baseURL ""), or GLM Coding Plan
// (baseURL "https://open.bigmodel.cn/api/anthropic").
type AnthropicClient struct {
	c anthropic.Client
}

// NewAnthropicClient builds a client. An empty baseURL uses the SDK default
// (https://api.anthropic.com or the ANTHROPIC_BASE_URL env var).
func NewAnthropicClient(apiKey, baseURL string) *AnthropicClient {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &AnthropicClient{c: anthropic.NewClient(opts...)}
}

// StreamMessage drives a streaming Messages call, accumulates the full
// response (so the returned *Message carries StopReason + tool_use blocks),
// and forwards each text delta to onDelta when non-nil.
func (a *AnthropicClient) StreamMessage(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (*anthropic.Message, error) {
	stream := a.c.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	msg := anthropic.Message{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, fmt.Errorf("accumulate stream event: %w", err)
		}
		if onDelta != nil {
			// surface text deltas for live UX
			switch e := ev.AsAny().(type) {
			case anthropic.ContentBlockDeltaEvent:
				switch d := e.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					onDelta(d.Text)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return &msg, nil
}
