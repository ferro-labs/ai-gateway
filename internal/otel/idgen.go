package otel

import (
	"context"
	"crypto/rand"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// loggingIDGen is a custom OTel IDGenerator that unifies the OTel trace_id
// with the logging trace ID already present in the context.
//
// When logging middleware has already seeded a trace ID into the context
// (via logging.WithTraceID), the generator parses that 32-hex-char string
// directly into an OTel trace.TraceID — ensuring that the OTel trace_id,
// the ferro.gateway.trace_id span attribute, the X-Request-ID response
// header, and the structured-log trace_id field are all byte-identical for
// every request.
//
// Fallback behaviour: when no logging trace ID is present in the context
// (e.g. an embedder calling Gateway.Route without the logging middleware),
// a cryptographically random trace ID is generated. The span remains
// internally consistent; it is just not linked to a logging trace ID.
//
// Concurrency: loggingIDGen has no shared mutable state. crypto/rand.Reader
// is safe for concurrent use.
type loggingIDGen struct{}

// Compile-time guard.
var _ sdktrace.IDGenerator = (*loggingIDGen)(nil)

// newLoggingIDGen constructs a loggingIDGen.
func newLoggingIDGen() *loggingIDGen {
	return &loggingIDGen{}
}

// NewIDs returns a trace ID derived from the logging trace ID in ctx (when
// valid) plus a random span ID.
func (g *loggingIDGen) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	tid := traceIDFromLogging(ctx)
	sid := randomSpanID()
	return tid, sid
}

// NewSpanID returns a random, non-zero span ID. The traceID parameter is
// unused; it is part of the IDGenerator interface contract.
func (g *loggingIDGen) NewSpanID(_ context.Context, _ trace.TraceID) trace.SpanID {
	return randomSpanID()
}

// traceIDFromLogging attempts to parse the logging trace ID from ctx into
// a trace.TraceID. Returns a random non-zero TraceID on any failure
// (missing, malformed, or wrong length).
//
// The raw ID is lowercased before parsing: X-Request-ID headers may arrive in
// uppercase hex (e.g. "0AF7651916CD43DD8448EB211C80319C"), which
// trace.TraceIDFromHex rejects. Lowercasing here keeps OTel and logging trace
// IDs in sync without modifying logging.Middleware.
//
// trace.TraceIDFromHex already returns an error for the all-zero ID, so no
// additional IsValid() guard is needed after a successful parse.
func traceIDFromLogging(ctx context.Context) trace.TraceID {
	tid := logging.TraceIDFromContext(ctx)
	if len(tid) == 32 {
		parsed, err := trace.TraceIDFromHex(strings.ToLower(tid))
		if err == nil {
			return parsed
		}
	}
	return randomTraceID()
}

// randomTraceID generates a cryptographically random, non-zero trace ID.
func randomTraceID() trace.TraceID {
	var tid trace.TraceID
	for {
		// crypto/rand.Read never returns an error on Go ≥1.20 (it panics
		// internally on OS RNG failure), so the result is safe to discard.
		_, _ = rand.Read(tid[:])
		if tid.IsValid() {
			return tid
		}
	}
}

// randomSpanID generates a cryptographically random, non-zero span ID.
func randomSpanID() trace.SpanID {
	var sid trace.SpanID
	for {
		// crypto/rand.Read never returns an error on Go ≥1.20 (it panics
		// internally on OS RNG failure), so the result is safe to discard.
		_, _ = rand.Read(sid[:])
		if sid.IsValid() {
			return sid
		}
	}
}
