// Command mcp-echo is a minimal stdio MCP server with one tool, "echo", used to
// verify the agent's MCP client integration end-to-end. Not part of the agent
// binary; build separately: go build -o /tmp/mcp-echo ./examples/mcp-echo
package main

import (
	"context"
	"os"
	"os/signal"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Text string `json:"text"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "mcp-echo", Version: "0.1.0"}, nil)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{Name: "echo", Description: "Echo back the given text."},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, a echoArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + a.Text}},
			}, nil, nil
		},
	)

	if err := s.Run(ctx, &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}
