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

// AGENT_MAX_CONTEXT_TOKENS and AGENT_MODEL used to be read only by the
// one-shot path in main.go, so the TUI silently ignored them. Resolving them
// here is what keeps both entry points in agreement.
func TestLoadEnv_resolvesEnvironment(t *testing.T) {
	t.Setenv("AGENT_CONFIG", "")
	t.Setenv("AGENT_MODEL", "glm-4.6")
	t.Setenv("AGENT_MAX_CONTEXT_TOKENS", "4242")
	f := LoadEnv()
	if f.Model != "glm-4.6" {
		t.Errorf("model = %q, want glm-4.6", f.Model)
	}
	if f.MaxContextTokens != 4242 {
		t.Errorf("max_context_tokens = %d, want 4242", f.MaxContextTokens)
	}
}

func TestLoadEnv_defaults(t *testing.T) {
	t.Setenv("AGENT_CONFIG", "")
	t.Setenv("AGENT_MAX_CONTEXT_TOKENS", "")
	if got := LoadEnv().MaxContextTokens; got != DefaultMaxContextTokens {
		t.Errorf("max_context_tokens = %d, want default %d", got, DefaultMaxContextTokens)
	}
	t.Setenv("AGENT_MAX_CONTEXT_TOKENS", "not-a-number")
	if got := LoadEnv().MaxContextTokens; got != DefaultMaxContextTokens {
		t.Errorf("bad value should fall back to the default, got %d", got)
	}
}

// The config file wins over the environment.
func TestLoadEnv_fileOverridesEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"model":"from-file","max_context_tokens":999}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_CONFIG", path)
	t.Setenv("AGENT_MODEL", "from-env")
	t.Setenv("AGENT_MAX_CONTEXT_TOKENS", "111")
	f := LoadEnv()
	if f.Model != "from-file" || f.MaxContextTokens != 999 {
		t.Errorf("env overrode the config file: %+v", f)
	}
}
