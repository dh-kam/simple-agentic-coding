package capture

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newRecordingClient(t *testing.T) (*http.Client, *HARLog) {
	t.Helper()
	log := &HARLog{}
	tp := NewTransport(log)
	return &http.Client{Transport: tp}, log
}

func TestTransport_recordsRequestAndResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client, log := newRecordingClient(t)
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close() // the entry is committed on Close

	if len(log.Log.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(log.Log.Entries))
	}
	e := log.Log.Entries[0]
	if e.Request.PostData == nil || !strings.Contains(e.Request.PostData.Text, "hi") {
		t.Errorf("request body not captured: %+v", e.Request.PostData)
	}
	if !strings.Contains(e.Response.Content.Text, `"ok":true`) {
		t.Errorf("response body not captured: %q", e.Response.Content.Text)
	}
	if e.Response.Status != 200 {
		t.Errorf("status = %d", e.Response.Status)
	}
}

// Streamed SSE bodies are read incrementally; every chunk must still land in
// the log, which is the whole point of wrapping the body.
func TestTransport_capturesStreamedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		for _, chunk := range []string{"data: a\n", "data: b\n", "data: [DONE]\n"} {
			io.WriteString(w, chunk)
			f.Flush()
		}
	}))
	defer srv.Close()

	client, log := newRecordingClient(t)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	for {
		if _, err := resp.Body.Read(buf); err != nil {
			break
		}
	}
	resp.Body.Close()

	if len(log.Log.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(log.Log.Entries))
	}
	got := log.Log.Entries[0].Response.Content.Text
	for _, want := range []string{"data: a", "data: b", "[DONE]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in captured stream: %q", want, got)
		}
	}
}

func TestRedaction(t *testing.T) {
	for _, c := range []struct{ name, in, mustNotContain string }{
		{"openai key", `{"key":"sk-abcdefghijklmnop"}`, "sk-abcdefghijklmnop"},
		{"anthropic key", `{"key":"sk-ant-abcdefghijklmnop"}`, "sk-ant-abcdefghijklmnop"},
		{"github token", `ghp_abcdefghijklmnopqrst`, "ghp_abcdefghijklmnopqrst"},
		{"bearer", `Authorization: Bearer abcdefghijklmnop`, "Bearer abcdefghijklmnop"},
	} {
		if got := redactBody(c.in); strings.Contains(got, c.mustNotContain) {
			t.Errorf("%s: secret survived redaction: %q", c.name, got)
		}
	}

	if got := redactKey("Authorization", "Bearer sk-ant-verysecretvalue"); strings.Contains(got, "verysecret") {
		t.Errorf("authorization header not masked: %q", got)
	}
	if got := redactKey("X-Api-Key", "sk-ant-verysecretvalue"); strings.Contains(got, "verysecret") {
		t.Errorf("api key header not masked: %q", got)
	}
	if got := redactKey("Content-Type", "application/json"); got != "application/json" {
		t.Errorf("ordinary header should pass through, got %q", got)
	}
}

func TestSave(t *testing.T) {
	log := &HARLog{}
	tp := NewTransport(log)
	path := filepath.Join(t.TempDir(), "out.har")
	if err := tp.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	if doc["log"] == nil {
		t.Error("HAR has no log object")
	}
}

// Tool calls run concurrently, so several requests can be in flight at once.
func TestTransport_concurrentRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client, log := newRecordingClient(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(srv.URL)
			if err != nil {
				return
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()
	if len(log.Log.Entries) != 20 {
		t.Errorf("entries = %d, want 20", len(log.Log.Entries))
	}
}

// Redaction has to be wired into the recording path, not just exist as a
// function: the response body and headers used to be stored verbatim.
func TestTransport_redactsBothDirections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=supersecretcookievalue; Path=/")
		io.WriteString(w, `{"echo":"your key is sk-ant-supersecretvalue123"}`)
	}))
	defer srv.Close()

	client, log := newRecordingClient(t)
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"key":"sk-ant-requestsecret456"}`))
	req.Header.Set("X-Api-Key", "sk-ant-headersecret789")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	blob, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"sk-ant-requestsecret456",    // request body
		"sk-ant-headersecret789",     // request header
		"sk-ant-supersecretvalue123", // response body
		"supersecretcookievalue",     // response header
	} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("secret %q survived into the HAR log", secret)
		}
	}
}

// The whole log sits in memory until the process exits, so one large response
// must not pin an unbounded amount of heap.
func TestTransport_capsStoredBodySize(t *testing.T) {
	const size = 8 << 20 // 8 MiB, well over the cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, size))
	}))
	defer srv.Close()

	client, log := newRecordingClient(t)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	e := log.Log.Entries[0]
	if len(e.Response.Content.Text) > maxBodyBytes+200 {
		t.Errorf("stored %d bytes, cap is %d", len(e.Response.Content.Text), maxBodyBytes)
	}
	if e.Response.Content.Size != size {
		t.Errorf("Size = %d, want the true length %d", e.Response.Content.Size, size)
	}
	if !strings.Contains(e.Response.Content.Text, "truncated") {
		t.Error("truncation was not disclosed in the log")
	}
}
