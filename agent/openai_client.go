package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// OpenAIClient implements LLMClient via the OpenAI Chat Completions API.
// It translates Anthropic params → OpenAI format (via JSON round-trip to
// avoid SDK union type complexity), calls the endpoint, and reconstructs
// *anthropic.Message from the streaming response.
type OpenAIClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func NewOpenAIClient(apiKey, baseURL string) *OpenAIClient {
	return &OpenAIClient{apiKey: apiKey, baseURL: baseURL, http: http.DefaultClient}
}

func (c *OpenAIClient) StreamMessage(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (*anthropic.Message, error) {
	// 1. Marshal Anthropic params to JSON (wire format) → parse into simple maps
	rawParams, _ := json.Marshal(params)
	var p struct {
		Model     string               `json:"model"`
		MaxTokens int64                `json:"max_tokens"`
		System    json.RawMessage      `json:"system"`
		Messages  []map[string]any     `json:"messages"`
		Tools     json.RawMessage      `json:"tools"`
	}
	json.Unmarshal(rawParams, &p)

	// 2. Build OpenAI request
	oaiMsgs := buildOpenAIMessages(p.System, p.Messages)
	reqBody := map[string]any{
		"model":          p.Model,
		"max_tokens":     p.MaxTokens,
		"messages":       oaiMsgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	// tools
	var tools []map[string]any
	json.Unmarshal(p.Tools, &tools)
	for _, t := range tools {
		if fn, ok := t["function"].(map[string]any); ok {
			// already OpenAI format from Anthropic SDK (name/description/input_schema → convert)
			if schema, ok := fn["input_schema"]; ok {
				fn["parameters"] = schema
				delete(fn, "input_schema")
			}
		}
	}
	if len(tools) > 0 {
		// Anthropic tool format: {name, description, input_schema}
		// OpenAI format: {type:"function", function:{name, description, parameters}}
		var oaiTools []map[string]any
		for _, t := range tools {
			name, _ := t["name"].(string)
			desc, _ := t["description"].(string)
			schema := t["input_schema"]
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			oaiTools = append(oaiTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": desc,
					"parameters":  schema,
				},
			})
		}
		reqBody["tools"] = oaiTools
	}

	jsonBody, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
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

// buildOpenAIMessages converts Anthropic system + messages to OpenAI format.
func buildOpenAIMessages(systemRaw json.RawMessage, anthropicMsgs []map[string]any) []map[string]any {
	var msgs []map[string]any

	// System prompt
	if len(systemRaw) > 0 {
		var sysBlocks []map[string]any
		if err := json.Unmarshal(systemRaw, &sysBlocks); err == nil {
			for _, b := range sysBlocks {
				if t, ok := b["text"].(string); ok && t != "" {
					msgs = append(msgs, map[string]any{"role": "system", "content": t})
				}
			}
		}
	}

	// Conversation messages
	for _, m := range anthropicMsgs {
		role, _ := m["role"].(string)
		content := m["content"]

		switch c := content.(type) {
		case string:
			msgs = append(msgs, map[string]any{"role": role, "content": c})
		case []any:
			var textParts []string
			var toolCalls []map[string]any
			for _, block := range c {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				switch b["type"] {
				case "text":
					if t, ok := b["text"].(string); ok {
						textParts = append(textParts, t)
					}
				case "tool_use":
					args, _ := json.Marshal(b["input"])
					toolCalls = append(toolCalls, map[string]any{
						"id":   b["id"],
						"type": "function",
						"function": map[string]any{
							"name":      b["name"],
							"arguments": string(args),
						},
					})
				case "tool_result":
					// Extract text from tool_result content
					resultText := extractResultText(b["content"])
					msgs = append(msgs, map[string]any{
						"role":         "tool",
						"tool_call_id": b["tool_use_id"],
						"content":      resultText,
					})
				}
			}
			// Build the message for this role
			if role == "assistant" {
				msg := map[string]any{"role": "assistant"}
				if len(textParts) > 0 {
					msg["content"] = strings.Join(textParts, "\n")
				}
				if len(toolCalls) > 0 {
					msg["tool_calls"] = toolCalls
				}
				msgs = append(msgs, msg)
			} else if len(textParts) > 0 {
				msgs = append(msgs, map[string]any{"role": role, "content": strings.Join(textParts, "\n")})
			}
		}
	}

	return msgs
}

func extractResultText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if bm, ok := b.(map[string]any); ok {
				if t, ok := bm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// parseOpenAIStream reads SSE chunks, accumulates content + tool_calls, and
// reconstructs an *anthropic.Message.
func parseOpenAIStream(body io.Reader, onDelta func(string)) (*anthropic.Message, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	var textContent strings.Builder
	var toolCalls []toolCallAccum
	var finishReason string

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
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				textContent.WriteString(choice.Delta.Content)
				if onDelta != nil {
					onDelta(choice.Delta.Content)
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				for len(toolCalls) <= tc.Index {
					toolCalls = append(toolCalls, toolCallAccum{})
				}
				acc := &toolCalls[tc.Index]
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				acc.args += tc.Function.Arguments
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai: read stream: %w", err)
	}

	return buildAnthropicMessage(textContent.String(), toolCalls, finishReason)
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
}

type toolCallAccum struct {
	id   string
	name string
	args string
}

func buildAnthropicMessage(text string, toolCalls []toolCallAccum, finishReason string) (*anthropic.Message, error) {
	stopReason := "end_turn"
	if finishReason == "tool_calls" {
		stopReason = "tool_use"
	}

	var contentBlocks []map[string]any
	if text != "" {
		contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": text})
	}
	for _, tc := range toolCalls {
		var input any = map[string]any{}
		if tc.args != "" {
			json.Unmarshal([]byte(tc.args), &input)
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  "tool_use",
			"id":    tc.id,
			"name":  tc.name,
			"input": input,
		})
	}

	msgJSON, _ := json.Marshal(map[string]any{
		"id":          "msg_openai",
		"type":        "message",
		"role":        "assistant",
		"content":     contentBlocks,
		"stop_reason": stopReason,
	})
	var msg anthropic.Message
	if err := json.Unmarshal(msgJSON, &msg); err != nil {
		return nil, fmt.Errorf("openai: reconstruct message: %w", err)
	}
	return &msg, nil
}
