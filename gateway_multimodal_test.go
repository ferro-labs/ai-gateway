package aigateway

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

type mockEmbeddingProvider struct {
	mockProvider
	capturedModel string
	calls         int
	err           error
	// promptTokens is echoed back as EmbeddingResponse.Usage.PromptTokens so
	// tests can assert on the cost/metrics/span accounting Embed derives from it.
	promptTokens int
	// embedFn, when set, overrides the default success/err behavior — used by
	// tests that need a call count to change behavior (e.g. fail-then-succeed).
	embedFn func(context.Context, providers.EmbeddingRequest) (*providers.EmbeddingResponse, error)
}

func (m *mockEmbeddingProvider) Embed(ctx context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	m.calls++
	m.capturedModel = req.Model
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	if m.err != nil {
		return nil, m.err
	}
	return &providers.EmbeddingResponse{
		Model: req.Model,
		Usage: providers.EmbeddingUsage{PromptTokens: m.promptTokens, TotalTokens: m.promptTokens},
	}, nil
}

// ── mockImageProvider ─────────────────────────────────────────────────────────

type mockImageProvider struct {
	mockProvider
	capturedModel string
	calls         int
	err           error
	// images is the number of entries GenerateImage puts in Data, so tests can
	// assert on ImageCount-based cost accounting. 0 defaults to 1, matching the
	// gateway's own req.N-unset fallback (see routeImage).
	images int
	// imageFn, when set, overrides the default success/err behavior.
	imageFn func(context.Context, providers.ImageRequest) (*providers.ImageResponse, error)
}

func (m *mockImageProvider) GenerateImage(ctx context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	m.calls++
	m.capturedModel = req.Model
	if m.imageFn != nil {
		return m.imageFn(ctx, req)
	}
	if m.err != nil {
		return nil, m.err
	}
	n := m.images
	if n == 0 {
		n = 1
	}
	return &providers.ImageResponse{Data: make([]providers.GeneratedImage, n)}, nil
}

// ── alias resolution tests ────────────────────────────────────────────────────

func TestGateway_Embed_BeforePluginRejects(t *testing.T) {
	// Governance (before-request plugins) must apply to embeddings, not just chat:
	// a rejecting before-plugin blocks the request before the provider is called.
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"text-embedding-3-small"}},
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
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
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
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
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
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
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
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
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
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

// ── configured-target routing (A2 fix) ────────────────────────────────────────

// TestGateway_Embed_UsesConfiguredTargetNotRegistrationOrder is the core A2
// regression test: with two registered embedding providers, only the one
// named in Targets may be called — registration order must not decide it.
func TestGateway_Embed_UsesConfiguredTargetNotRegistrationOrder(t *testing.T) {
	rogue := &mockEmbeddingProvider{mockProvider: mockProvider{name: "rogue", models: []string{"embed-model"}}}
	selected := &mockEmbeddingProvider{mockProvider: mockProvider{name: "selected", models: []string{"embed-model"}}}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "selected"}},
	})
	gw.RegisterProvider(rogue)
	gw.RegisterProvider(selected)

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: "embed-model", Input: "hi"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if rogue.calls != 0 || selected.calls != 1 {
		t.Fatalf("calls rogue=%d selected=%d, want 0/1", rogue.calls, selected.calls)
	}
}

// TestGateway_MultimodalFallbackAdvancesConfiguredTargets is the headline A2
// regression: strategy.mode: fallback with two embedding/image-capable targets
// must advance to the second target when the first fails — before the fix
// these surfaces had no fallback at all.
func TestGateway_MultimodalFallbackAdvancesConfiguredTargets(t *testing.T) {
	t.Run("embeddings", func(t *testing.T) {
		first := &mockEmbeddingProvider{mockProvider: mockProvider{name: "first", models: []string{"embed-model"}}, err: errors.New("down")}
		second := &mockEmbeddingProvider{mockProvider: mockProvider{name: "second", models: []string{"embed-model"}}}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "first"}, {VirtualKey: "second"}},
		})
		gw.RegisterProvider(first)
		gw.RegisterProvider(second)
		if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: "embed-model", Input: "hi"}); err != nil {
			t.Fatalf("Embed fallback: %v", err)
		}
		if first.calls != 1 || second.calls != 1 {
			t.Fatalf("calls first=%d second=%d, want 1/1", first.calls, second.calls)
		}
	})

	t.Run("images", func(t *testing.T) {
		first := &mockImageProvider{mockProvider: mockProvider{name: "first", models: []string{"image-model"}}, err: errors.New("down")}
		second := &mockImageProvider{mockProvider: mockProvider{name: "second", models: []string{"image-model"}}}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "first"}, {VirtualKey: "second"}},
		})
		gw.RegisterProvider(first)
		gw.RegisterProvider(second)
		if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: "image-model", Prompt: "cat"}); err != nil {
			t.Fatalf("GenerateImage fallback: %v", err)
		}
		if first.calls != 1 || second.calls != 1 {
			t.Fatalf("calls first=%d second=%d, want 1/1", first.calls, second.calls)
		}
	})
}

// TestGateway_MultimodalRetryHonoursConfiguredAttempts proves retries route
// through the pipeline's shared retry policy (gateway_pipeline.go) instead of a
// one-shot call: with Retry.Attempts: 3 configured and an always-failing
// target, the provider must be attempted exactly 3 times before giving up.
func TestGateway_MultimodalRetryHonoursConfiguredAttempts(t *testing.T) {
	t.Run("embeddings", func(t *testing.T) {
		ep := &mockEmbeddingProvider{mockProvider: mockProvider{name: "flaky", models: []string{"embed-model"}}, err: errors.New("down")}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "flaky", Retry: &config.RetryConfig{Attempts: 3, InitialBackoffMs: 1}}},
		})
		gw.RegisterProvider(ep)

		if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: "embed-model", Input: "hi"}); err == nil {
			t.Fatal("expected Embed to fail after exhausting retries")
		}
		if ep.calls != 3 {
			t.Fatalf("calls = %d, want 3 (the configured retry attempts)", ep.calls)
		}
	})

	t.Run("images", func(t *testing.T) {
		ip := &mockImageProvider{mockProvider: mockProvider{name: "flaky", models: []string{"image-model"}}, err: errors.New("down")}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "flaky", Retry: &config.RetryConfig{Attempts: 3, InitialBackoffMs: 1}}},
		})
		gw.RegisterProvider(ip)

		if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: "image-model", Prompt: "cat"}); err == nil {
			t.Fatal("expected GenerateImage to fail after exhausting retries")
		}
		if ip.calls != 3 {
			t.Fatalf("calls = %d, want 3 (the configured retry attempts)", ip.calls)
		}
	})
}

// TestGateway_MultimodalRequestTimeoutBoundsTheRequest proves RequestTimeout
// bounds Embed/GenerateImage the same way it bounds Route: a provider that
// hangs past the configured timeout must be cut short, not run to completion.
func TestGateway_MultimodalRequestTimeoutBoundsTheRequest(t *testing.T) {
	const hang = 500 * time.Millisecond

	t.Run("embeddings", func(t *testing.T) {
		ep := &mockEmbeddingProvider{
			mockProvider: mockProvider{name: "slow", models: []string{"embed-model"}},
			embedFn: func(ctx context.Context, _ providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(hang):
					return &providers.EmbeddingResponse{}, nil
				}
			},
		}
		gw, _ := newTestGateway(t, config.Config{
			Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
			Targets:        []config.Target{{VirtualKey: "slow"}},
			RequestTimeout: "50ms",
		})
		gw.RegisterProvider(ep)

		start := time.Now()
		_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: "embed-model", Input: "hi"})
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Embed error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed >= hang {
			t.Errorf("Embed took %v — the 50ms request_timeout did not bound it", elapsed)
		}
	})

	t.Run("images", func(t *testing.T) {
		ip := &mockImageProvider{
			mockProvider: mockProvider{name: "slow", models: []string{"image-model"}},
			imageFn: func(ctx context.Context, _ providers.ImageRequest) (*providers.ImageResponse, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(hang):
					return &providers.ImageResponse{}, nil
				}
			},
		}
		gw, _ := newTestGateway(t, config.Config{
			Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
			Targets:        []config.Target{{VirtualKey: "slow"}},
			RequestTimeout: "50ms",
		})
		gw.RegisterProvider(ip)

		start := time.Now()
		_, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: "image-model", Prompt: "cat"})
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GenerateImage error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed >= hang {
			t.Errorf("GenerateImage took %v — the 50ms request_timeout did not bound it", elapsed)
		}
	})
}

// TestGateway_MultimodalUnlistedProviderIsNotReachable pins targets as an
// allowlist on every surface.
//
// Until v1.4.0 embeddings, images and streaming each fell back to any
// registered provider when no configured target was viable, so a provider the
// operator never listed could serve — and bill — a request. Non-streaming chat
// never did, so the same model could resolve to different providers depending
// only on which surface it arrived on.
//
// A provider absent from targets is now unreachable on every surface. This is
// what LiteLLM, Envoy AI Gateway and Portkey all do: a model is served by a
// declared deployment or it is refused, and a catch-all is something the
// operator opts into rather than a default.
func TestGateway_MultimodalUnlistedProviderIsNotReachable(t *testing.T) {
	t.Run("embeddings", func(t *testing.T) {
		unlisted := &mockEmbeddingProvider{mockProvider: mockProvider{name: "unlisted", models: []string{"embed-model"}}}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			// No target names "unlisted" and no target can embed embed-model.
			Targets: []config.Target{{VirtualKey: "chat-only"}},
		})
		gw.RegisterProvider(&mockProvider{name: "chat-only", models: []string{"embed-model"}})
		gw.RegisterProvider(unlisted)

		_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: "embed-model", Input: "hi"})
		if err == nil {
			t.Fatal("Embed succeeded; a provider outside targets must not serve the request")
		}
		if !errors.Is(err, core.ErrNoCapableProvider) {
			t.Errorf("error = %v, want ErrNoCapableProvider so the surface answers 404 rather than 500", err)
		}
		if unlisted.calls != 0 {
			t.Errorf("unlisted provider calls = %d, want 0 — it is not a configured target", unlisted.calls)
		}
	})

	t.Run("images", func(t *testing.T) {
		unlisted := &mockImageProvider{mockProvider: mockProvider{name: "unlisted", models: []string{"image-model"}}}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "chat-only"}},
		})
		gw.RegisterProvider(&mockProvider{name: "chat-only", models: []string{"image-model"}})
		gw.RegisterProvider(unlisted)

		_, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: "image-model", Prompt: "cat"})
		if err == nil {
			t.Fatal("GenerateImage succeeded; a provider outside targets must not serve the request")
		}
		if !errors.Is(err, core.ErrNoCapableProvider) {
			t.Errorf("error = %v, want ErrNoCapableProvider so the surface answers 404 rather than 500", err)
		}
		if unlisted.calls != 0 {
			t.Errorf("unlisted provider calls = %d, want 0 — it is not a configured target", unlisted.calls)
		}
	})
}

func TestGateway_MultimodalHonorsLatencyAndCostStrategies(t *testing.T) {
	t.Run("image load balance weights only capable targets", func(t *testing.T) {
		chatOnly := &mockProvider{name: "chat-only", models: []string{"image-model"}}
		first := &mockImageProvider{mockProvider: mockProvider{name: "first", models: []string{"image-model"}}}
		second := &mockImageProvider{mockProvider: mockProvider{name: "second", models: []string{"image-model"}}}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeLoadBalance},
			Targets: []config.Target{
				{VirtualKey: "chat-only", Weight: 1_000_000_000},
				{VirtualKey: "first", Weight: 1},
				{VirtualKey: "second", Weight: 1},
			},
		})
		gw.RegisterProvider(chatOnly)
		gw.RegisterProvider(first)
		gw.RegisterProvider(second)

		for range 64 {
			if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: "image-model", Prompt: "cat"}); err != nil {
				t.Fatalf("GenerateImage: %v", err)
			}
		}
		if first.calls == 0 || second.calls == 0 {
			t.Fatalf("load balance calls first=%d second=%d; chat-only target distorted surface weights", first.calls, second.calls)
		}
	})

	t.Run("embedding latency", func(t *testing.T) {
		slow := &mockEmbeddingProvider{mockProvider: mockProvider{name: "slow", models: []string{"embed-model"}}}
		fast := &mockEmbeddingProvider{mockProvider: mockProvider{name: "fast", models: []string{"embed-model"}}}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeLatency},
			Targets:  []config.Target{{VirtualKey: "slow"}, {VirtualKey: "fast"}},
		})
		gw.RegisterProvider(slow)
		gw.RegisterProvider(fast)
		gw.latencyTracker.Record("slow", "embed-model", 100*time.Millisecond)
		gw.latencyTracker.Record("fast", "embed-model", time.Millisecond)

		// The ranking is read through the exploration share, and before any
		// call is made: both mocks answer instantly, so a served request
		// would teach the tracker that "slow" is not slow at all.
		leads := map[string]int{}
		for range 200 {
			keys, err := gw.surfaceTargetOrder("embed-model", surfaceEmbeddings)
			if err != nil {
				t.Fatalf("surfaceTargetOrder: %v", err)
			}
			leads[keys[0]]++
		}
		if leads["fast"] < 150 {
			t.Fatalf("fast led %d of 200 embedding selections, want the large majority", leads["fast"])
		}
		if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: "embed-model", Input: "hi"}); err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if slow.calls+fast.calls != 1 {
			t.Fatalf("calls slow=%d fast=%d, want exactly one embedding call", slow.calls, fast.calls)
		}
	})

	t.Run("image cost", func(t *testing.T) {
		expensive := &mockImageProvider{mockProvider: mockProvider{name: "expensive", models: []string{"image-model"}}}
		cheap := &mockImageProvider{mockProvider: mockProvider{name: "cheap", models: []string{"image-model"}}}
		gw, _ := newTestGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized},
			Targets:  []config.Target{{VirtualKey: "expensive"}, {VirtualKey: "cheap"}},
		})
		gw.RegisterProvider(expensive)
		gw.RegisterProvider(cheap)
		high, low := 0.10, 0.01
		gw.mu.Lock()
		gw.catalog = models.Catalog{
			"expensive/image-model": {Provider: "expensive", ModelID: "image-model", Mode: models.ModeImage, Pricing: models.Pricing{ImagePerTile: &high}},
			"cheap/image-model":     {Provider: "cheap", ModelID: "image-model", Mode: models.ModeImage, Pricing: models.Pricing{ImagePerTile: &low}},
		}
		gw.mu.Unlock()

		if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: "image-model", Prompt: "cat"}); err != nil {
			t.Fatalf("GenerateImage: %v", err)
		}
		if expensive.calls != 0 || cheap.calls != 1 {
			t.Fatalf("cost strategy calls expensive=%d cheap=%d, want 0/1", expensive.calls, cheap.calls)
		}
	})
}

// ── content-based routing on promptless surfaces ──────────────────────────────

// TestGateway_Embed_ContentBasedPromptNotContains_DoesNotVacuouslyMatch is a
// regression test: content-based routing rules key off req.Messages, which
// embeddings/image requests never have. prompt_not_contains is
// `!anyUserMessageContains(...)`, which is vacuously TRUE over zero messages —
// before the fix this rule matched every embeddings request and always won
// routing, regardless of the configured target order or what the rule said.
func TestGateway_Embed_ContentBasedPromptNotContains_DoesNotVacuouslyMatch(t *testing.T) {
	first := &mockEmbeddingProvider{mockProvider: mockProvider{name: "first", models: []string{"embed-model"}}}
	special := &mockEmbeddingProvider{mockProvider: mockProvider{name: "special", models: []string{"embed-model"}}}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{
				{Type: "prompt_not_contains", Value: "unrelated-topic", TargetKey: "special"},
			},
		},
		Targets: []config.Target{{VirtualKey: "first"}, {VirtualKey: "special"}},
	})
	gw.RegisterProvider(first)
	gw.RegisterProvider(special)

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: "embed-model", Input: "hi"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if first.calls != 1 || special.calls != 0 {
		t.Fatalf("calls first=%d special=%d, want 1/0 — prompt_not_contains vacuously matched a promptless request", first.calls, special.calls)
	}
}

// TestGateway_GenerateImage_ContentBasedPromptNotContains_DoesNotVacuouslyMatch
// is the image-surface counterpart of the embeddings regression above.
func TestGateway_GenerateImage_ContentBasedPromptNotContains_DoesNotVacuouslyMatch(t *testing.T) {
	first := &mockImageProvider{mockProvider: mockProvider{name: "first", models: []string{"image-model"}}}
	special := &mockImageProvider{mockProvider: mockProvider{name: "special", models: []string{"image-model"}}}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{
				{Type: "prompt_not_contains", Value: "unrelated-topic", TargetKey: "special"},
			},
		},
		Targets: []config.Target{{VirtualKey: "first"}, {VirtualKey: "special"}},
	})
	gw.RegisterProvider(first)
	gw.RegisterProvider(special)

	if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: "image-model", Prompt: "cat"}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if first.calls != 1 || special.calls != 0 {
		t.Fatalf("calls first=%d special=%d, want 1/0 — prompt_not_contains vacuously matched a promptless request", first.calls, special.calls)
	}
}

// ── StartDiscovery interval validation tests ──────────────────────────────────

func TestGateway_StartDiscovery_ZeroInterval(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "unused"}},
	})

	err := gw.StartDiscovery(context.Background(), 0)
	if err == nil {
		t.Fatal("StartDiscovery(0) should return an error")
	}
}

func TestGateway_StartDiscovery_NegativeInterval(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "unused"}},
	})

	err := gw.StartDiscovery(context.Background(), -time.Second)
	if err == nil {
		t.Fatal("StartDiscovery(-1s) should return an error")
	}
}

func TestGateway_StartDiscovery_ValidInterval(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "unused"}},
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

// ── observability parity tests (metrics / span / cost / lifecycle events) ────

// TestGateway_Embed_EmitsMetricsSpanAndEvent covers the embeddings observability
// gap: before the fix, Embed never emitted Prometheus metrics, an OTel span, cost
// accounting, or a lifecycle event. No plugins are registered on gw, which proves
// cost/metrics/events are recorded unconditionally — not only when the budget
// plugin happens to be installed (the pre-fix behavior).
func TestGateway_Embed_EmitsMetricsSpanAndEvent(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "embed-provider", models: []string{"text-embedding-3-small"}},
		promptTokens: 100,
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "embed-provider"}},
	})
	gw.RegisterProvider(ep)

	price := 0.02
	gw.mu.Lock()
	gw.catalog = models.Catalog{
		"embed-provider/text-embedding-3-small": {
			Provider: "embed-provider", ModelID: "text-embedding-3-small",
			Mode: models.ModeEmbedding, Pricing: models.Pricing{EmbeddingPerMTokens: &price},
		},
	}
	gw.mu.Unlock()

	obs := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(obs)
	handles := metrics.ForRequest("embed-provider", "text-embedding-3-small")
	successBefore := counterValue(t, handles.Success)
	costBefore := counterValue(t, handles.CostUSD)

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if got := counterValue(t, handles.Success); got != successBefore+1 {
		t.Errorf("Success counter = %v, want %v", got, successBefore+1)
	}
	if got := counterValue(t, handles.CostUSD); got <= costBefore {
		t.Errorf("CostUSD counter did not increase: got %v, before %v", got, costBefore)
	}

	if obs.attrs.Operation != "embeddings" {
		t.Errorf("Operation = %q, want %q", obs.attrs.Operation, "embeddings")
	}
	sp := obs.rootSpan()
	if sp == nil {
		t.Fatal("expected a root span for Embed")
	}
	if !sp.ended {
		t.Error("expected span.End() to be called")
	}
	if sp.tokensIn != 100 {
		t.Errorf("span tokensIn = %d, want 100", sp.tokensIn)
	}
	if sp.cost.TotalUSD <= 0 {
		t.Error("expected span cost to be recorded")
	}
	if got, ok := sp.attrs[observability.AttrFerroRoutingTargetKey]; !ok || got != "embed-provider" {
		t.Errorf("routing target key attr = %v, want embed-provider", got)
	}

	evts := eventsWithSubject(obs.capturedEvents(), "gateway.request.completed")
	if len(evts) != 1 {
		t.Fatalf("expected 1 completed event, got %d", len(evts))
	}
	if evts[0].Subject != "gateway.request.completed" {
		t.Errorf("event Subject = %q, want gateway.request.completed", evts[0].Subject)
	}
	if evts[0].Cost.TotalUSD <= 0 {
		t.Error("expected event cost to be recorded")
	}
}

// TestGateway_GenerateImage_EmitsMetricsSpanAndEvent covers the same
// observability gap for the images surface. No plugins are registered on gw,
// proving image cost/metrics/events do not depend on the budget plugin.
func TestGateway_GenerateImage_EmitsMetricsSpanAndEvent(t *testing.T) {
	ip := &mockImageProvider{
		mockProvider: mockProvider{name: "image-provider", models: []string{"dall-e-3"}},
		images:       2,
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "image-provider"}},
	})
	gw.RegisterProvider(ip)

	price := 0.04
	gw.mu.Lock()
	gw.catalog = models.Catalog{
		"image-provider/dall-e-3": {
			Provider: "image-provider", ModelID: "dall-e-3",
			Mode: models.ModeImage, Pricing: models.Pricing{ImagePerTile: &price},
		},
	}
	gw.mu.Unlock()

	obs := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(obs)
	handles := metrics.ForRequest("image-provider", "dall-e-3")
	successBefore := counterValue(t, handles.Success)
	costBefore := counterValue(t, handles.CostUSD)

	if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
		Model:  "dall-e-3",
		Prompt: "a cat",
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if got := counterValue(t, handles.Success); got != successBefore+1 {
		t.Errorf("Success counter = %v, want %v", got, successBefore+1)
	}
	wantCost := costBefore + price*2 // 2 images at $0.04/tile
	if got := counterValue(t, handles.CostUSD); got < wantCost {
		t.Errorf("CostUSD counter = %v, want at least %v", got, wantCost)
	}

	if obs.attrs.Operation != "images.generate" {
		t.Errorf("Operation = %q, want %q", obs.attrs.Operation, "images.generate")
	}
	sp := obs.rootSpan()
	if sp == nil {
		t.Fatal("expected a root span for GenerateImage")
	}
	if !sp.ended {
		t.Error("expected span.End() to be called")
	}
	if sp.cost.TotalUSD <= 0 {
		t.Error("expected span cost to be recorded")
	}
	if got, ok := sp.attrs[observability.AttrFerroRoutingTargetKey]; !ok || got != "image-provider" {
		t.Errorf("routing target key attr = %v, want image-provider", got)
	}

	evts := eventsWithSubject(obs.capturedEvents(), "gateway.request.completed")
	if len(evts) != 1 {
		t.Fatalf("expected 1 completed event, got %d", len(evts))
	}
	if evts[0].Subject != "gateway.request.completed" {
		t.Errorf("event Subject = %q, want gateway.request.completed", evts[0].Subject)
	}
	if evts[0].Cost.TotalUSD <= 0 {
		t.Error("expected event cost to be recorded")
	}
}

// TestGateway_Embed_FailurePathEmitsMetricsAndErrorEvent covers the
// recordSurfaceError side: a routing failure must still increment the
// Prometheus error counter, stamp the span with the error, and dispatch a
// failed lifecycle event. The event's Error text must be redacted using the
// same policy gateway_stream.go applies to synchronous stream-start failures.
func TestGateway_Embed_FailurePathEmitsMetricsAndErrorEvent(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "embed-provider", models: []string{"text-embedding-3-small"}},
		// Carries a credential-shaped token so the assertions below prove the
		// event text is redacted, not merely non-empty.
		err: errors.New("upstream exploded: 401 invalid api key sk-abcdefghijklmnopqrstuvwxyz012345"),
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "embed-provider"}},
	})
	gw.RegisterProvider(ep)

	obs := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(obs)
	handles := metrics.ForRequest("embed-provider", "text-embedding-3-small")
	errorsBefore := counterValue(t, handles.Error)

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	}); err == nil {
		t.Fatal("expected Embed to return an error")
	}

	if got := counterValue(t, handles.Error); got != errorsBefore+1 {
		t.Errorf("Error counter = %v, want %v", got, errorsBefore+1)
	}

	sp := obs.rootSpan()
	if sp == nil {
		t.Fatal("expected a root span for Embed")
	}
	if sp.err == nil {
		t.Error("expected span.SetError to be called")
	}

	evts := eventsWithSubject(obs.capturedEvents(), "gateway.request.failed")
	if len(evts) != 1 {
		t.Fatalf("expected 1 failed event, got %d", len(evts))
	}
	if evts[0].Subject != "gateway.request.failed" {
		t.Errorf("event Subject = %q, want gateway.request.failed", evts[0].Subject)
	}
	if evts[0].Error == "" {
		t.Error("event Error should be non-empty for a failed embedding request")
	}
	// Events reach observability exporters, which are third-party sinks. A
	// provider that echoes the offending key in its error body must not carry
	// it out of the process.
	if strings.Contains(evts[0].Error, "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Errorf("event Error leaked the provider credential unredacted: %q", evts[0].Error)
	}
	if !strings.Contains(evts[0].Error, "upstream exploded") {
		t.Errorf("event Error lost its diagnostic content to redaction: %q", evts[0].Error)
	}
}

// narrowEmbeddingProvider indexes a model its own SupportsModel refuses, the
// shape a catalog-backed model takes for a provider whose SupportsModel is a
// narrow hardcoded rule.
type narrowEmbeddingProvider struct {
	mockEmbeddingProvider
	supported []string
}

func (p *narrowEmbeddingProvider) SupportsModel(model string) bool {
	return slices.Contains(p.supported, model)
}

type narrowImageProvider struct {
	mockImageProvider
	supported []string
}

func (p *narrowImageProvider) SupportsModel(model string) bool {
	return slices.Contains(p.supported, model)
}

// TestGateway_Multimodal_IndexedModelNotInSupportsModel is the embeddings and
// images half of TestGateway_Route_IndexedModelNotInSupportsModel.
//
// A configured target must serve every model the gateway indexes — the set
// /v1/models advertises — not merely the narrower set the provider's own
// SupportsModel returns. Before v1.4.0 these two surfaces reached such a model
// only through the registry fallback, which meant the model resolved via a
// provider chosen by the index rather than by the configured strategy, and
// removing that fallback would otherwise have made them refuse a model the
// gateway advertises.
func TestGateway_Multimodal_IndexedModelNotInSupportsModel(t *testing.T) {
	const indexedModel = "mock-catalog-only"

	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "narrow"}},
	}

	t.Run("embeddings", func(t *testing.T) {
		gw, err := newTestGateway(t, cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		p := &narrowEmbeddingProvider{
			mockEmbeddingProvider: mockEmbeddingProvider{
				mockProvider: mockProvider{name: "narrow", models: []string{"mock-tiny", indexedModel}},
			},
			supported: []string{"mock-tiny"},
		}
		gw.RegisterProvider(p)
		if p.SupportsModel(indexedModel) {
			t.Fatalf("SupportsModel(%q) = true, want false — premise of this test", indexedModel)
		}

		if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: indexedModel, Input: "hi"}); err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if p.calls != 1 {
			t.Errorf("configured target calls = %d, want 1", p.calls)
		}
	})

	t.Run("images", func(t *testing.T) {
		gw, err := newTestGateway(t, cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		p := &narrowImageProvider{
			mockImageProvider: mockImageProvider{
				mockProvider: mockProvider{name: "narrow", models: []string{"mock-tiny", indexedModel}},
			},
			supported: []string{"mock-tiny"},
		}
		gw.RegisterProvider(p)

		if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: indexedModel, Prompt: "cat"}); err != nil {
			t.Fatalf("GenerateImage: %v", err)
		}
		if p.calls != 1 {
			t.Errorf("configured target calls = %d, want 1", p.calls)
		}
	})
}
