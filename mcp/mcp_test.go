package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dh-kam/simple-agentic-coding/agent"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newInMem wires a client to an in-process MCP server over an in-memory
// transport (no subprocess), returning the client session + server.
func newInMem(t *testing.T) (*mcpsdk.ClientSession, *mcpsdk.Server) {
	t.Helper()
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "t", Version: "v"}, nil)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "t", Version: "v"}, nil)
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, server
}

func TestWrapTool_callsServer(t *testing.T) {
	ctx := context.Background()
	cs, server := newInMem(t)

	server.AddTool(&mcpsdk.Tool{
		Name:        "greet",
		Description: "say hi",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
		},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "hello from mcp"}},
		}, nil
	})

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 1 {
		t.Fatalf("listed tools = %d, want 1", len(list.Tools))
	}

	tool := WrapTool(cs, "srv", list.Tools[0])
	if tool.Name != "srv__greet" {
		t.Errorf("tool name = %q, want srv__greet", tool.Name)
	}
	if _, ok := tool.InputSchema["name"]; !ok {
		t.Errorf("input schema properties not extracted: %v", tool.InputSchema)
	}

	out, err := tool.Run(ctx, json.RawMessage(`{"name":"a"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "hello from mcp" {
		t.Errorf("Run output = %q", out)
	}
}

func TestWrapTool_errorResult(t *testing.T) {
	ctx := context.Background()
	cs, server := newInMem(t)
	server.AddTool(&mcpsdk.Tool{Name: "boom", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "kaboom"}},
		}, nil
	})
	list, _ := cs.ListTools(ctx, nil)
	tool := WrapTool(cs, "srv", list.Tools[0])
	out, err := tool.Run(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for IsError result")
	}
	if !contains(out, "kaboom") {
		t.Errorf("error output lost: %q", out)
	}
}

func TestExtractText(t *testing.T) {
	res := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "a"},
			&mcpsdk.TextContent{Text: "b"},
		},
	}
	if got := extractText(res); got != "a\nb" {
		t.Errorf("extractText = %q", got)
	}
}

func TestPropertiesFrom(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string"}},
		"required":   []any{"x"},
	}
	p := propertiesFrom(schema)
	if _, ok := p["x"]; !ok {
		t.Errorf("properties missing x: %v", p)
	}
	// required is dropped (only properties returned)
	if _, ok := p["required"]; ok {
		t.Error("required should not be in properties map")
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"fs": {"command": "npx", "args": ["-y", "fs-server", "."]},
			"empty": {"command": ""}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg) != 1 {
		t.Fatalf("servers = %d, want 1 (empty command skipped)", len(cfg))
	}
	fs := cfg["fs"]
	if fs.Command != "npx" || len(fs.Args) != 3 || fs.Args[0] != "-y" {
		t.Errorf("fs config = %+v", fs)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSummarizeResources(t *testing.T) {
	if s := SummarizeResources(nil); s != "" {
		t.Error("nil refs should produce empty summary")
	}
	s := SummarizeResources([]ResourceRef{{Server: "fs", URI: "file:///x", Name: "x", Description: "the x file"}})
	if !contains(s, "file:///x") || !contains(s, "[fs]") || !contains(s, "read_resource") {
		t.Errorf("summary = %q", s)
	}
}

// TestConnectHTTP connects to an in-process Streamable HTTP MCP server and
// verifies list+call over the HTTP transport (no subprocess).
func TestConnectHTTP(t *testing.T) {
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "t", Version: "v"}, nil)
	mcpsdk.AddTool(server,
		&mcpsdk.Tool{Name: "ping", Description: "reply pong"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "pong"}}}, nil, nil
		},
	)
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	cl, tools, err := Connect(ctx, ServerConfig{Name: "h", URL: srv.URL})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cl.Close()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].Name != "h__ping" {
		t.Errorf("tool name = %q, want h__ping", tools[0].Name)
	}
	out, err := tools[0].Run(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "pong" {
		t.Errorf("Run output = %q, want pong", out)
	}
}

// TestConnectResourcesPrompts verifies Connect also surfaces MCP resources and
// prompts as read_resource / get_prompt tools over HTTP.
func TestConnectResourcesPrompts(t *testing.T) {
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "t", Version: "v"}, nil)
	server.AddResource(&mcpsdk.Resource{URI: "memo://notes", Name: "notes", Description: "notes"},
		func(_ context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: "memo://notes", Text: "hello notes"}}}, nil
		})
	server.AddPrompt(&mcpsdk.Prompt{Name: "greet", Description: "greeting"},
		func(_ context.Context, _ *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
			return &mcpsdk.GetPromptResult{Messages: []*mcpsdk.PromptMessage{{Role: "user", Content: &mcpsdk.TextContent{Text: "hi there"}}}}, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	cl, tools, err := Connect(ctx, ServerConfig{Name: "h", URL: srv.URL})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cl.Close()

	// resource catalog is collected for system-prompt auto-injection
	refs := cl.Resources()
	if len(refs) != 1 || refs[0].URI != "memo://notes" {
		t.Errorf("resources catalog = %+v", refs)
	}
	summary := SummarizeResources(refs)
	if !contains(summary, "memo://notes") || !contains(summary, "read_resource") {
		t.Errorf("summary missing catalog: %q", summary)
	}

	find := func(name string) agent.Tool {
		for _, t := range tools {
			if t.Name == name {
				return t
			}
		}
		return agent.Tool{}
	}

	rt := find("h__read_resource")
	if rt.Name == "" {
		t.Fatal("read_resource tool not registered")
	}
	out, err := rt.Run(ctx, json.RawMessage(`{"uri":"memo://notes"}`))
	if err != nil {
		t.Fatalf("read_resource: %v", err)
	}
	if out != "hello notes" {
		t.Errorf("read_resource output = %q, want hello notes", out)
	}

	pt := find("h__get_prompt")
	if pt.Name == "" {
		t.Fatal("get_prompt tool not registered")
	}
	out2, err := pt.Run(ctx, json.RawMessage(`{"name":"greet"}`))
	if err != nil {
		t.Fatalf("get_prompt: %v", err)
	}
	if out2 != "hi there" {
		t.Errorf("get_prompt output = %q, want hi there", out2)
	}
}
