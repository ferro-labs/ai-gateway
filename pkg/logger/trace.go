package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

type contextKey int

const traceIDKey contextKey = iota

// NewTraceID generates a random 16-byte hex trace ID.
func NewTraceID() string {
	b := make([]byte, 16)
	// crypto/rand.Read never returns an error on Go >= 1.20 (it panics
	// internally on OS RNG failure), so the result is safe to use directly.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithTraceID stores a trace ID in the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext retrieves the trace ID stored in the context, or "" when
// none is present.
func TraceIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(traceIDKey).(string)
	return v
}

// canonicalTraceID reports whether id is usable as a trace ID and returns it in
// the one form every consumer accepts: 32 lowercase hex characters, not all
// zero.
//
// The acceptance rule is internal/otel's, not a new one. Its IDGenerator parses
// the context trace ID with trace.TraceIDFromHex (32 chars, hex, rejecting the
// all-zero ID) and mints a random trace ID whenever that fails — so an id this
// rejects is precisely an id the OTel trace_id would NOT have equalled. Adopting
// anything looser is what split the id space: a client sending a UUID as
// X-Request-ID got that UUID in its logs and response header while OTel reported
// an unrelated trace_id, contradicting the unification the two packages both
// document.
//
// Uppercase hex is accepted and folded to lowercase rather than replaced: OTel
// already lowercases before parsing, so such an id does identify the same trace
// and only the echoed header disagreed on case. Folding makes the four values
// byte-identical; minting a fresh one would throw the caller's correlation id
// away for a difference that is not one.
//
// The scan is written out rather than handed to encoding/hex because
// hex.DecodeString allocates and this runs on every routed request; an
// already-canonical id costs nothing.
func canonicalTraceID(id string) (string, bool) {
	if len(id) != 32 {
		return "", false
	}
	allZero, hasUpper := true, false
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
			hasUpper = true
		default:
			return "", false
		}
		if id[i] != '0' {
			allZero = false
		}
	}
	if allZero {
		return "", false
	}
	if hasUpper {
		return strings.ToLower(id), true
	}
	return id, true
}

// EnsureTraceID returns a context carrying a canonical trace ID: the one it
// already holds when that is usable, otherwise a freshly minted one. ctx is
// returned untouched — no allocation — when it already carries a canonical ID,
// which is the case for every request that came through Middleware.
//
// Call it at each entry point that needs a trace ID, not just in HTTP
// middleware. Gateway.Route, RouteStream, Embed and GenerateImage are all
// exported and are called directly by embedders, and every one of them read the
// context trace ID straight into a span, a request-log row and a lifecycle
// event — so without an HTTP layer above them the whole request family was
// recorded under an empty trace ID and nothing could be correlated.
func EnsureTraceID(ctx context.Context) context.Context {
	id := TraceIDFromContext(ctx)
	canonical, ok := canonicalTraceID(id)
	switch {
	case ok && canonical == id:
		return ctx
	case ok:
		return WithTraceID(ctx, canonical)
	default:
		return WithTraceID(ctx, NewTraceID())
	}
}

// Ctx returns a logger annotated with the request trace_id from ctx (returned
// unchanged when the context carries none).
func (l *Logger) Ctx(ctx context.Context) *Logger {
	if id := TraceIDFromContext(ctx); id != "" {
		return l.With("trace_id", id)
	}
	return l
}

// Ctx is a package-level convenience for Default().Ctx(ctx), so request-scoped
// code can log with the trace_id attached without holding a Logger reference.
func Ctx(ctx context.Context) *Logger { return Default().Ctx(ctx) }

// Middleware injects a trace ID into every request context and echoes it in the
// X-Request-ID response header. Resolution precedence:
//  1. A trace ID already on the request context (e.g. seeded by
//     internal/otel.Middleware after extracting an inbound W3C traceparent).
//  2. The inbound X-Request-ID header.
//  3. A freshly generated 16-byte hex ID.
//
// Whichever wins is put through EnsureTraceID, so an inbound header that cannot
// be a trace ID — a UUID, a slug, an opaque request id from an upstream proxy —
// is replaced rather than adopted. Adopting it kept the client's string in the
// logs and the response header while OTel, which cannot parse it, reported a
// different trace_id: one request, two ids, correlated by neither.
//
// This precedence keeps the OTel trace_id, the logging trace ID, the
// X-Request-ID response header, and the ferro.gateway.trace_id span attribute
// equal per request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if TraceIDFromContext(ctx) == "" {
			ctx = WithTraceID(ctx, r.Header.Get(RequestIDHeader))
		}
		ctx = EnsureTraceID(ctx)
		w.Header().Set(RequestIDHeader, TraceIDFromContext(ctx))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
