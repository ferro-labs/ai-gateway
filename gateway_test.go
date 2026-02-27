package aigateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// mockProvider is a test double for providers.Provider.
type mockProvider struct {
	name   string
	models []string
	resp   *providers.Response
	err    error
}

func (m *mockProvider) Name() string                  { return m.name }
func (m *mockProvider) SupportedModels() []string     { return m.models }
func (m *mockProvider) Models() []providers.ModelInfo { return nil }
func (m *mockProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *mockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return m.resp, m.err
}

func TestGateway_Route_Single(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o"},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "r1" {
		t.Errorf("got ID %q, want r1", resp.ID)
	}
}

func TestGateway_Route_Fallback(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "bad"},
			{VirtualKey: "good"},
		},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "bad",
		models: []string{"gpt-4o"},
		err:    fmt.Errorf("provider down"),
	})
	gw.RegisterProvider(&mockProvider{
		name:   "good",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "fallback-ok"},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "fallback-ok" {
		t.Errorf("got ID %q, want fallback-ok", resp.ID)
	}
}

func TestGateway_Route_NoTargets(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

func TestGateway_Route_ProviderNotFound(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "missing"}},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

// testPlugin is a mock plugin for gateway tests.
type testPlugin struct {
	name   string
	typ    plugin.PluginType
	execFn func(ctx context.Context, pctx *plugin.Context) error
}

func (p *testPlugin) Name() string                      { return p.name }
func (p *testPlugin) Type() plugin.PluginType           { return p.typ }
func (p *testPlugin) Init(map[string]interface{}) error { return nil }
func (p *testPlugin) Execute(ctx context.Context, pctx *plugin.Context) error {
	if p.execFn != nil {
		return p.execFn(ctx, pctx)
	}
	return nil
}

func TestGateway_Route_WithBeforePlugin(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok"},
	})

	called := false
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "tracker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, _ *plugin.Context) error {
			called = true
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("before-request plugin was not called")
	}
}

func TestGateway_Route_PluginRejectsRequest(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "should-not-reach"},
	})

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "blocker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "PII detected"
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected rejection error")
	}
}

func init() {
	plugin.RegisterFactory("test-plugin", func() plugin.Plugin {
		return &testPlugin{name: "test-plugin", typ: plugin.TypeGuardrail}
	})
}

func TestGateway_LoadPlugins(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
		Plugins: []PluginConfig{
			{
				Name:    "test-plugin",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]interface{}{},
			},
		},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok"},
	})

	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}
	if !gw.plugins.HasPlugins() {
		t.Error("expected plugins to be registered")
	}
}

func TestGateway_LoadPlugins_UnknownPlugin(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
		Plugins: []PluginConfig{
			{
				Name:    "does-not-exist",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]interface{}{},
			},
		},
	})

	err := gw.LoadPlugins()
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
	if got := err.Error(); got != "unknown plugin: does-not-exist" {
		t.Errorf("got error %q, want %q", got, "unknown plugin: does-not-exist")
	}
}

// ── mockEmbeddingProvider ─────────────────────────────────────────────────────

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

func TestGateway_Embed_ResolvesAlias(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   "mock",
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
			name:   "mock",
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
			name:   "mock",
			models: []string{"dall-e-3"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
	gw, _ := New(Config{})
	err := gw.StartDiscovery(context.Background(), 0)
	if err == nil {
		t.Fatal("StartDiscovery(0) should return an error")
	}
}

func TestGateway_StartDiscovery_NegativeInterval(t *testing.T) {
	gw, _ := New(Config{})
	err := gw.StartDiscovery(context.Background(), -time.Second)
	if err == nil {
		t.Fatal("StartDiscovery(-1s) should return an error")
	}
}

func TestGateway_StartDiscovery_ValidInterval(t *testing.T) {
	gw, _ := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := gw.StartDiscovery(ctx, time.Hour)
	if err != nil {
		t.Fatalf("StartDiscovery(1h) returned unexpected error: %v", err)
	}
	// Cancel immediately; just verifies no panic and clean return.
	cancel()
}
