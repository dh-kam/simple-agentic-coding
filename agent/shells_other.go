//go:build !unix

package agent

import "os/exec"

// setProcessGroup is a no-op on non-Unix platforms (no portable process-group
// concept). killProcessTree then falls back to killing the direct process; some
// grandchildren may survive, but the package compiles and runs.
func setProcessGroup(cmd *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
