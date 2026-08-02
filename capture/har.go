// Package capture wraps the HTTP transport to record every request/response
// pair (including streaming SSE) in HAR (HTTP Archive) format.
//
// Enable via AGENT_HAR_FILE=/path/to/capture.har
package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// HARLog is the top-level HAR document.
type HARLog struct {
	Log struct {
		Version string     `json:"version"`
		Creator Creator    `json:"creator"`
		Entries []HAREntry `json:"entries"`
	} `json:"log"`
}

type Creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// HAREntry is one request/response pair.
type HAREntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	TimeMs          int64       `json:"time"`
	Request         HARRequest  `json:"request"`
	Response        HARResponse `json:"response"`
}

type HARRequest struct {
	Method   string       `json:"method"`
	URL      string       `json:"url"`
	Headers  []HARHeader  `json:"headers"`
	PostData *HARPostData `json:"postData,omitempty"`
}

type HARResponse struct {
	Status  int         `json:"status"`
	Headers []HARHeader `json:"headers"`
	Content HARContent  `json:"content"`
}

type HARHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type HARPostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type HARContent struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

// Transport wraps an http.RoundTripper and records every request/response
// into a HAR log. Safe for concurrent use.
type Transport struct {
	Base http.RoundTripper
	Log  *HARLog
	Mu   sync.Mutex
}

// NewTransport creates a capture transport over the given base (defaults to
// http.DefaultTransport). The caller owns log and should call Save() at exit.
func NewTransport(log *HARLog) *Transport {
	t := &Transport{Base: http.DefaultTransport, Log: log}
	t.Log.Log.Version = "1.2"
	t.Log.Log.Creator = Creator{Name: "agentic", Version: "0.1"}
	return t
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	entry := HAREntry{StartedDateTime: time.Now().UTC().Format(time.RFC3339)}
	start := time.Now()

	// --- capture request ---
	entry.Request.Method = req.Method
	entry.Request.URL = req.URL.String()
	// capture request headers
	for name, vals := range req.Header {
		for _, v := range vals {
			entry.Request.Headers = append(entry.Request.Headers, HARHeader{
				Name: name, Value: redactKey(name, v),
			})
		}
	}
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		entry.Request.PostData = &HARPostData{
			MimeType: req.Header.Get("Content-Type"),
			Text:     redactBody(string(body)),
		}
	}

	// --- execute ---
	resp, err := t.Base.RoundTrip(req)
	entry.TimeMs = time.Since(start).Milliseconds()

	if err != nil {
		entry.Response.Status = 0
		entry.Response.Content.Text = fmt.Sprintf("error: %v", err)
		t.Mu.Lock()
		t.Log.Log.Entries = append(t.Log.Log.Entries, entry)
		t.Mu.Unlock()
		return nil, err
	}

	// --- capture response headers ---
	entry.Response.Status = resp.StatusCode
	for name, vals := range resp.Header {
		for _, v := range vals {
			entry.Response.Headers = append(entry.Response.Headers, HARHeader{Name: name, Value: v})
		}
	}
	entry.Response.Content.MimeType = resp.Header.Get("Content-Type")

	// Wrap the response body so every byte is captured, including streaming SSE.
	rec := &recordingBody{
		source: resp.Body,
		entry:  &entry,
		tp:     t,
	}
	resp.Body = rec
	return resp, nil
}

// recordingBody captures all bytes read from the response body and writes
// the complete HAR entry to the log when the body is closed. This works
// for both regular and streaming (SSE) responses.
type recordingBody struct {
	source io.ReadCloser
	buf    bytes.Buffer
	entry  *HAREntry
	tp     *Transport
}

func (r *recordingBody) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n > 0 {
		r.buf.Write(p[:n])
	}
	return n, err
}

func (r *recordingBody) Close() error {
	// drain any remaining bytes
	io.Copy(&r.buf, r.source)
	err := r.source.Close()

	r.entry.Response.Content.Size = r.buf.Len()
	r.entry.Response.Content.Text = r.buf.String()

	r.tp.Mu.Lock()
	r.tp.Log.Log.Entries = append(r.tp.Log.Log.Entries, *r.entry)
	r.tp.Mu.Unlock()
	return err
}

// Save writes the HAR log to a file as JSON.
func (t *Transport) Save(path string) error {
	t.Mu.Lock()
	defer t.Mu.Unlock()
	data, err := json.MarshalIndent(t.Log, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// redactBody scans text for common API key patterns and masks them.
func redactBody(text string) string {
	patterns := []string{
		`sk-[a-zA-Z0-9-_]{8,}`,
		`sk-ant-[a-zA-Z0-9-_]{8,}`,
		`gh[pousr]_[a-zA-Z0-9]{8,}`,
		`Bearer [a-zA-Z0-9._-]{8,}`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		text = re.ReplaceAllStringFunc(text, func(m string) string {
			if len(m) > 8 {
				return m[:4] + "***" + m[len(m)-2:]
			}
			return "***"
		})
	}
	return text
}

// redactKey masks API keys in header values.
func redactKey(name, value string) string {
	switch strings.ToLower(name) {
	case "x-api-key", "authorization":
		if len(value) > 8 {
			return value[:4] + "…" + value[len(value)-4:]
		}
		return "***"
	}
	return value
}
