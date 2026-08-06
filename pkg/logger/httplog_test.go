package logger

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hijackableRecorder is an httptest recorder that also satisfies http.Hijacker,
// so tests can exercise the upgrade/tunnel path.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijackCalled bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijackCalled = true
	return nil, nil, nil
}

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	req.RemoteAddr = "10.0.0.7:5555"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestAccessLog_LogsRequestLine(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf})

	handler := AccessLog(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(RequestIDHeader, "trace-abc") // set by the trace-id middleware in production
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	}))

	serve(handler, http.MethodPost, "/v1/chat/completions")

	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("want 1 access line, got %d: %q", len(lines), buf.String())
	}
	m := decode(t, lines[0])
	checks := map[string]any{
		"msg":      "http request",
		"level":    "INFO",
		"method":   "POST",
		"path":     "/v1/chat/completions",
		"trace_id": "trace-abc",
		"remote":   "10.0.0.7:5555",
	}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("field %q = %v, want %v", k, m[k], want)
		}
	}
	// JSON numbers decode to float64.
	if m["status"] != float64(http.StatusCreated) {
		t.Errorf("status = %v, want %d", m["status"], http.StatusCreated)
	}
	if m["bytes"] != float64(len("hello")) {
		t.Errorf("bytes = %v, want %d", m["bytes"], len("hello"))
	}
	if _, ok := m["duration_ms"]; !ok {
		t.Errorf("missing duration_ms: %v", m)
	}
}

func TestAccessLog_LevelByStatus(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusNotFound, "WARN"},
		{http.StatusTooManyRequests, "WARN"},
		{http.StatusBadGateway, "ERROR"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		l := New(Options{Level: "debug", Output: &buf})
		status := c.status
		h := AccessLog(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		serve(h, http.MethodGet, "/x")
		if got := decode(t, nonEmptyLines(buf.String())[0])["level"]; got != c.want {
			t.Errorf("status %d -> level %v, want %v", c.status, got, c.want)
		}
	}
}

func TestAccessLog_DefaultStatus200WhenHandlerNeverSetsIt(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf})
	h := AccessLog(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("body")) // implicit 200
	}))
	serve(h, http.MethodGet, "/x")
	m := decode(t, nonEmptyLines(buf.String())[0])
	if m["status"] != float64(http.StatusOK) {
		t.Errorf("implicit status = %v, want 200", m["status"])
	}
}

// TestStatusRecorder_PreservesFlusher proves the SSE-critical property: a
// handler can still flush through the wrapper via http.ResponseController, which
// relies on statusRecorder.Unwrap reaching httptest.ResponseRecorder's Flush.
func TestStatusRecorder_PreservesFlusher(t *testing.T) {
	rr := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rr, status: http.StatusOK}

	if _, err := sr.Write([]byte("chunk")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := http.NewResponseController(sr).Flush(); err != nil {
		t.Fatalf("flush through wrapper failed (SSE would break): %v", err)
	}
	if !rr.Flushed {
		t.Error("underlying recorder was not flushed")
	}
	if sr.bytes != int64(len("chunk")) {
		t.Errorf("bytes = %d, want %d", sr.bytes, len("chunk"))
	}
}

func TestStatusRecorder_IgnoresInformationalStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rr, status: http.StatusOK}
	sr.WriteHeader(http.StatusContinue) // 100 — must not be recorded as the status
	sr.WriteHeader(http.StatusAccepted) // 202 — the real one
	if sr.status != http.StatusAccepted {
		t.Errorf("status = %d, want 202 (100 should be ignored)", sr.status)
	}
}

// A panic below AccessLog must still yield an access line (as a 500) and the
// panic must keep propagating so the outer RecoverJSON handles it.
func TestAccessLog_EmitsLineOnPanic(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf})
	h := AccessLog(l)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic must propagate past AccessLog")
			}
		}()
		serve(h, http.MethodGet, "/boom")
	}()

	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("want 1 access line on panic, got %d: %q", len(lines), buf.String())
	}
	m := decode(t, lines[0])
	if m["status"] != float64(http.StatusInternalServerError) {
		t.Errorf("panic status = %v, want 500", m["status"])
	}
	if m["level"] != "ERROR" {
		t.Errorf("panic level = %v, want ERROR", m["level"])
	}
}

// A deliberate http.ErrAbortHandler must re-raise untouched and produce no line.
func TestAccessLog_AbortHandlerReRaisedWithoutLine(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf})
	h := AccessLog(l)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	func() {
		defer func() {
			if r := recover(); r != http.ErrAbortHandler {
				t.Fatalf("recovered = %v, want http.ErrAbortHandler", r)
			}
		}()
		serve(h, http.MethodGet, "/abort")
	}()

	if s := strings.TrimSpace(buf.String()); s != "" {
		t.Errorf("abort must not be access-logged, got: %s", s)
	}
}

// A hijacked (upgraded/tunneled) connection must not emit a misleading line.
func TestAccessLog_SkipsHijackedConnections(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf})
	h := AccessLog(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, _, err := http.NewResponseController(w).Hijack(); err != nil {
			t.Fatalf("hijack: %v", err)
		}
	}))

	under := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/realtime", nil)
	h.ServeHTTP(under, req)

	if !under.hijackCalled {
		t.Fatal("underlying Hijack was not reached through the wrapper")
	}
	if s := strings.TrimSpace(buf.String()); s != "" {
		t.Errorf("hijacked connection must not be access-logged, got: %s", s)
	}
}

// statusRecorder must observe a hijack while still delegating it downward.
func TestStatusRecorder_TracksAndDelegatesHijack(t *testing.T) {
	under := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	sr := &statusRecorder{ResponseWriter: under, status: http.StatusOK}

	if _, _, err := http.NewResponseController(sr).Hijack(); err != nil {
		t.Fatalf("hijack through wrapper: %v", err)
	}
	if !sr.hijacked {
		t.Error("statusRecorder did not record the hijack")
	}
	if !under.hijackCalled {
		t.Error("hijack was not delegated to the underlying writer")
	}
}
