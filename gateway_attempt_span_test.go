package aigateway

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// attemptSpanRecorder is a Provider that also implements AttemptSpanProvider
// and keeps every attempt span it opened, in order.
type attemptSpanRecorder struct {
	fakeProvider
	mu       sync.Mutex
	attempts []*recordedAttemptSpan
}

type recordedAttemptSpan struct {
	fakeSpan
	targetKey string
	sequence  int
}

// attemptCtxKey marks the context StartAttemptSpan returns, so a provider call
// can report whether it actually ran under it. A real provider carries the OTel
// span the same way; the marker is what makes the parent/sibling distinction
// between the unary and streaming surfaces observable from a test.
type attemptCtxKey struct{}

func (p *attemptSpanRecorder) StartAttemptSpan(ctx context.Context, targetKey string, sequence int) (context.Context, observability.Span) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &recordedAttemptSpan{targetKey: targetKey, sequence: sequence}
	p.attempts = append(p.attempts, s)
	return context.WithValue(ctx, attemptCtxKey{}, s), s
}

var _ observability.AttemptSpanProvider = (*attemptSpanRecorder)(nil)

func failoverGatewayWithAttemptSpans(t *testing.T, attemptSpans bool) (*Gateway, *attemptSpanRecorder) {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "primary", Retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
			{VirtualKey: "secondary"},
		},
		Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{AttemptSpans: attemptSpans}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	rec := &attemptSpanRecorder{}
	gw.SetObservability(rec)
	gw.RegisterProvider(&mockProvider{name: "primary", models: []string{testModel},
		err: core.StatusError("primary", http.StatusServiceUnavailable, "down")})
	gw.RegisterProvider(&mockProvider{name: "secondary", models: []string{testModel},
		resp: &providers.Response{ID: "ok", Provider: "secondary", Model: testModel}})
	return gw, rec
}

func TestGateway_Route_AttemptSpansFollowEveryAttempt(t *testing.T) {
	gw, rec := failoverGatewayWithAttemptSpans(t, true)

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	if len(rec.attempts) != 3 {
		t.Fatalf("attempt spans = %d, want 3 (primary, primary retry, secondary)", len(rec.attempts))
	}
	want := []struct {
		key     string
		seq     int
		outcome string
		failed  bool
	}{
		{"primary", 1, "error", true},
		{"primary", 2, "error", true},
		{"secondary", 3, "success", false},
	}
	for i, a := range rec.attempts {
		if a.targetKey != want[i].key || a.sequence != want[i].seq {
			t.Errorf("attempt %d = %s #%d, want %s #%d", i+1, a.targetKey, a.sequence, want[i].key, want[i].seq)
		}
		if got := a.attrs[observability.AttrFerroRoutingOutcome]; got != want[i].outcome {
			t.Errorf("attempt %d outcome = %v, want %q", i+1, got, want[i].outcome)
		}
		if (a.err != nil) != want[i].failed {
			t.Errorf("attempt %d error recorded = %v, want %v", i+1, a.err != nil, want[i].failed)
		}
		if !a.ended {
			t.Errorf("attempt %d span was never ended", i+1)
		}
	}
}

// TestGateway_Route_UnaryCallRunsUnderTheAttemptSpanContext pins the half of
// the AttemptSpanProvider contract that holds on the unary surfaces: the
// provider call runs on the context StartAttemptSpan returned, which is what
// makes a real outbound HTTP span a CHILD of the attempt span. Streaming is
// deliberately the other way — see the RouteStream test below.
func TestGateway_Route_UnaryCallRunsUnderTheAttemptSpanContext(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy:      config.StrategyConfig{Mode: config.ModeSingle},
		Targets:       []config.Target{{VirtualKey: "primary"}},
		Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{AttemptSpans: true}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	rec := &attemptSpanRecorder{}
	gw.SetObservability(rec)

	var seen any
	gw.RegisterProvider(&mockProvider{name: "primary", models: []string{testModel},
		completeFn: func(ctx context.Context, _ providers.Request) (*providers.Response, error) {
			seen = ctx.Value(attemptCtxKey{})
			return &providers.Response{ID: "ok", Provider: "primary", Model: testModel}, nil
		}})

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(rec.attempts) != 1 {
		t.Fatalf("attempt spans = %d, want 1", len(rec.attempts))
	}
	if seen != rec.attempts[0] {
		t.Errorf("provider call ran under %v, want the context StartAttemptSpan returned for attempt 1", seen)
	}
}

// TestGateway_RouteStream_AttemptSpansAreSiblingsOfTheProviderCall covers
// streaming's deliberate asymmetry. Attempt spans are opened, sequenced and
// ended for a stream walk exactly as for a unary one, but the provider call
// runs on the stream context startStreamOn captured rather than the attempt
// span's — the channel outlives the attempt, so the attempt span ends when the
// stream STARTS. A real outbound HTTP span is therefore the attempt span's
// sibling under the request span, never its child, which is what the
// AttemptSpanProvider godoc warns implementers not to assume away.
func TestGateway_RouteStream_AttemptSpansAreSiblingsOfTheProviderCall(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy:      config.StrategyConfig{Mode: config.ModeFallback},
		Targets:       []config.Target{{VirtualKey: "primary"}, {VirtualKey: "secondary"}},
		Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{AttemptSpans: true}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	rec := &attemptSpanRecorder{}
	gw.SetObservability(rec)
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "primary", models: []string{testModel}},
		streamErr:    core.StatusError("primary", http.StatusServiceUnavailable, "down"),
	})
	// CompleteStream runs on a goroutine inside raceCompleteStream, so the flag
	// it sets is read from another goroutine and must be atomic under -race.
	var ranUnderAttemptCtx atomic.Bool
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "secondary", models: []string{testModel}},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ranUnderAttemptCtx.Store(ctx.Value(attemptCtxKey{}) != nil)
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	for range ch { //nolint:revive // drain so the stream completes before assertions
	}

	if len(rec.attempts) != 2 {
		t.Fatalf("attempt spans = %d, want 2 (primary, secondary)", len(rec.attempts))
	}
	want := []struct {
		key     string
		seq     int
		outcome string
		failed  bool
	}{
		{"primary", 1, "error", true},
		{"secondary", 2, "success", false},
	}
	for i, a := range rec.attempts {
		if a.targetKey != want[i].key || a.sequence != want[i].seq {
			t.Errorf("attempt %d = %s #%d, want %s #%d", i+1, a.targetKey, a.sequence, want[i].key, want[i].seq)
		}
		if got := a.attrs[observability.AttrFerroRoutingOutcome]; got != want[i].outcome {
			t.Errorf("attempt %d outcome = %v, want %q", i+1, got, want[i].outcome)
		}
		if (a.err != nil) != want[i].failed {
			t.Errorf("attempt %d error recorded = %v, want %v", i+1, a.err != nil, want[i].failed)
		}
		if !a.ended {
			t.Errorf("attempt %d span was never ended", i+1)
		}
	}
	if ranUnderAttemptCtx.Load() {
		t.Error("streaming provider call ran under the attempt-span context; startStreamOn runs it on the stream context that outlives the attempt, so its span is a sibling of the attempt span")
	}
}

// TestGateway_Route_PanicEndsAttemptSpanAndPropagates pins the panic path
// through attemptTarget: callUnderResilience can re-panic (the circuit
// breaker treats a panicking provider as a failure and re-raises), and that
// must still end the attempt span with an error identifying the panic, and
// still let the original panic value reach the caller unchanged.
func TestGateway_Route_PanicEndsAttemptSpanAndPropagates(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy:      config.StrategyConfig{Mode: config.ModeSingle},
		Targets:       []config.Target{{VirtualKey: "primary"}},
		Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{AttemptSpans: true}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	rec := &attemptSpanRecorder{}
	gw.SetObservability(rec)
	gw.RegisterProvider(&mockProvider{name: "primary", models: []string{testModel},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			panic("provider exploded")
		}})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = gw.Route(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	}()

	if recovered != "provider exploded" {
		t.Fatalf("recovered = %v, want the original panic value to propagate unchanged", recovered)
	}
	if len(rec.attempts) != 1 {
		t.Fatalf("attempt spans = %d, want 1", len(rec.attempts))
	}
	if !rec.attempts[0].ended {
		t.Error("attempt span was never ended on the panic path")
	}
	if rec.attempts[0].err == nil {
		t.Error("attempt span error was not set on the panic path")
	}
}

func TestGateway_Route_AttemptSpansOffByDefault(t *testing.T) {
	gw, rec := failoverGatewayWithAttemptSpans(t, false)

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(rec.attempts) != 0 {
		t.Fatalf("attempt spans = %d, want none while attempt_spans is off", len(rec.attempts))
	}
}
