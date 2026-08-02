// Package mcp connects to external MCP (Model Context Protocol) servers and
// exposes their tools to the agent as ordinary agent.Tool values.
//
// Why client-side: Anthropic's mcp_servers/mcp_toolset is server-side and
// GLM doesn't support it. So we connect to MCP servers ourselves, fetch their
// tools, and proxy calls — keeping the agent provider-agnostic.
//
// Uses the official Go SDK (github.com/modelcontextprotocol/go-sdk/mcp).
// Currently supports stdio servers (the common case for local tools).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dh-kam/simple-agentic-coding/agent"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerConfig describes one MCP server to connect to. A server uses either a
// stdio command (Command) or an HTTP/SSE endpoint (URL).
type ServerConfig struct {
	Name      string   // identifier; used to namespace tools as "<name>__<tool>"
	Command   string   // stdio: executable to run
	Args      []string // stdio: command arguments
	Env       []string // stdio: optional extra env (KEY=VAL); appended to os.Environ
	URL       string   // http/sse: server endpoint
	Transport string   // "http" (default for URL) | "sse" | "stdio" (default for Command)
}

// ResourceRef is one MCP resource advertised by a server (for auto-injection).
type ResourceRef struct {
	Server, URI, Name, Description string
}

// Client is a live connection to one MCP server.
type Client struct {
	session   *mcpsdk.ClientSession
	resources []ResourceRef
}

// Resources returns the resources this server advertised at connect time.
func (c *Client) Resources() []ResourceRef {
	if c == nil {
		return nil
	}
	return c.resources
}

// Close ends the server session.
func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.Close()
}

// SummarizeResources builds a system-prompt section listing available MCP
// resources, so the model knows what context it can pull via read_resource.
// Returns "" when there are none.
func SummarizeResources(refs []ResourceRef) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## 사용 가능한 MCP 리소스 (<서버>__read_resource 도구로 본문을 읽을 수 있다)")
	for _, r := range refs {
		line := "- [" + r.Server + "] " + r.URI
		switch {
		case r.Description != "":
			line += " — " + truncate(r.Description, 64)
		case r.Name != "":
			line += " — " + r.Name
		}
		b.WriteString("\n" + line)
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// transportFor picks the SDK transport for a server config: Streamable HTTP
// (default) or SSE for URL servers, CommandTransport for stdio servers.
func transportFor(cfg ServerConfig) (mcpsdk.Transport, error) {
	switch {
	case cfg.URL != "":
		if cfg.Transport == "sse" {
			return &mcpsdk.SSEClientTransport{Endpoint: cfg.URL}, nil
		}
		return &mcpsdk.StreamableClientTransport{Endpoint: cfg.URL}, nil // default: streamable HTTP (2025-03-26)
	case cfg.Command != "":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		// Scrub credential-bearing vars from inherited environment to prevent
		// MCP servers (often third-party npx/uvx packages) from seeing API keys.
		cmd.Env = scrubCreds(os.Environ(), cfg.Env)
		return &mcpsdk.CommandTransport{Command: cmd}, nil
	}
	return nil, fmt.Errorf("server %q needs command or url", cfg.Name)
}

// scrubCreds filters credential-bearing env vars from the inherited environment
// before passing to MCP child processes. Only vars explicitly listed in cfg.Env
// (from the MCP config) are forwarded in addition to non-credential vars.

var credPrefixes = []string{
	"ANTHROPIC", "OPENAI", "API_KEY", "SECRET", "TOKEN", "PASSWORD", "CREDENTIAL",
	"AWS_SECRET", "GITHUB_TOKEN", "DATABASE_URL", "PRIVATE_KEY",
}

func scrubCreds(inherit, extra []string) []string {
	result := []string{}
	for _, kv := range inherit {
		key := strings.SplitN(kv, "=", 2)[0]
		if isCredKey(key) {
			continue
		}
		result = append(result, kv)
	}
	return append(result, extra...)
}

func isCredKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, p := range credPrefixes {
		if strings.Contains(upper, p) {
			return true
		}
	}
	return false
}

// Connect connects to the server (stdio command or HTTP/SSE endpoint),
// completes the initialize handshake, lists its tools, and returns them wrapped
// as agent.Tool values plus a Client to keep the connection alive.
func Connect(ctx context.Context, cfg ServerConfig) (*Client, []agent.Tool, error) {
	transport, err := transportFor(cfg)
	if err != nil {
		return nil, nil, err
	}
	c := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agentic", Version: "0.1.0"}, nil)

	session, err := c.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: connect %q: %w", cfg.Name, err)
	}
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("mcp: list tools %q: %w", cfg.Name, err)
	}

	cl := &Client{session: session}
	tools := make([]agent.Tool, 0, len(list.Tools))
	for _, t := range list.Tools {
		tools = append(tools, WrapTool(session, cfg.Name, t))
	}
	// resources/prompts (best-effort: only if the server supports them)
	extra, refs := resourcePromptTools(ctx, session, cfg.Name)
	tools = append(tools, extra...)
	cl.resources = refs
	return cl, tools, nil
}

// resourcePromptTools lists an MCP server's resources and prompts (best-effort)
// and wraps read_resource / get_prompt as agent tools. A tool is added only when
// the server supports that capability (the list call didn't error).
func resourcePromptTools(ctx context.Context, session *mcpsdk.ClientSession, server string) ([]agent.Tool, []ResourceRef) {
	var tools []agent.Tool
	var refs []ResourceRef
	if res, err := session.ListResources(ctx, nil); err == nil {
		uris := make([]string, 0, len(res.Resources))
		for _, r := range res.Resources {
			if r.URI == "" {
				continue
			}
			refs = append(refs, ResourceRef{Server: server, URI: r.URI, Name: r.Name, Description: r.Description})
			if r.Name != "" {
				uris = append(uris, r.URI+" ("+r.Name+")")
			} else {
				uris = append(uris, r.URI)
			}
		}
		if len(uris) > 0 {
			tools = append(tools, makeReadResourceTool(session, server, uris))
		}
	}
	if pr, err := session.ListPrompts(ctx, nil); err == nil {
		names := make([]string, 0, len(pr.Prompts))
		for _, p := range pr.Prompts {
			if p.Name != "" {
				names = append(names, p.Name)
			}
		}
		if len(names) > 0 {
			tools = append(tools, makeGetPromptTool(session, server, names))
		}
	}
	return tools, refs
}

func makeReadResourceTool(session *mcpsdk.ClientSession, server string, uris []string) agent.Tool {
	desc := "[mcp:" + server + "] MCP 리소스를 URI로 읽어 텍스트를 반환한다."
	if len(uris) > 0 {
		desc += " 사용 가능: " + strings.Join(uris, ", ")
	}
	return agent.Tool{
		Name:        server + "__read_resource",
		Description: desc,
		InputSchema: map[string]any{
			"uri": map[string]any{"type": "string", "description": "읽을 리소스 URI"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(args, &in)
			if in.URI == "" {
				return "", fmt.Errorf("uri is required")
			}
			res, err := session.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: in.URI})
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, c := range res.Contents {
				b.WriteString(c.Text)
				b.WriteByte('\n')
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func makeGetPromptTool(session *mcpsdk.ClientSession, server string, names []string) agent.Tool {
	desc := "[mcp:" + server + "] MCP 프롬프트 템플릿을 렌더해 텍스트를 반환한다."
	if len(names) > 0 {
		desc += " 사용 가능: " + strings.Join(names, ", ")
	}
	return agent.Tool{
		Name:        server + "__get_prompt",
		Description: desc,
		InputSchema: map[string]any{
			"name":      map[string]any{"type": "string", "description": "프롬프트 이름"},
			"arguments": map[string]any{"type": "object", "description": "템플릿 인자(선택)"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			_ = json.Unmarshal(args, &in)
			if in.Name == "" {
				return "", fmt.Errorf("name is required")
			}
			res, err := session.GetPrompt(ctx, &mcpsdk.GetPromptParams{Name: in.Name, Arguments: in.Arguments})
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, m := range res.Messages {
				if tc, ok := m.Content.(*mcpsdk.TextContent); ok && tc != nil {
					b.WriteString(tc.Text)
					b.WriteByte('\n')
				}
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

// ConnectServers connects to every configured server. Per-server failures
// become warnings (the server is skipped); successful tools are merged.
func ConnectServers(ctx context.Context, servers map[string]ServerConfig) (tools []agent.Tool, clients []*Client, warnings []string) {
	for name, s := range servers {
		s.Name = name
		cl, ts, err := Connect(ctx, s)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		clients = append(clients, cl)
		tools = append(tools, ts...)
	}
	return tools, clients, warnings
}

// CloseAll closes a slice of clients (e.g. via defer).
func CloseAll(clients []*Client) {
	for _, c := range clients {
		_ = c.Close()
	}
}

// WrapTool turns an MCP server tool into an agent.Tool. The tool is namespaced
// "<server>__<tool>" to avoid collisions across servers; calls are proxied to
// the server via session.CallTool (using the original tool name).
func WrapTool(session *mcpsdk.ClientSession, server string, t *mcpsdk.Tool) agent.Tool {
	full := server + "__" + t.Name
	desc := strings.TrimSpace(t.Description)
	return agent.Tool{
		Name:        full,
		Description: fmt.Sprintf("[mcp:%s] %s", server, desc),
		InputSchema: propertiesFrom(t.InputSchema),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			m := map[string]any{}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &m) // best-effort; empty on bad JSON
			}
			res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.Name, Arguments: m})
			if err != nil {
				return "", err
			}
			text := extractText(res)
			if res.IsError {
				return text, fmt.Errorf("mcp tool %q reported error: %s", t.Name, firstLine(text))
			}
			return text, nil
		},
	}
}

// extractText joins all TextContent blocks of a tool result.
func extractText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok && tc != nil {
			b.WriteString(tc.Text)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

// propertiesFrom pulls the JSON-Schema "properties" object out of an MCP tool's
// inputSchema (which is the full schema: {type, properties, required, ...}).
// Falls back to the whole schema if "properties" is absent.
func propertiesFrom(schema any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{}
	}
	var full map[string]any
	if err := json.Unmarshal(b, &full); err != nil {
		return map[string]any{}
	}
	if p, ok := full["properties"].(map[string]any); ok {
		return p
	}
	return full
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// LoadConfig reads a Claude-Desktop-style config file:
//
//	{ "mcpServers": { "fs": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."] } } }
func LoadConfig(path string) (map[string]ServerConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Servers map[string]struct {
			Command   string   `json:"command"`
			Args      []string `json:"args"`
			Env       []string `json:"env"`
			URL       string   `json:"url"`
			Transport string   `json:"transport"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("mcp: parse config: %w", err)
	}
	out := make(map[string]ServerConfig, len(raw.Servers))
	for name, s := range raw.Servers {
		if s.Command == "" && s.URL == "" {
			continue
		}
		out[name] = ServerConfig{
			Name:      name,
			Command:   s.Command,
			Args:      s.Args,
			Env:       s.Env,
			URL:       s.URL,
			Transport: s.Transport,
		}
	}
	return out, nil
}
