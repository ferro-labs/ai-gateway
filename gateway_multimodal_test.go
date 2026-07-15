package aigateway

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

type mockEmbeddingProvider struct {
	mockProvider
	capturedModel string
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	m.capturedModel = req.Model
	return &providers.EmbeddingResponse{Model: req.Model}, nil
}

// ── mockImageProvider ─────────────────────────────────────────────────────────

type mockImageProvider struct {
	mockProvider
	capturedModel string
}

func (m *mockImageProvider) GenerateImage(_ context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	m.capturedModel = req.Model
	return &providers.ImageResponse{}, nil
}

// ── alias resolution tests ────────────────────────────────────────────────────

func TestGateway_Embed_BeforePluginRejects(t *testing.T) {
	// Governance (before-request plugins) must apply to embeddings, not just chat:
	// a rejecting before-plugin blocks the request before the provider is called.
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"text-embedding-3-small"}},
	}
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(ep)

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "embed-blocker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "blocked"
			return nil
		},
	})

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	}); err == nil {
		t.Fatal("expected before-plugin to reject the embedding request")
	}
	if ep.capturedModel != "" {
		t.Error("embedding provider was called despite a before-plugin rejection")
	}
}

func TestGateway_Embed_AfterPluginSeesSurfaceAndUsage(t *testing.T) {
	// After-request plugins on the embedding surface receive the surface tag and
	// normalized token usage via Metadata (the additive channel budget uses).
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"text-embedding-3-small"}},
	}
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(ep)

	var gotSurface any
	var sawUsage bool
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "embed-observer",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			gotSurface = pctx.Metadata["surface"]
			_, sawUsage = pctx.Metadata["usage"].(providers.Usage)
			return nil
		},
	})

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	}); err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if gotSurface != "embeddings" {
		t.Errorf("after-plugin saw surface %v, want %q", gotSurface, "embeddings")
	}
	if !sawUsage {
		t.Error(`after-plugin did not receive normalized usage via Metadata["usage"]`)
	}
}

func TestGateway_Embed_ResolvesAlias(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Aliases:  map[string]string{"my-embed": "text-embedding-3-small"},
	})

	gw.RegisterProvider(ep)

	_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "my-embed",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if ep.capturedModel != "text-embedding-3-small" {
		t.Errorf("provider received model %q, want text-embedding-3-small (alias not resolved)", ep.capturedModel)
	}
}

func TestGateway_Embed_NoAliasPassthrough(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(ep)

	_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if ep.capturedModel != "text-embedding-3-small" {
		t.Errorf("provider received model %q, want text-embedding-3-small", ep.capturedModel)
	}
}

func TestGateway_GenerateImage_ResolvesAlias(t *testing.T) {
	ip := &mockImageProvider{
		mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{"dall-e-3"},
		},
	}
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Aliases:  map[string]string{"my-image-model": "dall-e-3"},
	})

	gw.RegisterProvider(ip)

	_, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
		Model:  "my-image-model",
		Prompt: "a cat",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if ip.capturedModel != "dall-e-3" {
		t.Errorf("provider received model %q, want dall-e-3 (alias not resolved)", ip.capturedModel)
	}
}

// ── StartDiscovery interval validation tests ──────────────────────────────────

func TestGateway_StartDiscovery_ZeroInterval(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	err := gw.StartDiscovery(context.Background(), 0)
	if err == nil {
		t.Fatal("StartDiscovery(0) should return an error")
	}
}

func TestGateway_StartDiscovery_NegativeInterval(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	err := gw.StartDiscovery(context.Background(), -time.Second)
	if err == nil {
		t.Fatal("StartDiscovery(-1s) should return an error")
	}
}

func TestGateway_StartDiscovery_ValidInterval(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := gw.StartDiscovery(ctx, time.Hour)
	if err != nil {
		t.Fatalf("StartDiscovery(1h) returned unexpected error: %v", err)
	}
	// Cancel immediately; just verifies no panic and clean return.
	cancel()
}

// ─── MCP integration test ──────────────────────────────────────────────────

// multiCallProvider is a test provider that returns pre-configured responses
// in sequence, recording every request it receives for later inspection.
