package aigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
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
