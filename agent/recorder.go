package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Recorder wraps a Backend and persists every request/response to a directory
// for offline replay tests.
type Recorder struct {
	inner Backend
	dir   string
	n     int
}

func NewRecorder(inner Backend, dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create record dir %q: %w", dir, err)
	}
	return &Recorder{inner: inner, dir: dir}, nil
}

func (r *Recorder) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	idx := r.n
	r.n++
	if b, err := json.MarshalIndent(req, "", "  "); err == nil {
		os.WriteFile(filepath.Join(r.dir, fmt.Sprintf("%03d_request.json", idx)), b, 0644)
	}
	resp, err := r.inner.Chat(ctx, req, onDelta)
	if err != nil {
		return resp, err
	}
	if b, err := json.MarshalIndent(resp, "", "  "); err == nil {
		os.WriteFile(filepath.Join(r.dir, fmt.Sprintf("%03d_response.json", idx)), b, 0644)
	}
	return resp, nil
}

// ReplayClient reads captured *_response.json files and replays them.
type ReplayClient struct {
	files []string
	i     int
	Reqs  []ChatRequest // recorded requests for inspection
}

func NewReplayClient(dir string) (*ReplayClient, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_response.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no *_response.json in %q", dir)
	}
	return &ReplayClient{files: files}, nil
}

func (r *ReplayClient) Chat(_ context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	r.Reqs = append(r.Reqs, req)
	if r.i >= len(r.files) {
		return nil, fmt.Errorf("replay exhausted: %d responses, got call #%d", len(r.files), r.i+1)
	}
	f := r.files[r.i]
	r.i++
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	var resp ChatResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", filepath.Base(f), err)
	}
	if onDelta != nil && resp.Content != "" {
		onDelta(resp.Content)
	}
	return &resp, nil
}

// CloseAll is a no-op for Backend (no persistent connections at this level).
func CloseAll(_ []*struct{}) {}
