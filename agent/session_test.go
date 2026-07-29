package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestHistoryResume(t *testing.T) {
	fc := &fakeClient{responses: []string{respFinal}}
	ag := New(fc, "m", "s")
	ag.RegisterTool(NewReadFileTool(t.TempDir()))
	if _, err := ag.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	msgs := ag.History()
	if len(msgs) < 2 {
		t.Fatalf("history len = %d, want >= 2", len(msgs))
	}

	// JSON round-trip — this is what /save and /resume rely on.
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []anthropic.MessageParam
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// resume into a fresh agent
	ag2 := New(&fakeClient{responses: []string{respFinal}}, "m", "s")
	ag2.Resume(got)
	if len(ag2.History()) != len(got) {
		t.Errorf("resume: history = %d, want %d", len(ag2.History()), len(got))
	}
}

func TestUnregisterTool(t *testing.T) {
	ag := New(&fakeClient{responses: []string{respFinal}}, "m", "s")
	ag.RegisterTool(NewReadFileTool(t.TempDir()))
	ag.RegisterTool(Tool{Name: "x", InputSchema: map[string]any{}, Run: func(context.Context, json.RawMessage) (string, error) {
		return "", nil
	}})

	ag.UnregisterTool("read_file")

	names := map[string]bool{}
	for _, tu := range ag.toolDefs() {
		if tu.OfTool != nil {
			names[tu.OfTool.Name] = true
		}
	}
	if names["read_file"] {
		t.Error("read_file still advertised after unregister")
	}
	if !names["x"] {
		t.Error("x should still be registered")
	}
	ag.UnregisterTool("does-not-exist") // no-op, must not panic
}
