// Package config loads the optional agentic configuration file
// (AGENT_CONFIG path, JSON): system prompt, model, max context tokens, and a
// disable list for tools. All fields are optional; missing fields keep defaults.
package config

import (
	"encoding/json"
	"fmt"
	"os"
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

// LoadEnv loads AGENT_CONFIG if set; otherwise returns an empty config. A read
// or parse error is reported to stderr and treated as "no config" (don't fail
// the whole program over an optional file).
func LoadEnv() *File {
	path := os.Getenv("AGENT_CONFIG")
	if path == "" {
		return &File{}
	}
	f, err := Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "⚠", err)
		return &File{}
	}
	return f
}
