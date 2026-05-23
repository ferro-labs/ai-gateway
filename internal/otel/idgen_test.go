package otel

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/observability"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// knownTraceHex is a valid 32-char hex trace ID used across several tests.
const knownTraceHex = "0af7651916cd43dd8448eb211c80319c"

// newTestProviderWithIDGen is like newTestProvider but wires in the
// loggingIDGen so IDGenerator-specific tests use the real production path.
func newTestProviderWithIDGen(t *testing.T) (*otelProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithIDGenerator(newLoggingIDGen()),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return newProvider(tp, DefaultConfig()), exp
}

// --- Unit tests for loggingIDGen ---

func TestIDGen_NewIDs_ValidLoggingTraceID(t *testing.T) {
	ctx := logging.WithTraceID(context.Background(), knownTraceHex)
	gen := newLoggingIDGen()

	gotTraceID, gotSpanID := gen.NewIDs(ctx)

	want, _ := trace.TraceIDFromHex(knownTraceHex)
	if gotTraceID != want {
		t.Errorf("TraceID = %s, want %s", gotTraceID, want)
	}
	if !gotSpanID.IsValid() {
		t.Error("SpanID should be non-zero")
	}
}

func TestIDGen_NewIDs_UppercaseHex_Adopted(t *testing.T) {
	// X-Request-ID headers may carry uppercase hex; the generator must
	// lowercase-normalise and adopt the ID rather than fall back to random.
	upperHex := "0AF7651916CD43DD8448EB211C80319C"
	ctx := logging.WithTraceID(context.Background(), upperHex)
	gen := newLoggingIDGen()

	gotTraceID, gotSpanID := gen.NewIDs(ctx)

	// The returned trace ID must equal the lowercase-parsed form of upperHex.
	want, err := trace.TraceIDFromHex(knownTraceHex) // knownTraceHex is the lowercase form
	if err != nil {
		t.Fatalf("unexpected error parsing knownTraceHex: %v", err)
	}
	if gotTraceID != want {
		t.Errorf("TraceID = %s, want %s (uppercase input should be adopted after lowercasing)", gotTraceID, want)
	}
	if !gotSpanID.IsValid() {
		t.Error("SpanID should be non-zero")
	}
}

func TestIDGen_NewIDs_EmptyContext_FallsBackToRandom(t *testing.T) {
	gen := newLoggingIDGen()

	tid, sid := gen.NewIDs(context.Background())

	if !tid.IsValid() {
		t.Error("fallback TraceID should be non-zero")
	}
	if !sid.IsValid() {
		t.Error("fallback SpanID should be non-zero")
	}
	// Should not match the zero ID.
	var zero trace.TraceID
	if tid == zero {
		t.Error("fallback TraceID must not be the zero value")
	}
}

func TestIDGen_NewIDs_MalformedHex_FallsBackToRandom(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"wrong length short", "0af7651916cd43dd"},
		{"wrong length long", "0af7651916cd43dd8448eb211c80319cXXXX"},
		{"non-hex chars", "0af7651916cd43dd8448eb211c80319z"},
		{"all zero", "00000000000000000000000000000000"},
	}

	gen := newLoggingIDGen()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := logging.WithTraceID(context.Background(), tc.id)
			tid, sid := gen.NewIDs(ctx)

			if !tid.IsValid() {
				t.Errorf("tid should be non-zero for input %q", tc.id)
			}
			if !sid.IsValid() {
				t.Errorf("sid should be non-zero for input %q", tc.id)
			}
		})
	}
}

func TestIDGen_NewSpanID_NonZeroAndVaries(t *testing.T) {
	gen := newLoggingIDGen()
	var dummyTraceID trace.TraceID

	sid1 := gen.NewSpanID(context.Background(), dummyTraceID)
	sid2 := gen.NewSpanID(context.Background(), dummyTraceID)

	if !sid1.IsValid() {
		t.Error("sid1 should be non-zero")
	}
	if !sid2.IsValid() {
		t.Error("sid2 should be non-zero")
	}
	if sid1 == sid2 {
		t.Error("successive span IDs should differ (statistical; retries if flaky)")
	}
}

// --- Integration test: end-to-end unification ---

// TestIDGen_EndToEnd_TraceIDUnification verifies the core unification
// invariant: when a logging trace ID is seeded into the context, the OTel
// span's trace_id and the ferro.gateway.trace_id span attribute are all equal
// to that seeded value.
func TestIDGen_EndToEnd_TraceIDUnification(t *testing.T) {
	prov, exp := newTestProviderWithIDGen(t)

	// Seed a known logging trace ID into the context.
	ctx := logging.WithTraceID(context.Background(), knownTraceHex)

	_, span := prov.StartRequestSpan(ctx, observability.RequestAttrs{
		System:    "openai",
		Operation: "chat",
		TraceID:   knownTraceHex,
	})
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	got := spans[0]

	// 1. OTel span trace_id must equal the seeded logging ID.
	wantTraceID, _ := trace.TraceIDFromHex(knownTraceHex)
	if got.SpanContext.TraceID() != wantTraceID {
		t.Errorf("span trace_id = %s, want %s",
			got.SpanContext.TraceID(), wantTraceID)
	}

	// 2. ferro.gateway.trace_id attribute must also equal the seeded ID.
	attrMap := make(map[string]string, len(got.Attributes))
	for _, kv := range got.Attributes {
		attrMap[string(kv.Key)] = kv.Value.AsString()
	}
	if attrMap[observability.AttrFerroGatewayTraceID] != knownTraceHex {
		t.Errorf("ferro.gateway.trace_id attr = %q, want %q",
			attrMap[observability.AttrFerroGatewayTraceID], knownTraceHex)
	}

	// 3. The span trace_id hex must equal the attribute value.
	if got.SpanContext.TraceID().String() != knownTraceHex {
		t.Errorf("span.TraceID().String() = %q, want %q",
			got.SpanContext.TraceID().String(), knownTraceHex)
	}
}

// TestIDGen_EndToEnd_NoLoggingID_RandomFallback verifies that when no
// logging trace ID is in context, a valid random trace ID is assigned
// (the span is still internally consistent).
func TestIDGen_EndToEnd_NoLoggingID_RandomFallback(t *testing.T) {
	prov, exp := newTestProviderWithIDGen(t)

	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{
		System:    "openai",
		Operation: "chat",
	})
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if !spans[0].SpanContext.TraceID().IsValid() {
		t.Error("expected non-zero random trace ID when no logging ID is seeded")
	}
}
