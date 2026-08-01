// Command agentic is a coding-assistant agent built on the Anthropic Messages
// API (works against Anthropic first-party or GLM Coding Plan).
//
// Usage:
//
//	agentic                    → interactive TUI REPL
//	agentic ask "prompt"       → one-shot: run the agent loop, print result with glamour + diffs
//	agentic "prompt"           → same as ask (backward compat)
//	agentic --version          → version info
//
// Configure via .env — see .env.example. MCP servers via AGENT_MCP_CONFIG.
package main

import (
	"context"
	"encoding/json"
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

	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/glamour"
	"github.com/joho/godotenv"
)

const systemPrompt = "너는 Go 코드베이스를 돕는 코딩 어시스턴트다. " +
	"필요한 도구를 호출해 단계적으로 작업하고, 마지막에 결과를 간결히 요약해 답한다."

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// ANSI color codes for CLI output (works in any terminal, no lipgloss needed).
const (
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cGreen  = "\033[32m"
	cRed    = "\033[31m"
	cCyan   = "\033[36m"
	cYellow = "\033[33m"
	cReset  = "\033[0m"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("agentic %s (commit %s, %s)\n", version, commit, date)
		return
	}

	_ = godotenv.Load()

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
		model = "claude-opus-5"
	}
	system := systemPrompt
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt
	}
	base, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	mcpTools, mcpClients, mcpCatalog, mcpWarnings := loadMCP(context.Background())
	for _, w := range mcpWarnings {
		fmt.Fprintln(os.Stderr, cYellow+"⚠ mcp:"+cReset, w)
	}
	defer mcp.CloseAll(mcpClients)
	mcpSuffix := mcp.SummarizeResources(mcpCatalog)
	system += mcpSuffix

	// Load skills from AGENT_SKILLS_DIR (default .agentic/skills).
	skillDir := os.Getenv("AGENT_SKILLS_DIR")
	if skillDir == "" {
		skillDir = ".agentic/skills"
	}
	skills, _ := agent.LoadSkills(skillDir)
	skillSummary := agent.SkillSummary(skills)
	system += skillSummary

	// Build extra tools: MCP tools + skill loader.
	extraTools := mcpTools
	if len(skills) > 0 {
		extraTools = append([]agent.Tool{}, mcpTools...)
		extraTools = append(extraTools, agent.NewLoadSkillTool(skills))
	}

	// Determine the prompt. Support: "ask" subcommand, "-p" flag, or bare prompt.
	promptArgs := os.Args[1:]
	if len(promptArgs) == 0 {
		// No args → interactive TUI REPL.
		if err := tui.Run(agent.NewAnthropicClient(apiKey, baseURL), model, base, extraTools, mcpSuffix+skillSummary); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Strip "ask" or "-p" prefix.
	if promptArgs[0] == "ask" || promptArgs[0] == "-p" {
		promptArgs = promptArgs[1:]
	}
	if len(promptArgs) == 0 {
		log.Fatal("프롬프트를 입력하세요: agentic ask \"your prompt\"")
	}
	task := strings.Join(promptArgs, " ")

	// --- One-shot / ask mode ---
	var client agent.LLMClient = agent.NewAnthropicClient(apiKey, baseURL)
	if dir := os.Getenv("AGENT_RECORD_DIR"); dir != "" {
		rec, err := agent.NewRecorder(client, dir)
		if err != nil {
			log.Fatal(err)
		}
		client = rec
		fmt.Fprintf(os.Stderr, cDim+"🗄  recording to %s"+cReset+"\n", dir)
	}

	maxCtx := 50000
	if cfg.MaxContextTokens > 0 {
		maxCtx = cfg.MaxContextTokens
	} else if v := os.Getenv("AGENT_MAX_CONTEXT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxCtx = n
		}
	}

	// Accumulate the answer for glamour rendering at the end.
	var answer strings.Builder

	ag := agent.BuildCodingAssistant(client, model, system, base,
		agent.WithMaxTokens(128000),
		agent.WithMaxContextTokens(maxCtx),
		agent.WithPlanner(agent.NewLLMPlanner(client, model)),
		agent.WithOnPlan(func(p string) {
			fmt.Printf(cDim+"📝 %s"+cReset+"\n\n", truncStr(p, 200))
		}),
		agent.WithOnText(func(s string) {
			answer.WriteString(s) // accumulate; glamour-rendered after completion
		}),
		agent.WithOnToolStart(func(_ string, name string, args json.RawMessage) {
			fmt.Printf(cBold+"● %s"+cReset+" %s\n", name, summarizeArgsCLI(args))
		}),
		agent.WithOnToolEnd(func(_ string, _ string, _ string, err error) {
			if err != nil {
				fmt.Printf("  "+cRed+"✗ %s"+cReset+"\n\n", err)
			} else {
				fmt.Printf("  " + cGreen + "✓" + cReset + "\n\n")
			}
		}),
		agent.WithChangeHook(func(path, oldContent, newContent string) {
			printDiffCLI(path, oldContent, newContent)
		}),
	)
	for _, t := range extraTools {
		ag.RegisterTool(t)
	}
	for _, name := range cfg.DisableTools {
		ag.UnregisterTool(name)
	}

	if _, err := ag.Run(context.Background(), task); err != nil {
		fmt.Fprintf(os.Stderr, cRed+"✗ %s"+cReset+"\n", err)
		os.Exit(1)
	}

	// Glamour-render the accumulated answer.
	renderAnswer(answer.String())
}

// renderAnswer renders the agent's final answer as styled markdown.
func renderAnswer(md string) {
	md = strings.TrimSpace(md)
	if md == "" {
		return
	}
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(120))
	if err != nil {
		fmt.Println(md)
		return
	}
	out, err := r.Render(md)
	if err != nil {
		fmt.Println(md)
		return
	}
	fmt.Print(out)
}

// printDiffCLI prints a colored unified diff to stdout (like `git diff --color`).
func printDiffCLI(path, oldContent, newContent string) {
	if oldContent == newContent {
		return
	}
	diff := strings.TrimSpace(udiff.Unified("a/"+path, "b/"+path, oldContent, newContent))
	if diff == "" {
		return
	}
	fmt.Printf(cBold + "✎ " + path + cReset + "\n")
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			fmt.Printf(cDim+"%s"+cReset+"\n", line)
		case strings.HasPrefix(line, "+"):
			fmt.Printf(cGreen+"%s"+cReset+"\n", line)
		case strings.HasPrefix(line, "-"):
			fmt.Printf(cRed+"%s"+cReset+"\n", line)
		case strings.HasPrefix(line, "@@"):
			fmt.Printf(cCyan+"%s"+cReset+"\n", line)
		default:
			fmt.Printf(cDim+"%s"+cReset+"\n", line)
		}
	}
	fmt.Println()
}

// summarizeArgsCLI extracts a short hint from a tool's JSON args for the CLI indicator.
func summarizeArgsCLI(args json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	for _, k := range []string{"path", "command", "pattern", "url", "prompt", "query"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return cDim + k + "=" + truncStr(s, 60) + cReset
			}
		}
	}
	return ""
}

func truncStr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

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
