package aigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
)

// A failure the fallback walk moves past must still be counted against the
// provider that produced it. Otherwise a provider can fail every request and
// read zero, while its circuit breaker counts the same attempts and opens —
// two signals contradicting each other about one provider.
func TestRouteTargets_MaskedFailureIsCountedAgainstItsProvider(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "bad"}, {VirtualKey: "good"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name: "bad", models: []string{"gpt-4o"},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			return nil, errors.New("upstream exploded")
		},
	})
	gw.RegisterProvider(&mockProvider{
		name: "good", models: []string{"gpt-4o"},
		resp: &providers.Response{ID: "ok", Model: "gpt-4o", Provider: "good"},
	})

	before := counterValue(t, metrics.ProviderErrors.WithLabelValues("bad", metrics.ErrTypeProvider))
	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("route: %v — the healthy sibling should have served it", err)
	}
	if got := counterValue(t, metrics.ProviderErrors.WithLabelValues("bad", metrics.ErrTypeProvider)) - before; got != 1 {
		t.Errorf("provider_errors_total{provider=bad} delta = %v, want 1 — the masked failure was not counted", got)
	}
}
