package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SlashCommand is a user-defined or built-in slash command.
type SlashCommand struct {
	Name        string
	Description string
	Handler     func(ctx context.Context, args string) (string, error)
}

// CommandRegistry holds all registered slash commands.
type CommandRegistry struct {
	commands map[string]*SlashCommand
}

func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{commands: make(map[string]*SlashCommand)}
}

func (r *CommandRegistry) Register(cmd *SlashCommand) {
	r.commands[cmd.Name] = cmd
}

func (r *CommandRegistry) Get(name string) (*SlashCommand, bool) {
	c, ok := r.commands[name]
	return c, ok
}

func (r *CommandRegistry) List() []*SlashCommand {
	var list []*SlashCommand
	for _, c := range r.commands {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// LoadCustomCommands reads user-defined commands from .agentic/commands/*.json.
// Each file: {"name": "/mycmd", "description": "...", "prompt": "..."}
// The "prompt" is injected as a user message when the command is invoked.
func LoadCustomCommands(base string) []CustomCommand {
	dir := filepath.Join(base, ".agentic", "commands")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var cmds []CustomCommand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var cmd CustomCommand
		if json.Unmarshal(data, &cmd) == nil && cmd.Name != "" && cmd.Prompt != "" {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// CustomCommand is a user-defined slash command loaded from JSON.
type CustomCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// FormatCustomCommandList builds a help string for custom commands.
func FormatCustomCommandList(cmds []CustomCommand) string {
	if len(cmds) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n사용자 정의 명령:\n")
	for _, c := range cmds {
		line := "  " + c.Name
		if c.Description != "" {
			line += " — " + c.Description
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// MatchCustomCommand checks if input matches a custom command, returns the expanded prompt.
func MatchCustomCommand(cmds []CustomCommand, input string) (string, string, bool) {
	input = strings.TrimSpace(input)
	parts := strings.SplitN(input, " ", 2)
	cmdName := parts[0]
	args := ""
	if len(parts) > 1 {
		args = parts[1]
	}
	for _, c := range cmds {
		if c.Name == cmdName {
			prompt := c.Prompt
			if args != "" {
				prompt = fmt.Sprintf("%s\n\n추가 입력: %s", prompt, args)
			}
			return cmdName, prompt, true
		}
	}
	return "", "", false
}

func init() { _ = fmt.Sprintf }
