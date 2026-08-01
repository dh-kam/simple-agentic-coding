package agent

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient wraps the SDK for AnthropicBackend.
type AnthropicClient struct {
	c anthropic.Client
}

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
			return nil, fmt.Errorf("accumulate: %w", err)
		}
		if onDelta != nil {
			if e, ok := ev.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
				if d, ok := e.Delta.AsAny().(anthropic.TextDelta); ok {
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
