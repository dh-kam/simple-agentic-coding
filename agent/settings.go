package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readOnlyTools observe without changing anything outside the agent, so they
// can run unattended. This is an allowlist rather than a list of dangerous
// tools on purpose: anything absent — a new built-in, or a tool an MCP server
// exposes — is gated by default, because the agent cannot know what it does.
var readOnlyTools = map[string]bool{
	"read_file": true, "glob": true, "grep": true, "list_files": true,
	"web_fetch": true, "web_search": true, "todo_write": true,
	"bash_output": true, "load_skill": true, "code_review": true,
	// task itself is harmless; the subagent's own calls are gated
	// individually because it inherits the parent's approver.
	"task": true,
}

// IsMutating reports whether a tool call needs the user's permission.
func IsMutating(toolName string) bool { return !readOnlyTools[toolName] }

// Decision is the outcome of consulting the persistent permission rules.
type Decision int

const (
	DecideAsk Decision = iota // no rule applies — prompt the user
	DecideAllow
	DecideDeny
)

// Settings holds persistent permission rules, read from
// <base>/.agentic/settings.json.
type Settings struct {
	AllowTools []string `json:"allow_tools,omitempty"`
	DenyTools  []string `json:"deny_tools,omitempty"`
	// Mode: "" (ask for mutating tools), "plan" (refuse them outright),
	// "auto-edit" (file edits pass, commands still ask), "full-auto" (never ask).
	Mode string `json:"mode,omitempty"`
}

func LoadSettings(base string) *Settings {
	s := &Settings{}
	data, err := os.ReadFile(filepath.Join(base, ".agentic", "settings.json"))
	if err == nil {
		if err := json.Unmarshal(data, s); err != nil {
			fmt.Fprintln(os.Stderr, "⚠ .agentic/settings.json:", err)
		}
	}
	return s
}

func (s *Settings) Save(base string) error {
	dir := filepath.Join(base, ".agentic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644)
}

// Decide applies the persistent rules to one tool call. Deny wins over allow,
// and both win over the mode, so an explicit rule is never overridden by
// full-auto.
func (s *Settings) Decide(toolName string) Decision {
	for _, t := range s.DenyTools {
		if t == toolName {
			return DecideDeny
		}
	}
	for _, t := range s.AllowTools {
		if t == toolName {
			return DecideAllow
		}
	}
	if !IsMutating(toolName) {
		return DecideAllow
	}
	switch s.Mode {
	case "full-auto":
		return DecideAllow
	case "plan":
		return DecideDeny
	case "auto-edit":
		switch toolName {
		case "write", "edit", "multi_edit", "notebook_edit":
			return DecideAllow
		}
	}
	return DecideAsk
}

// Describe renders the active rules for /status.
func (s *Settings) Describe() string {
	mode := s.Mode
	if mode == "" {
		mode = "ask (기본)"
	}
	out := "  mode: " + mode
	if len(s.AllowTools) > 0 {
		out += fmt.Sprintf("\n  allow: %v", s.AllowTools)
	}
	if len(s.DenyTools) > 0 {
		out += fmt.Sprintf("\n  deny: %v", s.DenyTools)
	}
	return out
}
