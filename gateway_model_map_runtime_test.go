package aigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

const visibleMappedModel = "support-chat"

type mappedSurfaceProvider struct {
	mockProvider
	wantModel string
	fail      bool
	seen      []string
}

func newMappedSurfaceProvider(name, wantModel string, fail bool) *mappedSurfaceProvider {
	p := &mappedSurfaceProvider{wantModel: wantModel, fail: fail}
	p.name = name
	p.models = []string{wantModel}
	return p
}

func (p *mappedSurfaceProvider) record(model string) error {
	p.seen = append(p.seen, model)
	if p.fail {
		return errors.New("mapped target failed")
	}
	return nil
}

func (p *mappedSurfaceProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	return &providers.Response{Model: p.wantModel}, nil
}
func (p *mappedSurfaceProvider) CompleteStream(_ context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{Model: p.wantModel}
	close(ch)
	return ch, nil
}
func (p *mappedSurfaceProvider) Embed(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	return &providers.EmbeddingResponse{Model: p.wantModel}, nil
}
func (p *mappedSurfaceProvider) GenerateImage(_ context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	return &providers.ImageResponse{}, nil
}
func (p *mappedSurfaceProvider) Rerank(_ context.Context, req providers.RerankRequest) (*providers.RerankResponse, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	return &providers.RerankResponse{Model: p.wantModel}, nil
}
func (p *mappedSurfaceProvider) Moderate(_ context.Context, req providers.ModerationRequest) (*providers.ModerationResponse, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	return &providers.ModerationResponse{Model: p.wantModel}, nil
}
func (p *mappedSurfaceProvider) Transcribe(_ context.Context, req providers.TranscriptionRequest) (*providers.TranscriptionResponse, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	return &providers.TranscriptionResponse{Text: "ok"}, nil
}
func (p *mappedSurfaceProvider) Speech(_ context.Context, req providers.SpeechRequest) (*providers.SpeechResponse, error) {
	if err := p.record(req.Model); err != nil {
		return nil, err
	}
	return &providers.SpeechResponse{Audio: []byte("ok")}, nil
}

func mappedRuntimeGateway(t *testing.T) (*Gateway, *mappedSurfaceProvider, *mappedSurfaceProvider) {
	t.Helper()
	first := newMappedSurfaceProvider("first", "upstream-a", true)
	second := newMappedSurfaceProvider("second", "upstream-b", false)
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "first", ModelMap: map[string]string{visibleMappedModel: "upstream-a"}},
			{VirtualKey: "second", ModelMap: map[string]string{visibleMappedModel: "upstream-b"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(first)
	gw.RegisterProvider(second)
	return gw, first, second
}

func TestRouteTargets_MapsEachTypedSurfaceWithoutMutatingCaller(t *testing.T) {
	tests := []struct {
		name string
		call func(*Gateway) error
	}{
		{"chat", func(g *Gateway) error {
			req := providers.Request{Model: visibleMappedModel}
			_, err := g.Route(context.Background(), req)
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
		{"stream", func(g *Gateway) error {
			req := providers.Request{Model: visibleMappedModel}
			ch, err := g.RouteStream(context.Background(), req)
			if err == nil {
				drainStream(t, ch)
			}
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
		{"embeddings", func(g *Gateway) error {
			req := providers.EmbeddingRequest{Model: visibleMappedModel, Input: "x"}
			_, err := g.Embed(context.Background(), req)
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
		{"images", func(g *Gateway) error {
			req := providers.ImageRequest{Model: visibleMappedModel, Prompt: "x"}
			_, err := g.GenerateImage(context.Background(), req)
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
		{"rerank", func(g *Gateway) error {
			req := providers.RerankRequest{Model: visibleMappedModel, Query: "x", Documents: []string{"x"}}
			_, err := g.Rerank(context.Background(), req)
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
		{"moderation", func(g *Gateway) error {
			req := providers.ModerationRequest{Model: visibleMappedModel, Input: "x"}
			_, err := g.Moderate(context.Background(), req)
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
		{"transcription", func(g *Gateway) error {
			req := providers.TranscriptionRequest{Model: visibleMappedModel, File: []byte("x")}
			_, err := g.Transcribe(context.Background(), req)
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
		{"speech", func(g *Gateway) error {
			req := providers.SpeechRequest{Model: visibleMappedModel, Input: "x", Voice: "alloy"}
			_, err := g.Speech(context.Background(), req)
			if req.Model != visibleMappedModel {
				t.Errorf("caller model = %q", req.Model)
			}
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gw, first, second := mappedRuntimeGateway(t)
			if err := tc.call(gw); err != nil {
				t.Fatalf("route: %v", err)
			}
			if len(first.seen) != 1 || first.seen[0] != "upstream-a" {
				t.Errorf("first saw %v", first.seen)
			}
			if len(second.seen) != 1 || second.seen[0] != "upstream-b" {
				t.Errorf("second saw %v", second.seen)
			}
		})
	}
}

func TestMappedModel_RemainsVisibleToPlugins(t *testing.T) {
	gw, _, _ := mappedRuntimeGateway(t)
	var seen string
	if err := gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{name: "visible-model", typ: plugin.TypeGuardrail, execFn: func(_ context.Context, pctx *plugin.Context) error {
		seen = pctx.Request.Model
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Route(context.Background(), providers.Request{Model: visibleMappedModel}); err != nil {
		t.Fatal(err)
	}
	if seen != visibleMappedModel {
		t.Fatalf("plugin saw %q, want %q", seen, visibleMappedModel)
	}
}

func TestModelMap_TerminalAttributionMatchesUnaryAndPricesStreamUpstream(t *testing.T) {
	const upstreamModel = "vendor/support-v2"
	pricedCatalog := models.Catalog{
		"mock/" + upstreamModel: {
			Provider: "mock",
			ModelID:  upstreamModel,
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrFloat64(5),
				OutputPerMTokens: ptrFloat64(15),
			},
		},
	}

	for _, tc := range []struct {
		name          string
		streamFailure error
		wantSubject   string
	}{
		{name: "completed", wantSubject: "gateway.request.completed"},
		{name: "failed", streamFailure: errors.New("mid-stream failure"), wantSubject: "gateway.request.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets: []config.Target{{
					VirtualKey: "mock",
					ModelMap:   map[string]string{visibleMappedModel: upstreamModel},
				}},
			})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			gw.catalog = pricedCatalog
			ep := &eventCapturingProvider{recordingActive: true}
			gw.SetObservability(ep)
			gw.RegisterProvider(&mockStreamProvider{
				mockProvider: mockProvider{
					name:   "mock",
					models: []string{upstreamModel},
					resp: &providers.Response{
						Model: upstreamModel,
						Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50},
					},
				},
				streamFn: func(_ context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
					if req.Model != upstreamModel {
						t.Errorf("stream provider model = %q, want %q", req.Model, upstreamModel)
					}
					ch := make(chan providers.StreamChunk, 2)
					ch <- providers.StreamChunk{Model: upstreamModel, Usage: &providers.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}}
					if tc.streamFailure != nil {
						ch <- providers.StreamChunk{Error: tc.streamFailure}
					}
					close(ch)
					return ch, nil
				},
			})

			if _, err := gw.Route(context.Background(), providers.Request{Model: visibleMappedModel}); err != nil {
				t.Fatalf("Route: %v", err)
			}
			unary := eventsWithSubject(ep.capturedEvents(), "gateway.request.completed")
			if len(unary) != 1 || unary[0].Model != visibleMappedModel {
				t.Fatalf("unary terminal events = %#v, want model %q", unary, visibleMappedModel)
			}

			ch, err := gw.RouteStream(context.Background(), providers.Request{Model: visibleMappedModel, Stream: true})
			if err != nil {
				t.Fatalf("RouteStream: %v", err)
			}
			for chunk := range ch {
				_ = chunk
			}

			terminals := eventsWithSubject(ep.capturedEvents(), tc.wantSubject)
			if tc.wantSubject == "gateway.request.completed" {
				terminals = terminals[1:]
			}
			if len(terminals) != 1 || terminals[0].Model != visibleMappedModel {
				t.Fatalf("stream terminal events = %#v, want model %q", terminals, visibleMappedModel)
			}
			attempts := eventsWithSubject(ep.capturedEvents(), observability.SubjectRoutingAttempt)
			last := attempts[len(attempts)-1].RoutingAttempt
			if last.RoutedModel != visibleMappedModel || last.UpstreamModel != upstreamModel {
				t.Errorf("attempt models = (%q, %q), want (%q, %q)", last.RoutedModel, last.UpstreamModel, visibleMappedModel, upstreamModel)
			}
			if tc.streamFailure == nil {
				if terminals[0].Cost.TotalUSD != aliasPricingWantCostUSD {
					t.Errorf("stream cost = %v, want %v", terminals[0].Cost.TotalUSD, aliasPricingWantCostUSD)
				}
			}
		})
	}
}

// The routed model is the response's identity on every surface: the chunks the
// client receives name it, exactly as a non-streaming response does, so the
// upstream id a model_map translates to never reaches the caller.
func TestRouteStream_ModelMapKeepsRoutedModelOnChunksInAfterPluginAndSpan(t *testing.T) {
	const upstreamModel = "vendor/support-v2"
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "mock",
			ModelMap:   map[string]string{visibleMappedModel: upstreamModel},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	var afterModel string
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "capture-response-model",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			afterModel = pctx.Response.Model
			return nil
		},
	}); err != nil {
		t.Fatalf("register after plugin: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "mock", models: []string{upstreamModel}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{Model: upstreamModel}
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{Model: visibleMappedModel, Stream: true})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	var forwardedModel string
	for chunk := range ch {
		forwardedModel = chunk.Model
	}

	if forwardedModel != visibleMappedModel {
		t.Errorf("forwarded chunk model = %q, want routed model %q", forwardedModel, visibleMappedModel)
	}
	if afterModel != visibleMappedModel {
		t.Errorf("after_request response model = %q, want %q", afterModel, visibleMappedModel)
	}
	span := fp.rootSpan()
	if span == nil {
		t.Fatal("no root span was started")
	}
	span.mu.Lock()
	responseModel := span.attrs[observability.AttrGenAIResponseModel]
	span.mu.Unlock()
	if responseModel != visibleMappedModel {
		t.Errorf("%s = %v, want %q", observability.AttrGenAIResponseModel, responseModel, visibleMappedModel)
	}
}

func TestRoute_ModelMapIgnoresProviderReturnedModelForIdentityAndPricing(t *testing.T) {
	const (
		upstreamModel = "vendor/support-v2"
		providerModel = "provider/third-id"
	)
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "mock",
			ModelMap:   map[string]string{visibleMappedModel: upstreamModel},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.catalog = models.Catalog{
		"mock/" + upstreamModel: {
			Provider: "mock", ModelID: upstreamModel, Mode: models.ModeChat,
			Pricing: models.Pricing{InputPerMTokens: ptrFloat64(5), OutputPerMTokens: ptrFloat64(15)},
		},
		"mock/" + providerModel: {
			Provider: "mock", ModelID: providerModel, Mode: models.ModeChat,
			Pricing: models.Pricing{InputPerMTokens: ptrFloat64(50), OutputPerMTokens: ptrFloat64(150)},
		},
	}

	var providerInput, afterModel string
	gw.RegisterProvider(&mockProvider{
		name: "mock", models: []string{upstreamModel},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			providerInput = req.Model
			return &providers.Response{Model: providerModel, Provider: "mock", Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50}}, nil
		},
	})
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{name: "capture", typ: plugin.TypeLogging, execFn: func(_ context.Context, pctx *plugin.Context) error {
		afterModel = pctx.Response.Model
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)

	resp, err := gw.Route(context.Background(), providers.Request{Model: visibleMappedModel})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if providerInput != upstreamModel {
		t.Errorf("provider input model = %q, want %q", providerInput, upstreamModel)
	}
	if resp.Model != visibleMappedModel || afterModel != visibleMappedModel {
		t.Errorf("response/after models = (%q, %q), want %q", resp.Model, afterModel, visibleMappedModel)
	}
	completed := eventsWithSubject(ep.capturedEvents(), "gateway.request.completed")
	if len(completed) != 1 || completed[0].Model != visibleMappedModel {
		t.Fatalf("completed events = %#v, want routed model %q", completed, visibleMappedModel)
	}
	if completed[0].Cost.TotalUSD != aliasPricingWantCostUSD {
		t.Errorf("cost = %v, want upstream-model price %v", completed[0].Cost.TotalUSD, aliasPricingWantCostUSD)
	}
	span := ep.rootSpan()
	span.mu.Lock()
	spanModel := span.attrs[observability.AttrGenAIResponseModel]
	span.mu.Unlock()
	if spanModel != visibleMappedModel {
		t.Errorf("span model = %v, want %q", spanModel, visibleMappedModel)
	}
}

func TestTypedSurfaces_ModelMapNormalizesProviderReturnedModel(t *testing.T) {
	const (
		upstreamModel = "vendor/typed-v2"
		providerModel = "provider/third-id"
	)
	tests := []struct {
		name string
		call func(*Gateway) (string, error)
	}{
		{name: "embeddings", call: func(g *Gateway) (string, error) {
			resp, err := g.Embed(context.Background(), providers.EmbeddingRequest{Model: visibleMappedModel, Input: "x"})
			if err != nil {
				return "", err
			}
			return resp.Model, nil
		}},
		{name: "rerank", call: func(g *Gateway) (string, error) {
			resp, err := g.Rerank(context.Background(), providers.RerankRequest{Model: visibleMappedModel, Query: "x", Documents: []string{"x"}})
			if err != nil {
				return "", err
			}
			return resp.Model, nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := newMappedSurfaceProvider("mock", upstreamModel, false)
			gw, err := newTestGateway(t, config.Config{Strategy: config.StrategyConfig{Mode: config.ModeSingle}, Targets: []config.Target{{VirtualKey: "mock", ModelMap: map[string]string{visibleMappedModel: upstreamModel}}}})
			if err != nil {
				t.Fatal(err)
			}
			gw.RegisterProvider(provider)
			provider.wantModel = providerModel
			var afterModel string
			if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{name: "capture", typ: plugin.TypeLogging, execFn: func(_ context.Context, pctx *plugin.Context) error {
				afterModel = pctx.Response.Model
				return nil
			}}); err != nil {
				t.Fatal(err)
			}

			got, err := tc.call(gw)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(provider.seen) != 1 || provider.seen[0] != upstreamModel {
				t.Errorf("provider inputs = %v, want [%q]", provider.seen, upstreamModel)
			}
			if got != visibleMappedModel || afterModel != visibleMappedModel {
				t.Errorf("response/after models = (%q, %q), want %q", got, afterModel, visibleMappedModel)
			}
		})
	}
}

func TestRoute_AliasResolvesBeforeTargetModelMapWithoutMutatingCaller(t *testing.T) {
	const (
		alias    = "support"
		upstream = "vendor/support-v2"
	)
	provider := newMappedSurfaceProvider("mock", upstream, false)
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Aliases:  map[string]string{alias: visibleMappedModel},
		Targets: []config.Target{{
			VirtualKey: "mock",
			ModelMap:   map[string]string{visibleMappedModel: upstream},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(provider)
	req := providers.Request{Model: alias}
	if _, err := gw.Route(context.Background(), req); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if req.Model != alias {
		t.Errorf("caller model = %q, want %q", req.Model, alias)
	}
	if len(provider.seen) != 1 || provider.seen[0] != upstream {
		t.Errorf("provider models = %v, want [%q]", provider.seen, upstream)
	}
}

// A reload replaces the target model maps along with the config: the next
// request is translated with the new mapping, not the one the gateway was
// built with.
func TestReloadConfig_AppliesTheNewModelMap(t *testing.T) {
	const (
		before = "vendor/before"
		after  = "vendor/after"
	)
	configWith := func(upstream string) config.Config {
		return config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "mock", ModelMap: map[string]string{visibleMappedModel: upstream}}},
		}
	}
	gw, err := newTestGateway(t, configWith(before))
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	var providerInput string
	gw.RegisterProvider(&mockProvider{
		name: "mock", models: []string{before, after},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			providerInput = req.Model
			return &providers.Response{Model: req.Model, Provider: "mock"}, nil
		},
	})

	if _, err := gw.Route(context.Background(), providers.Request{Model: visibleMappedModel}); err != nil {
		t.Fatalf("Route before reload: %v", err)
	}
	if providerInput != before {
		t.Fatalf("provider input before reload = %q, want %q", providerInput, before)
	}

	if err := gw.ReloadConfig(context.Background(), configWith(after)); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if _, err := gw.Route(context.Background(), providers.Request{Model: visibleMappedModel}); err != nil {
		t.Fatalf("Route after reload: %v", err)
	}
	if providerInput != after {
		t.Errorf("provider input after reload = %q, want the reloaded mapping %q", providerInput, after)
	}
}
