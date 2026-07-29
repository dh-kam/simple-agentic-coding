package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	in := `{
		"system_prompt": "you are a test agent",
		"model": "glm-5.2",
		"max_context_tokens": 12345,
		"disable_tools": ["run_command", "task"]
	}`
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.SystemPrompt != "you are a test agent" {
		t.Errorf("system_prompt = %q", f.SystemPrompt)
	}
	if f.Model != "glm-5.2" {
		t.Errorf("model = %q", f.Model)
	}
	if f.MaxContextTokens != 12345 {
		t.Errorf("max_context_tokens = %d", f.MaxContextTokens)
	}
	if len(f.DisableTools) != 2 || f.DisableTools[0] != "run_command" {
		t.Errorf("disable_tools = %v", f.DisableTools)
	}
}

func TestLoadEnv_unset(t *testing.T) {
	t.Setenv("AGENT_CONFIG", "")
	f := LoadEnv()
	if f == nil || f.SystemPrompt != "" {
		t.Error("unset AGENT_CONFIG should yield empty config")
	}
}

func TestLoadEnv_missingFile(t *testing.T) {
	t.Setenv("AGENT_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	f := LoadEnv() // should warn + return empty, not panic
	if f.SystemPrompt != "" {
		t.Errorf("expected empty config on missing file, got %q", f.SystemPrompt)
	}
}
