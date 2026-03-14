package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetup_DoesNotPanic(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "unknown"} {
		for _, format := range []string{"json", "text", ""} {
			Setup(level, format)
			if Logger == nil {
				t.Errorf("Logger is nil after Setup(%q, %q)", level, format)
			}
		}
	}
	Setup("info", "json")
}

func TestNewTraceID_Format(t *testing.T) {
	id := NewTraceID()
	if len(id) != 32 {
		t.Errorf("expected 32-char hex trace ID, got %d chars: %q", len(id), id)
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("trace ID contains non-hex char %q: %q", c, id)
		}
	}
}

func TestNewTraceID_Uniqueness(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := NewTraceID()
		if _, dup := ids[id]; dup {
			t.Errorf("duplicate trace ID on iteration %d: %q", i, id)
		}
		ids[id] = struct{}{}
	}
}

func TestWithTraceID_RoundTrip(t *testing.T) {
	ctx := context.Background()
	const want = "abc123"
	ctx = WithTraceID(ctx, want)
	if got := TraceIDFromContext(ctx); got != want {
		t.Errorf("TraceIDFromContext = %q, want %q", got, want)
	}
}

func TestTraceIDFromContext_EmptyOnMissing(t *testing.T) {
	if got := TraceIDFromContext(context.Background()); got != "" {
		t.Errorf("expected empty string on missing trace ID, got %q", got)
	}
}

func TestFromContext_WithTraceID(t *testing.T) {
	Setup("info", "json")
	ctx := WithTraceID(context.Background(), "trace-42")
	if FromContext(ctx) == nil {
		t.Fatal("FromContext returned nil logger")
	}
}

func TestFromContext_WithoutTraceID(t *testing.T) {
	Setup("info", "json")
	if FromContext(context.Background()) == nil {
		t.Fatal("FromContext returned nil logger")
	}
}

func TestMiddleware_PropagatesProvidedHeader(t *testing.T) {
	const traceID = "my-trace-id"
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := TraceIDFromContext(r.Context())
		if got != traceID {
			t.Errorf("context trace ID = %q, want %q", got, traceID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", traceID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-ID"); got != traceID {
		t.Errorf("response X-Request-ID = %q, want %q", got, traceID)
	}
}

func TestMiddleware_GeneratesHeaderWhenMissing(t *testing.T) {
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TraceIDFromContext(r.Context()) == "" {
			t.Error("expected non-empty trace ID in context when header absent")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("X-Request-ID") == "" {
		t.Error("expected non-empty X-Request-ID response header")
	}
}
