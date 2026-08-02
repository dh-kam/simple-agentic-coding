package agent

import (
	"fmt"
	"os"
	"sync"
)

// FileHistory tracks file changes for undo support.
type FileHistory struct {
	mu        sync.Mutex
	snapshots map[string][]string // path → stack of old contents (LIFO)
}

func NewFileHistory() *FileHistory {
	return &FileHistory{snapshots: make(map[string][]string)}
}

// Snapshot saves the current content of a file before it's modified.
func (h *FileHistory) Snapshot(path, oldContent string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.snapshots[path] = append(h.snapshots[path], oldContent)
}

// Undo restores the previous version of a file.
func (h *FileHistory) Undo(path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	stack := h.snapshots[path]
	if len(stack) == 0 {
		return fmt.Errorf("no history for %s", path)
	}
	prev := stack[len(stack)-1]
	h.snapshots[path] = stack[:len(stack)-1]
	if prev == "" {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(prev), 0644)
}

// UndoAll restores all tracked files to their original state.
func (h *FileHistory) UndoAll() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var restored []string
	for path := range h.snapshots {
		stack := h.snapshots[path]
		if len(stack) > 0 {
			orig := stack[0]
			if orig == "" {
				os.Remove(path)
			} else {
				os.WriteFile(path, []byte(orig), 0644)
			}
			restored = append(restored, path)
		}
	}
	h.snapshots = make(map[string][]string)
	return restored
}

func (h *FileHistory) TrackedFiles() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var files []string
	for f := range h.snapshots {
		files = append(files, f)
	}
	return files
}
