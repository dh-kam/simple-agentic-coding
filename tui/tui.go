// Package tui implements a Claude-Code-style interactive REPL using
// bubbletea + lipgloss + glamour. It is not a byte-for-byte clone of Claude
// Code, but mirrors its interaction model: welcome banner, prompt box,
// streaming markdown answers, tool-call lines with a spinner → ✓/✗, and
// slash commands.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/dh-kam/simple-agentic-coding/agent"
	"github.com/dh-kam/simple-agentic-coding/config"

	"github.com/charmbracelet/bubbletea"
)

// shouldAsk reports whether a tool call needs user permission in the TUI.
// Mutating/executing tools do; read-only tools auto-allow.
func shouldAsk(name string) bool {
	switch name {
	case "write", "edit", "multi_edit", "run_command":
		return true
	}
	return false
}

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
	maxCtx := 50000
	if cfg.MaxContextTokens > 0 {
		maxCtx = cfg.MaxContextTokens
	}

	m := newModel(client, modelName, base)
	m.ctx, m.cancel = context.WithCancel(context.Background())

	m.agent = agent.BuildCodingAssistant(client, modelName, system, base,
		agent.WithMaxTokens(128000),
		agent.WithMaxContextTokens(maxCtx),
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
			m.send(changeMsg{path: path, oldContent: oldContent, newContent: newContent})
		}),
		agent.WithApprover(func(name string, args json.RawMessage) (bool, string) {
			if !shouldAsk(name) {
				return true, "" // read-only tools auto-allow
			}
			id := atomic.AddUint64(&m.approvalSeq, 1)
			req := approvalReq{
				id:   fmt.Sprintf("ap-%d", id),
				name: name,
				args: args,
				resp: make(chan choice, 1),
			}
			m.send(approvalMsg{req: req})
			c := <-req.resp // block until the user decides
			// Note: this blocks until user responds. ctx.Done() handling
			// is via drainApprovals() which sends deny on interrupt.
			return c.allow, c.reason
		}),
	)
	for _, t := range extraTools {
		m.agent.RegisterTool(t)
	}
	for _, name := range cfg.DisableTools {
		m.agent.UnregisterTool(name)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	m.program = p
	_, err := p.Run()
	return err
}
