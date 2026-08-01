// Command agentic is a coding-assistant agent built on the Anthropic Messages
// API (works against Anthropic first-party or GLM Coding Plan).
//
// With no arguments it launches an interactive Claude-Code-style TUI REPL;
// with a prompt argument it runs once and exits (also used for record/replay).
// Configure via .env — see .env.example. MCP servers via AGENT_MCP_CONFIG.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dh-kam/simple-agentic-coding/agent"
	"github.com/dh-kam/simple-agentic-coding/config"
	"github.com/dh-kam/simple-agentic-coding/mcp"
	"github.com/dh-kam/simple-agentic-coding/tui"

	"github.com/joho/godotenv"
)

const systemPrompt = "너는 Go 코드베이스를 돕는 코딩 어시스턴트다. " +
	"필요한 도구를 호출해 단계적으로 작업하고, 마지막에 결과를 간결히 요약해 답한다."

// Build metadata, stamped via -ldflags at release time; "dev"/"none"/"unknown" otherwise.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("agentic %s (commit %s, %s)\n", version, commit, date)
		return
	}

	_ = godotenv.Load() // .env auto-load; already-set env vars win.

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY 가 필요합니다 (.env 또는 환경변수)")
	}
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	cfg := config.LoadEnv()
	model := os.Getenv("AGENT_MODEL")
	if cfg.Model != "" {
		model = cfg.Model
	}
	if model == "" {
		model = "claude-opus-5" // GLM 사용 시: glm-5.2
	}
	system := systemPrompt
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt
	}
	base, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	// Connect configured MCP servers (client-side, so it works on GLM too).
	mcpTools, mcpClients, mcpCatalog, mcpWarnings := loadMCP(context.Background())
	for _, w := range mcpWarnings {
		fmt.Fprintln(os.Stderr, "⚠ mcp:", w)
	}
	defer mcp.CloseAll(mcpClients)
	mcpSuffix := mcp.SummarizeResources(mcpCatalog) // auto-injected into the system prompt
	system += mcpSuffix                             // one-shot path; TUI gets it via tui.Run arg

	// No prompt arg → interactive TUI REPL.
	if len(os.Args) <= 1 {
		if err := tui.Run(agent.NewAnthropicClient(apiKey, baseURL), model, base, mcpTools, mcpSuffix); err != nil {
			log.Fatal(err)
		}
		return
	}

	// One-shot (also the record/replay path).
	var client agent.LLMClient = agent.NewAnthropicClient(apiKey, baseURL)
	if dir := os.Getenv("AGENT_RECORD_DIR"); dir != "" {
		rec, err := agent.NewRecorder(client, dir)
		if err != nil {
			log.Fatal(err)
		}
		client = rec
		fmt.Printf("🗄  recording responses to %s\n", dir)
	}

	maxCtx := 50000
	if cfg.MaxContextTokens > 0 {
		maxCtx = cfg.MaxContextTokens
	} else if v := os.Getenv("AGENT_MAX_CONTEXT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxCtx = n
		}
	}

	ag := agent.BuildCodingAssistant(client, model, system, base,
		agent.WithMaxTokens(128000),
		agent.WithMaxContextTokens(maxCtx),
		agent.WithPlanner(agent.NewLLMPlanner(client, model)),
		agent.WithOnPlan(func(p string) {
			fmt.Println("📝 실행 계획")
			fmt.Println(p)
			fmt.Println("━ 실행 ━")
		}),
		agent.WithOnText(func(s string) { fmt.Print(s) }),
	)
	for _, t := range mcpTools {
		ag.RegisterTool(t)
	}
	for _, name := range cfg.DisableTools {
		ag.UnregisterTool(name)
	}

	task := strings.Join(os.Args[1:], " ")
	if _, err := ag.Run(context.Background(), task); err != nil {
		log.Fatal(err)
	}
	fmt.Println() // newline after streamed answer
}

// loadMCP reads AGENT_MCP_CONFIG (Claude-Desktop-style JSON) and connects the
// listed MCP servers. Returns merged tools + clients + resource catalog.
func loadMCP(ctx context.Context) ([]agent.Tool, []*mcp.Client, []mcp.ResourceRef, []string) {
	path := os.Getenv("AGENT_MCP_CONFIG")
	if path == "" {
		return nil, nil, nil, nil
	}
	servers, err := mcp.LoadConfig(path)
	if err != nil {
		return nil, nil, nil, []string{err.Error()}
	}
	if len(servers) == 0 {
		return nil, nil, nil, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tools, clients, warnings := mcp.ConnectServers(cctx, servers)
	var catalog []mcp.ResourceRef
	for _, c := range clients {
		catalog = append(catalog, c.Resources()...)
	}
	return tools, clients, catalog, warnings
}
