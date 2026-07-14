package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/logging"
)

// captureLogs redirects the package logger for the duration of a test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := logging.Logger
	logging.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	t.Cleanup(func() { logging.Logger = prev })
	return &buf
}

func panicsAt(http.ResponseWriter, *http.Request) { panic("boom") }

func TestRecoverJSONLogsStackAndTraceID(t *testing.T) {
	logs := captureLogs(t)

	// logging.Middleware sets X-Request-ID on the shared ResponseWriter; that is
	// the only way the outermost recover can correlate the panic to the request.
	handler := RecoverJSON(logging.Middleware(http.HandlerFunc(panicsAt)))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil)
	req.Header.Set("X-Request-ID", "trace-abc123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "trace_id=trace-abc123") {
		t.Fatalf("panic log is missing the request trace_id: %s", out)
	}
	if !strings.Contains(out, "panicsAt") {
		t.Fatalf("panic log is missing a stack trace through the panicking frame: %s", out)
	}
	if got := w.Header().Get("X-Request-ID"); got != "trace-abc123" {
		t.Fatalf("X-Request-ID = %q, want trace-abc123", got)
	}
}

// A panic partway through a streamed response must not append an error envelope
// to bytes the client has already received.
func TestRecoverJSONDoesNotCorruptCommittedResponse(t *testing.T) {
	captureLogs(t)

	handler := RecoverJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chunk-1\"}\n\n"))
		panic("exploded mid-stream")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/stream", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want the already-sent 200", w.Code)
	}
	body := w.Body.String()
	if body != "data: {\"id\":\"chunk-1\"}\n\n" {
		t.Fatalf("recovery appended bytes to a committed response: %q", body)
	}
	if strings.Contains(body, "internal_error") {
		t.Fatal("error envelope was appended to an in-flight stream")
	}
}

// net/http allows any number of 1xx informational headers before the single
// 2xx-5xx one, so only a final status commits the response.
func TestCommittedWriterIgnoresInformationalStatus(t *testing.T) {
	cw := &committedWriter{ResponseWriter: httptest.NewRecorder()}

	cw.WriteHeader(http.StatusEarlyHints) // 103
	if cw.committed {
		t.Fatal("a 1xx informational response must not commit the response")
	}
	cw.WriteHeader(http.StatusOK)
	if !cw.committed {
		t.Fatal("a final status must commit the response")
	}
}

// httputil.ReverseProxy forwards an upstream 1xx straight through WriteHeader.
// Treating it as committed would leave a panicking request with no final
// response at all — the client would hang on a bare 103.
//
// httptest.ResponseRecorder latches Code on the first WriteHeader and cannot
// model 1xx, so this runs against a real server.
func TestRecoverJSONWritesEnvelopeAfterInformationalResponse(t *testing.T) {
	captureLogs(t)

	server := httptest.NewServer(RecoverJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "</style.css>; rel=preload; as=style")
		w.WriteHeader(http.StatusEarlyHints) // 103, informational only
		panic("exploded after early hints")
	})))
	defer server.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 after a 103", resp.StatusCode)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "internal_error" {
		t.Fatalf("error code = %q, want internal_error", payload.Error.Code)
	}
}

// The ordering contract: RecoverJSON sits above logging (and tracing), so a
// panic raised inside those layers still produces the JSON envelope.
func TestRecoverJSONRecoversPanicRaisedInsideLoggingLayer(t *testing.T) {
	captureLogs(t)

	panicInLoggingLayer := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			_ = next
			panic("middleware exploded")
		})
	}
	handler := RecoverJSON(panicInLoggingLayer(logging.Middleware(http.HandlerFunc(panicsAt))))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestRecoverJSONReturnsErrorEnvelope(t *testing.T) {
	handler := RecoverJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "internal_error" || payload.Error.Type != "server_error" {
		t.Fatalf("error = %#v, want internal_error/server_error", payload.Error)
	}
	if strings.Contains(payload.Error.Message, "boom") {
		t.Fatalf("panic detail leaked in response message: %q", payload.Error.Message)
	}
}

func TestRecoverJSONRepanicsAbortHandler(t *testing.T) {
	handler := RecoverJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/abort", nil)
	w := httptest.NewRecorder()

	defer func() {
		recovered := recover()
		if recovered != http.ErrAbortHandler {
			t.Fatalf("recovered = %v, want http.ErrAbortHandler", recovered)
		}
		if w.Body.Len() != 0 {
			t.Fatalf("response body should stay empty, got %q", w.Body.String())
		}
	}()
	handler.ServeHTTP(w, req)
}
