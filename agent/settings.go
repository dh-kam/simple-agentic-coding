package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings holds persistent permission rules.
type Settings struct {
	AllowTools []string `json:"allow_tools,omitempty"`
	DenyTools  []string `json:"deny_tools,omitempty"`
	Mode       string   `json:"mode,omitempty"` // "", "plan", "auto-edit", "full-auto"
}

func LoadSettings(base string) *Settings {
	s := &Settings{}
	data, err := os.ReadFile(filepath.Join(base, ".agentic", "settings.json"))
	if err == nil {
		json.Unmarshal(data, s)
	}
	return s
}

func (s *Settings) Save(base string) error {
	dir := filepath.Join(base, ".agentic")
	os.MkdirAll(dir, 0755)
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(dir, "settings.json"), data, 0644)
}

// ShouldAllow checks if a tool should be auto-allowed by persistent rules.
func (s *Settings) ShouldAllow(toolName string) bool {
	for _, t := range s.DenyTools {
		if t == toolName {
			return false
		}
	}
	for _, t := range s.AllowTools {
		if t == toolName {
			return true
		}
	}
	return false // no rule → ask (unless full-auto)
}
