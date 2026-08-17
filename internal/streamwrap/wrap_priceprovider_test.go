package streamwrap

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// ptrF returns a *float64 from a literal, mirroring the helper the strategies
// package tests keep for the same purpose.
func ptrF(v float64) *float64 { return &v }

// TestMeter_PriceProviderPricing is issue #396's streaming regression suite.
// A streaming request through a target registered under a routing alias
// (Gateway.RegisterProviderAs) must be priced against the alias's canonical
// vendor identity, never against the alias itself -- the catalog has never
// heard of it, so pricing the alias directly always resolves to unpriced. It
// must also keep reporting the alias everywhere attribution reads Provider:
// Prometheus labels and the published lifecycle event.
func TestMeter_PriceProviderPricing(t *testing.T) {
	catalog := models.Catalog{
		"openai/gpt-4o": {
			Provider: "openai",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(5.0),
				OutputPerMTokens: ptrF(15.0),
			},
		},
	}

	tests := []struct {
		name          string
		provider      string
		priceProvider string // empty leaves MeterMeta.PriceProvider unset
		wantHasCost   bool
		wantCostUSD   float64
	}{
		{
			// A caller that predates PriceProvider (every one before this
			// fix) leaves it unset and must keep pricing on Provider exactly
			// as it always did -- here that name is not in the catalog, so
			// the request is correctly unpriced.
			name:        "PriceProvider unset falls back to Provider",
			provider:    "openai--cred-1",
			wantHasCost: false,
			wantCostUSD: 0,
		},
		{
			// The routing alias "openai--cred-1" is not a catalog entry;
			// only PriceProvider naming the canonical "openai" identity
			// prices this request. (100 prompt + 50 completion) tokens at
			// $5 / $15 per 1M tokens = 0.0005 + 0.00075.
			name:          "PriceProvider set prices against the canonical identity",
			provider:      "openai--cred-1",
			priceProvider: "openai",
			wantHasCost:   true,
			wantCostUSD:   0.00125,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var published []map[string]any
			publishFn := func(_ context.Context, event events.HookEvent) {
				mu.Lock()
				published = append(published, event.Map())
				mu.Unlock()
			}

			src := feed(providers.StreamChunk{
				ID:    "1",
				Usage: &providers.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			})

			out := Meter(context.Background(), src, time.Now(), MeterMeta{
				Provider:      tt.provider,
				PriceProvider: tt.priceProvider,
				Model:         "gpt-4o",
				MetricModel:   "gpt-4o",
				Catalog:       catalog,
				PublishFn:     publishFn,
			})
			for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
			}

			mu.Lock()
			defer mu.Unlock()
			if len(published) != 1 {
				t.Fatalf("expected 1 publish call, got %d", len(published))
			}
			data := published[0]

			// Attribution invariant: the published event always names the
			// ROUTING alias, never the canonical identity PriceProvider
			// resolved the cost against.
			if data["provider"] != tt.provider {
				t.Errorf("published provider = %v, want routing key %q (attribution must not shift to the canonical name)", data["provider"], tt.provider)
			}

			hasCost, _ := data["cost_usd"].(float64)
			if tt.wantHasCost && hasCost != tt.wantCostUSD {
				t.Errorf("cost_usd = %v, want %v", hasCost, tt.wantCostUSD)
			}
			if !tt.wantHasCost && hasCost != 0 {
				t.Errorf("cost_usd = %v, want 0 (unpriced)", hasCost)
			}
		})
	}
}
