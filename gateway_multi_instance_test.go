package aigateway

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestGateway_RegisterProviderAs verifies that two providers reporting the
// same Name() can be registered independently under distinct routing
// aliases without either clobbering the other, and that RegisterProviderAs
// records the canonical provider type for later resolution.
func TestGateway_RegisterProviderAs(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "ollama-cloud-a"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gw.Close() }()

	if err := gw.RegisterProviderAs("ollama-cloud-a", "ollama-cloud", &mockProvider{
		name:   "ollama-cloud",
		models: []string{"model-a"},
		resp:   &providers.Response{ID: "resp-a", Model: "model-a"},
	}); err != nil {
		t.Fatalf("RegisterProviderAs(ollama-cloud-a): %v", err)
	}
	if err := gw.RegisterProviderAs("ollama-cloud-b", "ollama-cloud", &mockProvider{
		name:   "ollama-cloud",
		models: []string{"model-b"},
		resp:   &providers.Response{ID: "resp-b", Model: "model-b"},
	}); err != nil {
		t.Fatalf("RegisterProviderAs(ollama-cloud-b): %v", err)
	}

	pa, ok := gw.GetProvider("ollama-cloud-a")
	if !ok {
		t.Fatal("expected ollama-cloud-a to be registered")
	}
	pb, ok := gw.GetProvider("ollama-cloud-b")
	if !ok {
		t.Fatal("expected ollama-cloud-b to be registered")
	}
	if !pa.SupportsModel("model-a") || pa.SupportsModel("model-b") {
		t.Error("ollama-cloud-a instance clobbered or missing its own models")
	}
	if !pb.SupportsModel("model-b") || pb.SupportsModel("model-a") {
		t.Error("ollama-cloud-b instance clobbered or missing its own models")
	}

	names := gw.ListProviders()
	if len(names) != 2 {
		t.Fatalf("got %d registered provider names, want 2 (both aliases retained): %v", len(names), names)
	}

	if got := gw.canonicalProviderType("ollama-cloud-a"); got != "ollama-cloud" {
		t.Errorf("canonicalProviderType(ollama-cloud-a) = %q, want %q", got, "ollama-cloud")
	}
	if got := gw.canonicalProviderType("ollama-cloud-b"); got != "ollama-cloud" {
		t.Errorf("canonicalProviderType(ollama-cloud-b) = %q, want %q", got, "ollama-cloud")
	}

	// Fallback: a name that was never aliased returns itself unchanged.
	if got := gw.canonicalProviderType("never-aliased"); got != "never-aliased" {
		t.Errorf("canonicalProviderType(never-aliased) = %q, want input returned unchanged", got)
	}

	if err := gw.RegisterProviderAs("", "ollama-cloud", &mockProvider{name: "ollama-cloud"}); err == nil {
		t.Error("expected RegisterProviderAs with empty alias to return an error")
	}
}

// TestGateway_RouteStream_MultiInstanceStampsRoutingAlias is the streaming
// regression test for the multi-instance provider bug: two provider
// instances registered under distinct routing aliases (RegisterProviderAs)
// but sharing one canonical Name() ("ollama-cloud") must each report their
// own alias as the metered Provider — never the shared Name() both mock
// providers happen to return — otherwise metrics/logs/traces could never
// distinguish which instance handled a streaming request. This mirrors
// TestGateway_RegisterProviderAs's fixture style, extended to the streaming
// path via RouteStream + an after_request plugin that observes the metered
// response streamwrap.Meter builds from MeterMeta.Provider.
func TestGateway_RouteStream_MultiInstanceStampsRoutingAlias(t *testing.T) {
	newStreamMock := func(canonicalName string, model string) *mockStreamProvider {
		return &mockStreamProvider{
			mockProvider: mockProvider{name: canonicalName, models: []string{model}},
			streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
				ch := make(chan providers.StreamChunk, 1)
				ch <- providers.StreamChunk{
					ID:      "chatcmpl-multi",
					Object:  "chat.completion.chunk",
					Model:   model,
					Choices: []providers.StreamChoice{{Index: 0, Delta: providers.MessageDelta{Role: "assistant", Content: "hi"}, FinishReason: "stop"}},
				}
				close(ch)
				return ch, nil
			},
		}
	}

	run := func(t *testing.T, alias, model string, provider *mockStreamProvider) string {
		t.Helper()
		gw, err := New(Config{
			Strategy: StrategyConfig{Mode: ModeSingle},
			Targets:  []Target{{VirtualKey: alias}},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = gw.Close() }()

		if err := gw.RegisterProviderAs(alias, "ollama-cloud", provider); err != nil {
			t.Fatalf("RegisterProviderAs(%s): %v", alias, err)
		}

		var gotProvider string
		_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
			name: "capture-provider",
			typ:  plugin.TypeLogging,
			execFn: func(_ context.Context, pctx *plugin.Context) error {
				if pctx.Response != nil {
					gotProvider = pctx.Response.Provider
				}
				return nil
			},
		})

		ch, err := gw.RouteStream(context.Background(), providers.Request{
			Model:    model,
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
			Stream:   true,
		})
		if err != nil {
			t.Fatalf("RouteStream: %v", err)
		}
		drainStream(t, ch)
		return gotProvider
	}

	gotA := run(t, "ollama-cloud-a", "model-a", newStreamMock("ollama-cloud", "model-a"))
	if gotA != "ollama-cloud-a" {
		t.Errorf("instance A: metered provider = %q, want alias %q", gotA, "ollama-cloud-a")
	}

	gotB := run(t, "ollama-cloud-b", "model-b", newStreamMock("ollama-cloud", "model-b"))
	if gotB != "ollama-cloud-b" {
		t.Errorf("instance B: metered provider = %q, want alias %q", gotB, "ollama-cloud-b")
	}

	if gotA == gotB {
		t.Fatalf("both instances reported the same provider label %q; multi-instance targets must be distinguishable", gotA)
	}
}

// TestGateway_RecordSuccess_ResolvesAliasToCanonicalForCostLookup verifies
// that recordSuccess's cost calculation resolves resp.Provider (the routing
// alias, e.g. "ollama-cloud-a") to its canonical provider type before doing
// the catalog lookup. resp.Provider itself stays the alias (used for
// metrics/log labels so per-instance dashboards can distinguish aliases) —
// only the catalog lookup key changes.
func TestGateway_RecordSuccess_ResolvesAliasToCanonicalForCostLookup(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "ollama-cloud-a"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gw.Close() }()

	if err := gw.RegisterProviderAs("ollama-cloud-a", "ollama-cloud", &mockProvider{
		name:   "ollama-cloud",
		models: []string{"model-a"},
		resp: &providers.Response{
			ID:    "resp-a",
			Model: "model-a",
			Usage: providers.Usage{PromptTokens: 1000, CompletionTokens: 500},
		},
	}); err != nil {
		t.Fatalf("RegisterProviderAs: %v", err)
	}

	// Catalog is keyed by canonical provider type only — there is no
	// "ollama-cloud-a/model-a" entry. If the cost lookup used the alias
	// directly, cost would resolve to zero.
	gw.mu.Lock()
	gw.catalog = models.Catalog{
		"ollama-cloud/model-a": {
			Provider: "ollama-cloud",
			ModelID:  "model-a",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrFloat64(1.0),
				OutputPerMTokens: ptrFloat64(2.0),
			},
		},
	}
	gw.mu.Unlock()

	costCounter := metrics.ForRequest("ollama-cloud-a", "model-a").CostUSD
	before := counterValue(t, costCounter)

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "model-a",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.Provider != "ollama-cloud-a" {
		t.Fatalf("resp.Provider = %q, want alias %q preserved for metrics/log labels", resp.Provider, "ollama-cloud-a")
	}

	after := counterValue(t, costCounter)
	if after <= before {
		t.Fatalf("expected cost counter to increase via alias-resolved catalog lookup, before=%v after=%v", before, after)
	}
}

// TestGateway_RegisterProvider_IsCanonicalByDefault verifies RegisterProvider
// is a pure refactor over RegisterProviderAs: a provider registered the plain
// way is its own canonical type.
func TestGateway_RegisterProvider_IsCanonicalByDefault(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gw.Close() }()

	gw.RegisterProvider(&mockProvider{name: mockProviderName, models: []string{"gpt-4o"}})

	if got := gw.canonicalProviderType(mockProviderName); got != mockProviderName {
		t.Errorf("canonicalProviderType(%q) = %q, want unchanged", mockProviderName, got)
	}
}
