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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dh-kam/simple-agentic-coding/agent"
	"github.com/dh-kam/simple-agentic-coding/config"
	"github.com/dh-kam/simple-agentic-coding/mcp"
	"github.com/dh-kam/simple-agentic-coding/tui"

	"net/http"

	"github.com/aymanbagabas/go-udiff"

	"github.com/dh-kam/simple-agentic-coding/capture"

	"github.com/anthropics/anthropic-sdk-go/option"
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

// makeClient creates a Backend based on AGENT_API (default: anthropic).
// Supports: "anthropic" (Anthropic SDK) and "openai" (OpenAI-compatible API).
// har, when non-nil, records traffic for *either* backend — it used to be
// wired only into the Anthropic path, so OpenAI/GLM sessions announced a HAR
// file and then wrote an empty one.
func makeClient(apiKey, baseURL string, har *capture.Transport) agent.Backend {
	var transport http.RoundTripper = agent.NewHTTPTransport()
	if har != nil {
		har.Base = transport
		transport = har
	}

	apiType := os.Getenv("AGENT_API")
	if apiType == "" {
		apiType = "anthropic"
	}
	if apiType == "anthropic" {
		return agent.NewAnthropicBackend(apiKey, baseURL,
			option.WithHTTPClient(&http.Client{Transport: transport}))
	}

	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	fmt.Fprintf(os.Stderr, cDim+"🔌 OpenAI backend: %s"+cReset+"\n", baseURL)
	return agent.NewOpenAIBackend(apiKey, baseURL, transport)
}

// main keeps no logic of its own so that run()'s defers — saving the HAR log
// above all — still execute on the error paths. log.Fatal and os.Exit skip
// deferred calls, which is how every failed session used to lose its capture.
func main() { os.Exit(run()) }

// fail prints an error and returns the process exit code, so callers can
// `return fail(...)` from run() and let its defers unwind.
func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, cRed+"✗ "+format+cReset+"\n", args...)
	return 1
}

func run() int {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("agentic %s (commit %s, %s)\n", version, commit, date)
		return 0
	}

	// Skills management subcommands (no API key needed).
	if len(os.Args) > 1 && os.Args[1] == "skills" {
		return handleSkills(os.Args[2:])
	}

	// Load .env but protect security-sensitive vars from being overridden
	// by a project-local .env (prevents BASE_URL redirect attacks).
	savedBaseURL := os.Getenv("ANTHROPIC_BASE_URL")
	savedAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	savedAPI := os.Getenv("AGENT_API")
	_ = godotenv.Load()
	// Restore security vars from real env if .env tried to override them.
	if savedBaseURL != "" {
		os.Setenv("ANTHROPIC_BASE_URL", savedBaseURL)
	}
	if savedAPIKey != "" {
		os.Setenv("ANTHROPIC_API_KEY", savedAPIKey)
	}
	if savedAPI != "" {
		os.Setenv("AGENT_API", savedAPI)
	}

	// HAR capture: default ON, saved to ~/.agentic/hars/session-<timestamp>.har
	// Override path with AGENT_HAR_FILE; disable with AGENT_HAR_DISABLE=1.
	var harTransport *capture.Transport
	if os.Getenv("AGENT_HAR_DISABLE") != "1" {
		harPath := os.Getenv("AGENT_HAR_FILE")
		if harPath == "" {
			home, _ := os.UserHomeDir()
			harDir := filepath.Join(home, ".agentic", "hars")
			os.MkdirAll(harDir, 0755)
			harPath = filepath.Join(harDir, "session-"+time.Now().Format("20060102-150405")+".har")
		}
		harLog := &capture.HARLog{}
		harTransport = capture.NewTransport(harLog)
		defer harTransport.Save(harPath)
		fmt.Fprintf(os.Stderr, cDim+"🗄  HAR → %s"+cReset+"\n", harPath)
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fail("ANTHROPIC_API_KEY 가 필요합니다 (.env 또는 환경변수)")
	}
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	cfg := config.LoadEnv()
	model := cfg.Model
	if model == "" {
		model = "claude-opus-5"
	}
	system := systemPrompt
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt
	}
	base, err := os.Getwd()
	if err != nil {
		return fail("%s", err)
	}

	// Doctor diagnostic.
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		fmt.Println(agent.RunDiagnostics(model, baseURL))
		return 0
	}

	// Terminal setup.
	if len(os.Args) > 1 && os.Args[1] == "terminal-setup" {
		fmt.Println(agent.SetupTerminal())
		return 0
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
	// Load project memory (AGENTS.md / CLAUDE.md)
	system += agent.LoadProjectMemory(base)

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
		if err := tui.Run(makeClient(apiKey, baseURL, harTransport), model, base, extraTools, mcpSuffix+skillSummary); err != nil {
			return fail("%s", err)
		}
		return 0
	}
	// Strip "ask" or "-p" prefix.
	if promptArgs[0] == "ask" || promptArgs[0] == "-p" {
		promptArgs = promptArgs[1:]
	}
	if len(promptArgs) == 0 {
		return fail("프롬프트를 입력하세요: agentic ask \"your prompt\"")
	}
	task := strings.Join(promptArgs, " ")

	// --- One-shot / ask mode ---
	var client agent.Backend = makeClient(apiKey, baseURL, harTransport)
	if dir := os.Getenv("AGENT_RECORD_DIR"); dir != "" {
		rec, err := agent.NewRecorder(client, dir)
		if err != nil {
			return fail("%s", err)
		}
		client = rec
		fmt.Fprintf(os.Stderr, cDim+"🗄  recording to %s"+cReset+"\n", dir)
	}

	// Accumulate the answer for glamour rendering at the end.
	var answer strings.Builder

	ag := agent.BuildCodingAssistant(client, model, system, base,
		agent.WithMaxTokens(128000),
		agent.WithMaxContextTokens(cfg.MaxContextTokens),
		agent.WithApprover(oneShotApprover(agent.LoadSettings(base))),
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

	// Background shells outlive the agent loop; without this they are orphaned
	// when the process exits.
	defer ag.Shells().KillAll()

	_, runErr := ag.Run(context.Background(), task)
	renderAnswer(answer.String())
	if u := ag.TotalUsage(); u.InputTokens+u.OutputTokens > 0 {
		fmt.Fprintf(os.Stderr, cDim+"💰 tokens: in %d · out %d"+cReset+"\n", u.InputTokens, u.OutputTokens)
	}
	if runErr != nil {
		return fail("%s", runErr)
	}
	return 0
}

// oneShotApprover is the permission gate for `agentic ask`. Nobody is at the
// keyboard, so a call the rules do not decide runs. The persistent rules still
// apply — deny_tools and mode:"plan" refuse exactly as they do in the TUI,
// which is the only way to hold back a non-interactive run.
func oneShotApprover(s *agent.Settings) agent.Approver {
	return func(name string, _ json.RawMessage) (bool, string) {
		if s.Decide(name) == agent.DecideDeny {
			return false, ".agentic/settings.json 규칙에 의해 차단됨"
		}
		return true, ""
	}
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
	fmt.Printf(cBold+"✎ %s"+cReset+"\n", path)
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

func truncStr(s string, n int) string { return agent.TruncRunes(s, n) }

// handleSkills processes the `agentic skills` subcommand and returns an exit code.
func handleSkills(args []string) int {
	skillDir := os.Getenv("AGENT_SKILLS_DIR")
	if skillDir == "" {
		skillDir = ".agentic/skills"
	}

	if len(args) == 0 {
		fmt.Println("Usage:")
		fmt.Println("  agentic skills install <github-url>  Install a skill from GitHub")
		fmt.Println("  agentic skills list                  List installed skills")
		fmt.Println("  agentic skills remove <name>         Remove a skill")
		fmt.Println()
		agent.ListInstalledSkills(skillDir)
		return 0
	}

	switch args[0] {
	case "install":
		if len(args) < 2 {
			return fail("Usage: agentic skills install <github-url|owner/repo>")
		}
		fmt.Printf("Installing from %s ...\n", args[1])
		names, err := agent.InstallFromGitHub(args[1], skillDir)
		if err != nil {
			return fail("%s", err)
		}
		for _, n := range names {
			fmt.Printf("  "+cGreen+"✓"+cReset+" installed: %s\n", n)
		}
		fmt.Printf("\n%d skill(s) installed. Run `agentic skills list` to verify.\n", len(names))

	case "list":
		agent.ListInstalledSkills(skillDir)

	case "remove":
		if len(args) < 2 {
			return fail("Usage: agentic skills remove <name>")
		}
		if err := agent.RemoveSkill(skillDir, args[1]); err != nil {
			return fail("%s", err)
		}
		fmt.Printf("  "+cGreen+"✓"+cReset+" removed: %s\n", args[1])

	default:
		return fail("unknown skills command: %s", args[0])
	}
	return 0
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
