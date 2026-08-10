package aigateway

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
)

type aliasCapabilityProvider struct{}

var (
	_ providers.Provider                = (*aliasCapabilityProvider)(nil)
	_ providers.StreamProvider          = (*aliasCapabilityProvider)(nil)
	_ providers.EmbeddingProvider       = (*aliasCapabilityProvider)(nil)
	_ providers.ImageProvider           = (*aliasCapabilityProvider)(nil)
	_ providers.RerankProvider          = (*aliasCapabilityProvider)(nil)
	_ providers.ModerationProvider      = (*aliasCapabilityProvider)(nil)
	_ providers.TranscriptionProvider   = (*aliasCapabilityProvider)(nil)
	_ providers.SpeechProvider          = (*aliasCapabilityProvider)(nil)
	_ providers.ProxiableProvider       = (*aliasCapabilityProvider)(nil)
	_ providers.NonOpenAIWireProvider   = (*aliasCapabilityProvider)(nil)
	_ providers.RequestSigner           = (*aliasCapabilityProvider)(nil)
	_ providers.BatchProvider           = (*aliasCapabilityProvider)(nil)
	_ providers.DiscoveryProvider       = (*aliasCapabilityProvider)(nil)
	_ providers.ConfiguredModelProvider = (*aliasCapabilityProvider)(nil)
	_ providers.AnyModelProvider        = (*aliasCapabilityProvider)(nil)
)

func (*aliasCapabilityProvider) Name() string { return "canonical" }

func (*aliasCapabilityProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	return &providers.Response{Model: req.Model}, nil
}

func (*aliasCapabilityProvider) SupportsModel(string) bool { return true }
func (*aliasCapabilityProvider) ServesAnyModel()           {}

func (*aliasCapabilityProvider) ConfiguredModels() []string { return []string{"configured-model"} }

func (*aliasCapabilityProvider) CompleteStream(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func (*aliasCapabilityProvider) Embed(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	return &providers.EmbeddingResponse{Model: req.Model}, nil
}

func (*aliasCapabilityProvider) GenerateImage(context.Context, providers.ImageRequest) (*providers.ImageResponse, error) {
	return &providers.ImageResponse{}, nil
}

func (*aliasCapabilityProvider) Rerank(_ context.Context, req providers.RerankRequest) (*providers.RerankResponse, error) {
	return &providers.RerankResponse{Model: req.Model}, nil
}

func (*aliasCapabilityProvider) Moderate(_ context.Context, req providers.ModerationRequest) (*providers.ModerationResponse, error) {
	return &providers.ModerationResponse{Model: req.Model}, nil
}

func (*aliasCapabilityProvider) Transcribe(context.Context, providers.TranscriptionRequest) (*providers.TranscriptionResponse, error) {
	return &providers.TranscriptionResponse{Text: "ok"}, nil
}

func (*aliasCapabilityProvider) Speech(context.Context, providers.SpeechRequest) (*providers.SpeechResponse, error) {
	return &providers.SpeechResponse{Audio: []byte("ok"), ContentType: "audio/mpeg"}, nil
}

func (*aliasCapabilityProvider) BaseURL() string                      { return "https://example.com/v1" }
func (*aliasCapabilityProvider) AuthHeaders() map[string]string       { return nil }
func (*aliasCapabilityProvider) NonOpenAIWire()                       {}
func (*aliasCapabilityProvider) SignProxyRequest(*http.Request) error { return nil }
func (*aliasCapabilityProvider) BatchBaseURL() string                 { return "https://example.com/v1" }
func (*aliasCapabilityProvider) BatchAuthHeaders() map[string]string  { return nil }

func (*aliasCapabilityProvider) DiscoverModels(context.Context) ([]providers.ModelInfo, error) {
	return []providers.ModelInfo{{ID: "discovered-model"}}, nil
}

func TestRegisterProviderAsPreservesEveryOptionalCapability(t *testing.T) {
	const alias = "canonical::credential-1"
	provider := &aliasCapabilityProvider{}
	gw, err := New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: alias, Models: []string{"alias-model"}}},
	})
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(provider)
	gw.RegisterProviderAs(alias, provider)

	if _, ok := gw.GetProvider("canonical"); !ok {
		t.Fatal("canonical provider was not registered")
	}
	registered, ok := gw.GetProvider(alias)
	if !ok {
		t.Fatalf("provider %q was not registered", alias)
	}
	if registered.Name() != alias {
		t.Fatalf("registered name = %q, want %q", registered.Name(), alias)
	}
	if got := gw.ListProviders(); !slices.Equal(got, []string{"canonical", alias}) {
		t.Fatalf("registered providers = %v, want [canonical %s]", got, alias)
	}

	assertCapability := func(name string, ok bool) {
		t.Helper()
		if !ok {
			t.Errorf("alias lost %s", name)
		}
	}
	_, ok = providers.As[providers.StreamProvider](registered)
	assertCapability("StreamProvider", ok)
	_, ok = providers.As[providers.EmbeddingProvider](registered)
	assertCapability("EmbeddingProvider", ok)
	_, ok = providers.As[providers.ImageProvider](registered)
	assertCapability("ImageProvider", ok)
	_, ok = providers.As[providers.RerankProvider](registered)
	assertCapability("RerankProvider", ok)
	_, ok = providers.As[providers.ModerationProvider](registered)
	assertCapability("ModerationProvider", ok)
	_, ok = providers.As[providers.TranscriptionProvider](registered)
	assertCapability("TranscriptionProvider", ok)
	_, ok = providers.As[providers.SpeechProvider](registered)
	assertCapability("SpeechProvider", ok)
	_, ok = providers.As[providers.ProxiableProvider](registered)
	assertCapability("ProxiableProvider", ok)
	_, ok = providers.As[providers.NonOpenAIWireProvider](registered)
	assertCapability("NonOpenAIWireProvider", ok)
	_, ok = providers.As[providers.RequestSigner](registered)
	assertCapability("RequestSigner", ok)
	_, ok = providers.As[providers.BatchProvider](registered)
	assertCapability("BatchProvider", ok)
	_, ok = providers.As[providers.DiscoveryProvider](registered)
	assertCapability("DiscoveryProvider", ok)
	_, ok = providers.As[providers.ConfiguredModelProvider](registered)
	assertCapability("ConfiguredModelProvider", ok)
	_, ok = providers.As[providers.AnyModelProvider](registered)
	assertCapability("AnyModelProvider", ok)

	streamProvider, ok := gw.FindStreamingByModel("alias-model")
	if !ok {
		t.Fatal("streaming alias was not indexed")
	}
	if _, err := streamProvider.CompleteStream(context.Background(), providers.Request{Model: "alias-model"}); err != nil {
		t.Fatalf("stream through alias: %v", err)
	}

	ctx := context.Background()
	if _, err := gw.Embed(ctx, providers.EmbeddingRequest{Model: "alias-model", Input: "hello"}); err != nil {
		t.Fatalf("embed through alias: %v", err)
	}
	if _, err := gw.GenerateImage(ctx, providers.ImageRequest{Model: "alias-model", Prompt: "hello"}); err != nil {
		t.Fatalf("image through alias: %v", err)
	}
	if _, err := gw.Rerank(ctx, providers.RerankRequest{Model: "alias-model", Query: "q", Documents: []string{"d"}}); err != nil {
		t.Fatalf("rerank through alias: %v", err)
	}
	if _, err := gw.Moderate(ctx, providers.ModerationRequest{Model: "alias-model", Input: "hello"}); err != nil {
		t.Fatalf("moderation through alias: %v", err)
	}
	if _, err := gw.Transcribe(ctx, providers.TranscriptionRequest{Model: "alias-model", File: []byte("audio"), Filename: "test.wav"}); err != nil {
		t.Fatalf("transcription through alias: %v", err)
	}
	if _, err := gw.Speech(ctx, providers.SpeechRequest{Model: "alias-model", Input: "hello", Voice: "test"}); err != nil {
		t.Fatalf("speech through alias: %v", err)
	}
}
