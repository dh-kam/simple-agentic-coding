package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SetupTerminal prints shell integration instructions and optionally
// writes a completion script.
func SetupTerminal() string {
	home, _ := os.UserHomeDir()
	var sb strings.Builder
	sb.WriteString("🖥  터미널 설정\n\n")

	// Bash completion
	bashCompletion := `# agentic completion (add to ~/.bashrc)
_agentic_completion() {
    local cur opts
    cur="${COMP_WORDS[COMP_CWORD]}"
    opts="ask skills --version -p"
    COMPREPLY=( $(compgen -W "$opts" -- "$cur") )
}
complete -F _agentic_completion agentic
`

	bashPath := filepath.Join(home, ".agentic", "completion.bash")
	os.MkdirAll(filepath.Dir(bashPath), 0755)
	os.WriteFile(bashPath, []byte(bashCompletion), 0644)

	sb.WriteString(fmt.Sprintf("  Bash completion: %s\n", bashPath))
	sb.WriteString("  Add to ~/.bashrc:\n")
	sb.WriteString("    source " + bashPath + "\n\n")

	// Zsh completion
	zshCompletion := `#compdef agentic
_agentic() {
    local -a opts
    opts=('ask:one-shot prompt' 'skills:manage skills' '--version:version' '-p:prompt')
    _describe 'agentic' opts
}
compdef _agentic agentic
`
	zshPath := filepath.Join(home, ".agentic", "completion.zsh")
	os.WriteFile(zshPath, []byte(zshCompletion), 0644)

	sb.WriteString(fmt.Sprintf("  Zsh completion: %s\n", zshPath))
	sb.WriteString("  Add to ~/.zshrc:\n")
	sb.WriteString("    source " + zshPath + "\n")

	// Environment variables
	sb.WriteString("\n  Environment:\n")
	sb.WriteString("    ANTHROPIC_API_KEY=...      (required)\n")
	sb.WriteString("    ANTHROPIC_BASE_URL=...     (GLM: https://open.bigmodel.cn/api/anthropic)\n")
	sb.WriteString("    AGENT_MODEL=glm-5.2        (or claude-opus-5)\n")
	sb.WriteString("    AGENT_API=anthropic|openai  (backend selection)\n")

	return sb.String()
}
