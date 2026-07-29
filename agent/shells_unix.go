//go:build unix

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the background command in its own process group so
// killProcessTree can take down the whole tree (otherwise a forked child like
// `sleep` survives and keeps the pipe open).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree signals the command's whole process group.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
