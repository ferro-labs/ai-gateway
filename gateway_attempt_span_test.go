package aigateway

import (
	"context"
	"net/http"
	"sync"
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

func (p *attemptSpanRecorder) StartAttemptSpan(ctx context.Context, targetKey string, sequence int) (context.Context, observability.Span) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &recordedAttemptSpan{targetKey: targetKey, sequence: sequence}
	p.attempts = append(p.attempts, s)
	return ctx, s
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

func TestGateway_Route_AttemptSpansOffByDefault(t *testing.T) {
	gw, rec := failoverGatewayWithAttemptSpans(t, false)

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(rec.attempts) != 0 {
		t.Fatalf("attempt spans = %d, want none while attempt_spans is off", len(rec.attempts))
	}
}
