// Package config loads the optional agentic configuration file
// (AGENT_CONFIG path, JSON): system prompt, model, max context tokens, and a
// disable list for tools. All fields are optional; missing fields keep defaults.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// File is the parsed config.
type File struct {
	SystemPrompt     string   `json:"system_prompt,omitempty"`
	Model            string   `json:"model,omitempty"`
	MaxContextTokens int      `json:"max_context_tokens,omitempty"`
	DisableTools     []string `json:"disable_tools,omitempty"`
}

// Load reads and parses a config file.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}
	return &f, nil
}

// DefaultMaxContextTokens is used when neither the config file nor the
// environment sets a limit.
const DefaultMaxContextTokens = 50000

// LoadEnv loads AGENT_CONFIG if set, then fills unset fields from the
// environment. A read or parse error is reported to stderr and treated as
// "no config" (don't fail the whole program over an optional file).
//
// Resolving the environment here rather than at each call site is what keeps
// the TUI and the one-shot CLI in agreement — AGENT_MAX_CONTEXT_TOKENS used to
// be read only by the one-shot path and silently ignored in the TUI.
func LoadEnv() *File {
	f := &File{}
	if path := os.Getenv("AGENT_CONFIG"); path != "" {
		loaded, err := Load(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "⚠", err)
		} else {
			f = loaded
		}
	}
	if f.Model == "" {
		f.Model = os.Getenv("AGENT_MODEL")
	}
	if f.MaxContextTokens <= 0 {
		if n, err := strconv.Atoi(os.Getenv("AGENT_MAX_CONTEXT_TOKENS")); err == nil && n > 0 {
			f.MaxContextTokens = n
		} else {
			f.MaxContextTokens = DefaultMaxContextTokens
		}
	}
	return f
}
