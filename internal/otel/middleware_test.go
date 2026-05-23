package otel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/logging"
)

// TestMiddlewareExtractsTraceparent verifies that a valid inbound W3C
// traceparent is parsed and its trace_id seeded into the logging
// context for the downstream handler, satisfying the trace-ID
// unification contract.
func TestMiddlewareExtractsTraceparent(t *testing.T) {
	installPropagator() // Init normally installs this; tests call it directly.

	const (
		wantTraceID = "0af7651916cd43dd8448eb211c80319c"
		traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	)

	var seen string
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("traceparent", traceparent)
	h.ServeHTTP(rr, req)

	if seen != wantTraceID {
		t.Fatalf("trace id in context = %q, want %q", seen, wantTraceID)
	}
}

// TestMiddlewareNoTraceparentLeavesContextEmpty confirms that absent
// inbound traceparent we do not invent a trace ID — logging.Middleware
// handles generation downstream.
func TestMiddlewareNoTraceparentLeavesContextEmpty(t *testing.T) {
	installPropagator()

	var seen string
	h := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = logging.TraceIDFromContext(r.Context())
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rr, req)

	if seen != "" {
		t.Fatalf("expected empty trace id, got %q", seen)
	}
}
