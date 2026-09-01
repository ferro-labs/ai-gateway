package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// deterministic is a mock with the demo randomness switched off.
func deterministic() settings {
	return settings{name: "alpha"}
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newHandler(deterministic()))
	t.Cleanup(srv.Close)
	return srv
}

// reply is one answer from the mock, fully read so no body is left open.
type reply struct {
	status int
	header http.Header
	body   []byte
}

// call sends one request to the mock and returns its answer.
func call(t *testing.T, srv *httptest.Server, method, path, body string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: data}
}

func setScenario(t *testing.T, srv *httptest.Server, body string) {
	t.Helper()
	if r := call(t, srv, http.MethodPost, "/_mock/scenario", body); r.status != http.StatusNoContent {
		t.Fatalf("set scenario: status %d", r.status)
	}
}

func completion(t *testing.T, srv *httptest.Server, body string) reply {
	t.Helper()
	return call(t, srv, http.MethodPost, "/openai/v1/chat/completions", body)
}

func calls(t *testing.T, srv *httptest.Server) map[string]any {
	t.Helper()
	return decode(t, bytes.NewReader(call(t, srv, http.MethodGet, "/_mock/calls", "").body))
}

func decode(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestChat_HealthyIsDeterministicAndNamesTheInstance(t *testing.T) {
	srv := newTestServer(t)

	r := completion(t, srv, `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.status)
	}
	body := decode(t, bytes.NewReader(r.body))
	if body["model"] != "m1" {
		t.Errorf("model = %v, want the requested m1", body["model"])
	}
	content := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "ok from alpha" {
		t.Errorf("content = %q, want %q", content, "ok from alpha")
	}
}

func TestScenario_FailsTheNextNRequestsThenHeals(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"status":503,"fail_count":2}`)

	got := make([]int, 0, 3)
	for range 3 {
		got = append(got, completion(t, srv, `{"model":"m1"}`).status)
	}
	if want := []int{503, 503, 200}; !equalInts(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

func TestScenario_FailsEveryRequestUntilCleared(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"status":500}`)

	for range 3 {
		if code := completion(t, srv, `{"model":"m1"}`).status; code != 500 {
			t.Fatalf("status = %d, want 500 while the scenario stands", code)
		}
	}
	if r := call(t, srv, http.MethodDelete, "/_mock/scenario", ""); r.status != http.StatusNoContent {
		t.Fatalf("clear scenario: status %d", r.status)
	}
	if code := completion(t, srv, `{"model":"m1"}`).status; code != 200 {
		t.Errorf("status after clearing = %d, want 200", code)
	}
}

func TestScenario_RateLimitCarriesRetryAfter(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"status":429,"retry_after_s":7}`)

	r := completion(t, srv, `{"model":"m1"}`)
	if r.status != 429 || r.header.Get("Retry-After") != "7" {
		t.Errorf("status/Retry-After = %d/%q, want 429/7", r.status, r.header.Get("Retry-After"))
	}
}

func TestScenario_DelayIsApplied(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"delay_ms":120}`)

	start := time.Now()
	completion(t, srv, `{"model":"m1"}`)
	if took := time.Since(start); took < 100*time.Millisecond {
		t.Errorf("request took %v, want the 120ms delay", took)
	}
}

func TestScenario_HangHoldsUntilTheClientLeaves(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"hang":true}`)

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m1"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a hung upstream answered")
	}
	// The handler must let go once the client is gone; a leaked goroutine
	// would keep srv.Close from returning, which the test would report as a hang.
}

func TestStream_HealthyEndsWithUsageAndDone(t *testing.T) {
	srv := newTestServer(t)

	r := completion(t, srv, `{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	frames := readFrames(t, bytes.NewReader(r.body))
	if last := frames[len(frames)-1]; last != "[DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", last)
	}
	var content strings.Builder
	sawUsage := false
	for _, frame := range frames[:len(frames)-1] {
		chunk := decode(t, strings.NewReader(frame))
		if chunk["model"] != "m1" {
			t.Errorf("chunk model = %v, want m1", chunk["model"])
		}
		if usage, ok := chunk["usage"].(map[string]any); ok && usage["total_tokens"].(float64) > 0 {
			sawUsage = true
		}
		for _, c := range chunk["choices"].([]any) {
			if delta, ok := c.(map[string]any)["delta"].(map[string]any); ok {
				if s, ok := delta["content"].(string); ok {
					content.WriteString(s)
				}
			}
		}
	}
	if content.String() != "ok from alpha" {
		t.Errorf("streamed content = %q, want %q", content.String(), "ok from alpha")
	}
	if !sawUsage {
		t.Error("no chunk carried usage")
	}
}

func TestStream_FailsAfterNChunksWithAnErrorFrame(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"stream_fail_after":2}`)

	frames := readFrames(t, bytes.NewReader(completion(t, srv, `{"model":"m1","stream":true}`).body))
	if len(frames) != 3 {
		t.Fatalf("frames = %d (%v), want 2 chunks and one error frame", len(frames), frames)
	}
	last := decode(t, strings.NewReader(frames[2]))
	if _, ok := last["error"].(map[string]any); !ok {
		t.Errorf("third frame = %s, want an error envelope", frames[2])
	}
	for _, frame := range frames {
		if frame == "[DONE]" {
			t.Error("a failed stream must not end with [DONE]")
		}
	}
}

func TestCalls_CountRequestsAndRememberModels(t *testing.T) {
	srv := newTestServer(t)
	completion(t, srv, `{"model":"m1"}`)
	completion(t, srv, `{"model":"m2","stream":true}`)
	call(t, srv, http.MethodPost, "/v1/embeddings", `{"model":"e1","input":"x"}`)

	got := calls(t, srv)
	if got["calls"].(float64) != 3 || got["streams"].(float64) != 1 || got["last_model"] != "e1" {
		t.Errorf("calls = %v, want calls=3 streams=1 last_model=e1", got)
	}
	if got["models"].(map[string]any)["m1"].(float64) != 1 {
		t.Errorf("models = %v, want m1 counted once", got["models"])
	}

	if r := call(t, srv, http.MethodPost, "/_mock/reset", ""); r.status != http.StatusNoContent {
		t.Fatalf("reset: status %d", r.status)
	}
	if after := calls(t, srv); after["calls"].(float64) != 0 {
		t.Errorf("calls after reset = %v, want 0", after["calls"])
	}
}

func TestEmbeddings_ObeyTheScenario(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"status":502}`)

	if r := call(t, srv, http.MethodPost, "/v1/embeddings", `{"model":"e1","input":"x"}`); r.status != 502 {
		t.Errorf("status = %d, want 502", r.status)
	}
}

// readFrames returns the payload of every "data:" line, in order.
func readFrames(t *testing.T, r io.Reader) []string {
	t.Helper()
	var frames []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if payload, ok := bytes.CutPrefix(scanner.Bytes(), []byte("data: ")); ok {
			frames = append(frames, string(payload))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("stream carried no data frames")
	}
	return frames
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
