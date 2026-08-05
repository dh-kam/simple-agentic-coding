package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dh-kam/simple-agentic-coding/agent"
)

// One-shot mode used to build its Agent with no approver at all, so
// .agentic/settings.json was honoured in the TUI and silently ignored by
// `agentic ask`.
func TestOneShotApprover(t *testing.T) {
	cases := []struct {
		name      string
		settings  agent.Settings
		tool      string
		wantAllow bool
	}{
		{"unrestricted runs", agent.Settings{}, "run_command", true},
		{"deny list refuses", agent.Settings{DenyTools: []string{"run_command"}}, "run_command", false},
		{"deny beats full-auto", agent.Settings{DenyTools: []string{"write"}, Mode: "full-auto"}, "write", false},
		{"plan mode refuses writes", agent.Settings{Mode: "plan"}, "write", false},
		{"plan mode refuses git", agent.Settings{Mode: "plan"}, "git_commit", false},
		{"plan mode still reads", agent.Settings{Mode: "plan"}, "read_file", true},
		{"allow list runs", agent.Settings{AllowTools: []string{"write"}}, "write", true},
	}
	for _, c := range cases {
		s := c.settings
		allow, reason := oneShotApprover(&s)(c.tool, json.RawMessage(`{}`))
		if allow != c.wantAllow {
			t.Errorf("%s: approver(%q) = %v, want %v", c.name, c.tool, allow, c.wantAllow)
		}
		if !allow && reason == "" {
			t.Errorf("%s: a refusal must tell the model why", c.name)
		}
	}
}

func TestOneShotApprover_readsSettingsFromDisk(t *testing.T) {
	base := t.TempDir()
	s := &agent.Settings{DenyTools: []string{"run_command"}}
	if err := s.Save(base); err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Abs(base); err != nil {
		t.Fatal(err)
	}
	approve := oneShotApprover(agent.LoadSettings(base))
	if allow, _ := approve("run_command", json.RawMessage(`{}`)); allow {
		t.Error("deny_tools from settings.json was not applied")
	}
	if allow, _ := approve("edit", json.RawMessage(`{}`)); !allow {
		t.Error("an unlisted tool should still run in one-shot mode")
	}
}

func TestTruncStr_isRuneSafe(t *testing.T) {
	got := truncStr("한국어 요약 문장입니다 반복 반복", 8)
	if strings.ContainsRune(got, '�') {
		t.Errorf("truncation produced a replacement char: %q", got)
	}
	if n := len([]rune(got)); n > 8 {
		t.Errorf("got %d runes, want <= 8", n)
	}
}
