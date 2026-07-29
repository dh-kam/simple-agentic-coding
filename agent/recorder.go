package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/anthropics/anthropic-sdk-go"
)

// Recorder wraps an LLMClient and persists every request/response to a
// directory, so a real provider run (e.g. GLM Coding Plan) can be captured
// once and replayed offline by tests. Files written per call, in order:
//
//	<dir>/NNN_request.json  — the MessageNewParams sent
//	<dir>/NNN_response.json — the Message returned (what the loop consumes)
//
// The API key lives in the HTTP header, never in the request body, so these
// files contain no secrets and are safe to commit.
type Recorder struct {
	inner LLMClient
	dir   string
	n     int
}

// NewRecorder wraps inner and ensures dir exists.
func NewRecorder(inner LLMClient, dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create record dir %q: %w", dir, err)
	}
	return &Recorder{inner: inner, dir: dir}, nil
}

func (r *Recorder) StreamMessage(ctx context.Context, p anthropic.MessageNewParams, onDelta func(string)) (*anthropic.Message, error) {
	idx := r.n
	r.n++

	// request snapshot — best effort; ignore marshal/write errors
	if b, err := json.MarshalIndent(p, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(r.dir, fmt.Sprintf("%03d_request.json", idx)), b, 0o644)
	}

	msg, err := r.inner.StreamMessage(ctx, p, onDelta)
	if err != nil {
		return msg, err
	}

	if b, err := json.MarshalIndent(msg, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(r.dir, fmt.Sprintf("%03d_response.json", idx)), b, 0o644)
	}
	return msg, nil
}

// ReplayClient reads captured *_response.json files from dir (in order) and
// returns one per StreamMessage call. It drives the loop offline using real
// provider data captured by Recorder. onDelta receives each text block, so
// streaming behavior is exercised on real data too.
type ReplayClient struct {
	files  []string
	i      int
	params []anthropic.MessageNewParams
}

// NewReplayClient globs <dir>/*_response.json (sorted) for ordered replay.
func NewReplayClient(dir string) (*ReplayClient, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_response.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no *_response.json fixtures in %q", dir)
	}
	return &ReplayClient{files: files}, nil
}

func (r *ReplayClient) StreamMessage(_ context.Context, p anthropic.MessageNewParams, onDelta func(string)) (*anthropic.Message, error) {
	r.params = append(r.params, p)
	if r.i >= len(r.files) {
		return nil, fmt.Errorf("replay exhausted: %d responses recorded, got call #%d", len(r.files), r.i+1)
	}
	f := r.files[r.i]
	r.i++

	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	var msg anthropic.Message
	if err := json.Unmarshal(b, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", filepath.Base(f), err)
	}
	if onDelta != nil {
		for _, blk := range msg.Content {
			if tb, ok := blk.AsAny().(anthropic.TextBlock); ok {
				onDelta(tb.Text)
			}
		}
	}
	return &msg, nil
}
