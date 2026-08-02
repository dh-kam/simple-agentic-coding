package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// This file implements background shells: run_command can start a command in
// the background, bash_output reads its buffered output, kill_shell stops it.
// Mirrors Claude Code's BashOutput / KillShell.

// lockedWriter is a mutex-guarded buffer, safe for concurrent Write (stdout +
// stderr from one process).
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type bgShell struct {
	cmd  *exec.Cmd
	out  *lockedWriter
	done chan struct{} // closed when the process exits
}

// ShellRegistry tracks background shells by id for bash_output / kill_shell.
type ShellRegistry struct {
	mu      sync.Mutex
	shells  map[string]*bgShell
	counter uint64
}

// NewShellRegistry builds an empty registry.
func NewShellRegistry() *ShellRegistry {
	return &ShellRegistry{shells: make(map[string]*bgShell)}
}

// Start launches cmd in the background, capturing combined stdout+stderr. The
// command runs in its own process group so Kill can take down the whole tree
// (otherwise a forked child like `sleep` survives and keeps the pipe open).
func (r *ShellRegistry) Start(cmd *exec.Cmd) (string, error) {
	lw := &lockedWriter{}
	cmd.Stdout = lw
	cmd.Stderr = lw
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	id := fmt.Sprintf("shell-%d", atomic.AddUint64(&r.counter, 1))
	sh := &bgShell{cmd: cmd, out: lw, done: make(chan struct{})}
	r.mu.Lock()
	r.shells[id] = sh
	r.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		close(sh.done)
	}()
	return id, nil
}

func (r *ShellRegistry) get(id string) (*bgShell, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sh, ok := r.shells[id]
	return sh, ok
}

// Output returns captured output so far and whether the shell is still running.
func (r *ShellRegistry) Output(id string) (string, bool, error) {
	sh, ok := r.get(id)
	if !ok {
		return "", false, fmt.Errorf("unknown shell_id %q", id)
	}
	running := true
	select {
	case <-sh.done:
		running = false
	default:
	}
	return sh.out.String(), running, nil
}

// Kill terminates a background shell and its whole process group, then waits
// for it to exit (so the output pipe closes and buffered output is flushed).
func (r *ShellRegistry) Kill(id string) error {
	sh, ok := r.get(id)
	if !ok {
		return fmt.Errorf("unknown shell_id %q", id)
	}
	_ = killProcessTree(sh.cmd)
	<-sh.done
	r.mu.Lock()
	delete(r.shells, id)
	r.mu.Unlock()
	return nil
}

// NewBashOutputTool reads a background shell's captured output so far.
func NewBashOutputTool(reg *ShellRegistry) Tool {
	return Tool{
		Name:        "bash_output",
		Description: "백그라운드 shell_id 의 지금까지 출력(stdout+stderr)과 실행 중 여부를 반환한다.",
		InputSchema: map[string]any{
			"shell_id": map[string]any{"type": "string", "description": "run_command(background=true) 가 반환한 id"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				ShellID string `json:"shell_id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			out, running, err := reg.Output(in.ShellID)
			if err != nil {
				return "", err
			}
			state := "running"
			if !running {
				state = "exited"
			}
			return fmt.Sprintf("[%s]\n%s", state, out), nil
		},
	}
}

// NewKillShellTool stops a background shell.
func NewKillShellTool(reg *ShellRegistry) Tool {
	return Tool{
		Name:        "kill_shell",
		Description: "백그라운드 shell_id 의 프로세스를 종료한다.",
		InputSchema: map[string]any{
			"shell_id": map[string]any{"type": "string"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				ShellID string `json:"shell_id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if err := reg.Kill(in.ShellID); err != nil {
				return "", err
			}
			return "killed " + in.ShellID, nil
		},
	}
}

// CleanupStale removes shells that have been idle for longer than maxAge.
func (r *ShellRegistry) CleanupStale(maxAge time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for id, sh := range r.shells {
		select {
		case <-sh.done:
			delete(r.shells, id)
			count++
		default:
		}
	}
	return count
}
