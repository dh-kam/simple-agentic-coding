// Package tui implements a Claude-Code-style interactive REPL using
// bubbletea + lipgloss + glamour. It is not a byte-for-byte clone of Claude
// Code, but mirrors its interaction model: welcome banner, prompt box,
// streaming markdown answers, tool-call lines with a spinner → ✓/✗, and
// slash commands.
package tui

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/dh-kam/simple-agentic-coding/agent"
	"github.com/dh-kam/simple-agentic-coding/config"

	"github.com/charmbracelet/bubbletea"
)

// shouldAsk reports whether a tool call needs user permission in the TUI.
//
// This delegates to agent.IsMutating so the list cannot drift from the tool
// set again: git, git_commit and notebook_edit were all missing here, which
// left the agent able to rewrite notebooks, stage commits and mutate
// .git/config without a prompt.
func shouldAsk(name string) bool { return agent.IsMutating(name) }

// Run launches the REPL against the given provider client. Blocks until the
// user quits (Ctrl+C or /exit). The agent's full tool set (CCTools + task
// subagent) plus any extraTools (e.g. MCP tools) is wired here; events stream
// back into the program via hooks.
func Run(client agent.Backend, modelName, base string, extraTools []agent.Tool, systemSuffix string) error {
	cfg := config.LoadEnv()
	system := systemPrompt
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt
	}
	system += systemSuffix
	system += agent.LoadProjectMemory(base)

	m := newModel(client, modelName, base)
	m.ctx, m.rootCancel = context.WithCancel(context.Background())
	m.settings = agent.LoadSettings(base)
	m.fileHistory = agent.NewFileHistory()
	m.customCmds = agent.LoadCustomCommands(base)

	m.agent = agent.BuildCodingAssistant(client, modelName, system, base,
		agent.WithMaxTokens(128000),
		agent.WithMaxContextTokens(cfg.MaxContextTokens),
		agent.WithPlanner(agent.NewLLMPlanner(client, modelName)),
		agent.WithOnText(func(s string) { m.send(deltaMsg{text: s}) }),
		agent.WithOnPlan(func(p string) { m.send(planMsg{plan: p}) }),
		agent.WithOnToolStart(func(id, name string, args json.RawMessage) {
			m.send(toolStartMsg{id: id, name: name, args: args})
		}),
		agent.WithOnToolEnd(func(id, name, result string, err error) {
			m.send(toolEndMsg{id: id, name: name, result: result, isErr: err != nil})
		}),
		agent.WithChangeHook(func(path, oldContent, newContent string) {
			// Snapshot before the diff so /undo can roll the edit back. Paths
			// arrive relative to base; store them absolute so undo does not
			// depend on the process working directory.
			m.fileHistory.Snapshot(filepath.Join(base, path), oldContent)
			m.send(changeMsg{path: path, oldContent: oldContent, newContent: newContent})
		}),
		agent.WithApprover(m.approve),
	)
	for _, t := range extraTools {
		m.agent.RegisterTool(t)
	}
	for _, name := range cfg.DisableTools {
		m.agent.UnregisterTool(name)
	}
	// Background shells run in their own process group and would otherwise
	// survive the REPL.
	defer m.agent.Shells().KillAll()

	p := tea.NewProgram(m, tea.WithAltScreen())
	m.program = p
	_, err := p.Run()
	return err
}
