package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// OpenAIBackend implements Backend via the OpenAI Chat Completions API.
// Works with any OpenAI-compatible endpoint (GLM, OpenAI, local, etc.).
type OpenAIBackend struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewOpenAIBackend builds an OpenAI-compatible backend. base is the transport
// to send through — pass the HAR capture transport to record traffic, or nil
// for the default. Callers must not hand in an *http.Client with a Timeout:
// streamed answers legitimately outlive any fixed deadline, so the guards here
// are on connect and on time-to-first-byte instead.
func NewOpenAIBackend(apiKey, baseURL string, base http.RoundTripper) *OpenAIBackend {
	if base == nil {
		base = NewHTTPTransport()
	}
	return &OpenAIBackend{
		apiKey:  apiKey,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http: &http.Client{
			Transport: base,
		},
	}
}

func (b *OpenAIBackend) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	oaiReq := chatReqToOpenAI(req)
	jsonBody, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", b.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return parseOpenAIStream(resp.Body, onDelta)
}

func chatReqToOpenAI(req ChatRequest) map[string]any {
	var msgs []map[string]any
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, chatMsgToOpenAI(m))
	}

	result := map[string]any{
		"model":      req.Model,
		"max_tokens": openAIMaxTokens(req.MaxTokens), // GLM OpenAI endpoint caps lower than Anthropic
		"messages":   msgs,
		"stream":     true,
	}
	// Streaming responses omit usage unless asked. Not every OpenAI-compatible
	// server accepts the field, so allow turning it back off.
	if os.Getenv("AGENT_OPENAI_NO_STREAM_OPTIONS") != "1" {
		result["stream_options"] = map[string]any{"include_usage": true}
	}

	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			params := t.InputSchema
			if params == nil || len(params) == 0 {
				params = map[string]any{}
			}
			// Tool.InputSchema is just the "properties" map — wrap it in a
			// proper JSON Schema object for the OpenAI API (Anthropic auto-adds
			// type:object but OpenAI requires it explicitly).
			if _, hasType := params["type"]; !hasType {
				params = map[string]any{
					"type":       "object",
					"properties": params,
				}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  params,
				},
			})
		}
		result["tools"] = tools
	}
	return result
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func chatMsgToOpenAI(m ChatMessage) map[string]any {
	if m.ToolCallID != "" {
		return map[string]any{"role": "tool", "tool_call_id": m.ToolCallID, "content": m.Content}
	}
	if len(m.ToolCalls) > 0 {
		msg := map[string]any{"role": "assistant"}
		if m.Content != "" {
			msg["content"] = m.Content
		}
		var tcs []map[string]any
		for _, tc := range m.ToolCalls {
			tcs = append(tcs, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": string(tc.Arguments),
				},
			})
		}
		msg["tool_calls"] = tcs
		return msg
	}
	return map[string]any{"role": m.Role, "content": m.Content}
}

// parseOpenAIStream reads SSE chunks and returns a ChatResponse.
func parseOpenAIStream(body io.Reader, onDelta func(string)) (*ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	var text strings.Builder
	var toolCalls []toolCallAccum
	var finishReason string
	var usage Usage

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk openAIChunk
		if e := json.Unmarshal([]byte(data), &chunk); e != nil {
			continue
		}
		// The usage chunk arrives last and carries no choices.
		if chunk.Usage != nil {
			usage = Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				if onDelta != nil {
					onDelta(c.Delta.Content)
				}
			}
			for _, tc := range c.Delta.ToolCalls {
				for len(toolCalls) <= tc.Index {
					toolCalls = append(toolCalls, toolCallAccum{})
				}
				a := &toolCalls[tc.Index]
				if tc.ID != "" {
					a.id = tc.ID
				}
				if tc.Function.Name != "" {
					a.name = tc.Function.Name
				}
				a.args += tc.Function.Arguments
			}
			if c.FinishReason != "" {
				finishReason = c.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai: read stream: %w", err)
	}

	resp := &ChatResponse{
		Content:    text.String(),
		StopReason: "end_turn",
		Usage:      usage,
	}
	switch finishReason {
	case "tool_calls":
		resp.StopReason = "tool_use"
	}
	for _, tc := range toolCalls {
		args := json.RawMessage(tc.args)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		resp.ToolCalls = append(resp.ToolCalls, ChatToolCall{
			ID: tc.id, Name: tc.name, Arguments: args,
		})
	}
	return resp, nil
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

type toolCallAccum struct {
	id   string
	name string
	args string
}

func openAIMaxTokens(requested int64) int64 {
	limit := int64(8192)
	if v := os.Getenv("AGENT_OPENAI_MAX_TOKENS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			limit = n
		}
	}
	return min64(requested, limit)
}
