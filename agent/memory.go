package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadProjectMemory reads AGENTS.md (or CLAUDE.md) from the working directory
// and returns its content for injection into the system prompt.
func LoadProjectMemory(base string) string {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(base, name))
		if err == nil && len(data) > 0 {
			return "\n\n## 프로젝트 메모리 (" + name + ")\n" + strings.TrimSpace(string(data))
		}
	}
	return ""
}
