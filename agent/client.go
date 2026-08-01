package agent

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient adapts the official Anthropic Go SDK to the LLMClient seam.
type AnthropicClient struct {
	c anthropic.Client
}

// NewAnthropicClient builds a client. extra opts are appended after apiKey/baseURL
// (use option.WithHTTPClient for HAR capture, proxies, etc.).
func NewAnthropicClient(apiKey, baseURL string, extra ...option.RequestOption) *AnthropicClient {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	opts = append(opts, extra...)
	return &AnthropicClient{c: anthropic.NewClient(opts...)}
}

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
