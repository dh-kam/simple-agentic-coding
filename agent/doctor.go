package agent

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// RunDiagnostics checks system health and returns a report.
func RunDiagnostics(model, baseURL string) string {
	var sb strings.Builder
	sb.WriteString("🔍 agentic 진단\n\n")

	// Go version
	sb.WriteString(fmt.Sprintf("  Go: %s\n", runtime.Version()))

	// Platform
	sb.WriteString(fmt.Sprintf("  OS: %s/%s\n", runtime.GOOS, runtime.GOARCH))

	// Model
	sb.WriteString(fmt.Sprintf("  Model: %s\n", model))
	sb.WriteString(fmt.Sprintf("  Endpoint: %s\n", baseURL))

	// Git
	if git, err := exec.LookPath("git"); err == nil {
		out, _ := exec.Command(git, "--version").Output()
		sb.WriteString(fmt.Sprintf("  Git: %s", string(out)))
	} else {
		sb.WriteString("  Git: ❌ not found\n")
	}

	// .env
	if _, err := os.Stat(".env"); err == nil {
		sb.WriteString("  .env: ✅ found\n")
	} else {
		sb.WriteString("  .env: ⚠️ not found (using env vars)\n")
	}

	// Skills
	if entries, err := os.ReadDir(".agentic/skills"); err == nil {
		sb.WriteString(fmt.Sprintf("  Skills: %d installed\n", len(entries)))
	} else {
		sb.WriteString("  Skills: 0 (none installed)\n")
	}

	// MCP config
	if mcpPath := os.Getenv("AGENT_MCP_CONFIG"); mcpPath != "" {
		if _, err := os.Stat(mcpPath); err == nil {
			sb.WriteString(fmt.Sprintf("  MCP: %s ✅\n", mcpPath))
		} else {
			sb.WriteString(fmt.Sprintf("  MCP: %s ❌ not found\n", mcpPath))
		}
	} else {
		sb.WriteString("  MCP: not configured\n")
	}

	return sb.String()
}
