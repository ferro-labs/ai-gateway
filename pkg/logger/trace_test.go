package logger

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// otelAcceptable mirrors internal/otel/idgen.go's acceptance rule: the OTel
// IDGenerator parses the context trace ID with trace.TraceIDFromHex after
// lowercasing it, and mints a random trace ID whenever that fails. It is
// restated here rather than imported because pkg/ must not depend on internal/.
//
// An id this rejects is an id whose OTel trace_id would NOT have matched the
// logging trace ID — which is the whole defect EnsureTraceID exists to close.
func otelAcceptable(id string) bool {
	if len(id) != 32 {
		return false
	}
	lowered := strings.ToLower(id)
	allZero := true
	for i := 0; i < len(lowered); i++ {
		c := lowered[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
		if c != '0' {
			allZero = false
		}
	}
	return !allZero
}

func TestEnsureTraceID(t *testing.T) {
	const canonical = "0af7651916cd43dd8448eb211c80319c"
	tests := []struct {
		name   string
		seed   string
		want   string // "" means "any freshly minted canonical ID"
		minted bool
	}{
		{name: "canonical id is kept and the context is not rebuilt", seed: canonical, want: canonical},
		{name: "uppercase hex is folded, not thrown away", seed: strings.ToUpper(canonical), want: canonical},
		{name: "uuid is replaced", seed: "0af76519-16cd-43dd-8448-eb211c80319c", minted: true},
		{name: "opaque proxy id is replaced", seed: "req_01HZX3QK4V", minted: true},
		{name: "too short is replaced", seed: "0af7651916cd43dd", minted: true},
		{name: "non-hex of the right length is replaced", seed: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", minted: true},
		{name: "all-zero is replaced — OTel rejects it too", seed: strings.Repeat("0", 32), minted: true},
		{name: "absent is minted", seed: "", minted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.seed != "" {
				ctx = WithTraceID(ctx, tt.seed)
			}
			got := TraceIDFromContext(EnsureTraceID(ctx))

			if !otelAcceptable(got) {
				t.Fatalf("EnsureTraceID left %q, which internal/otel would refuse — the OTel trace_id "+
					"and the logging trace ID would then differ for this request", got)
			}
			if tt.minted {
				if got == tt.seed {
					t.Errorf("adopted %q verbatim; it cannot be an OTel trace_id, so the id space stays split", tt.seed)
				}
				return
			}
			if got != tt.want {
				t.Errorf("EnsureTraceID = %q, want %q", got, tt.want)
			}
		})
	}
}

// An already-canonical context must be returned untouched. Route, RouteStream,
// Embed and GenerateImage all call this on every request, so rebuilding the
// context each time would put an allocation on the hot path for nothing.
func TestEnsureTraceID_CanonicalContextIsNotRebuilt(t *testing.T) {
	ctx := WithTraceID(context.Background(), "0af7651916cd43dd8448eb211c80319c")
	if EnsureTraceID(ctx) != ctx {
		t.Error("EnsureTraceID rebuilt a context that already carried a canonical trace ID")
	}
}

// Middleware must apply the same rule, so the echoed X-Request-ID is always an
// id the rest of the stack can agree on.
func TestMiddleware_CanonicalisesInboundRequestID(t *testing.T) {
	const canonical = "0af7651916cd43dd8448eb211c80319c"
	tests := []struct {
		name   string
		header string
		want   string // "" means "must not be the header value"
	}{
		{name: "canonical header is echoed", header: canonical, want: canonical},
		{name: "uppercase header is echoed folded", header: strings.ToUpper(canonical), want: canonical},
		{name: "uuid header is not adopted", header: "0af76519-16cd-43dd-8448-eb211c80319c"},
		{name: "no header at all", header: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var inContext string
			h := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				inContext = TraceIDFromContext(r.Context())
			}))

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/models", nil)
			if tt.header != "" {
				req.Header.Set(RequestIDHeader, tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			echoed := rec.Header().Get(RequestIDHeader)
			if echoed != inContext {
				t.Errorf("echoed %q but the handler saw %q; the response header must name the trace the logs use", echoed, inContext)
			}
			if !otelAcceptable(echoed) {
				t.Fatalf("echoed %q, which internal/otel would refuse", echoed)
			}
			if tt.want != "" && echoed != tt.want {
				t.Errorf("echoed %q, want %q", echoed, tt.want)
			}
			if tt.want == "" && tt.header != "" && echoed == tt.header {
				t.Errorf("echoed the client's %q verbatim; OTel cannot parse it, so trace_id would not match", tt.header)
			}
		})
	}
}

// A trace ID already on the context — internal/otel.Middleware seeds one from an
// inbound W3C traceparent — still outranks the X-Request-ID header.
func TestMiddleware_ContextTraceIDOutranksHeader(t *testing.T) {
	const fromTraceparent = "4bf92f3577b34da6a3ce929d0e0e4736"

	h := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/models", nil)
	req.Header.Set(RequestIDHeader, "00000000000000000000000000000001")
	req = req.WithContext(WithTraceID(req.Context(), fromTraceparent))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != fromTraceparent {
		t.Errorf("X-Request-ID = %q, want the traceparent-derived %q", got, fromTraceparent)
	}
}
