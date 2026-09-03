package aigateway

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// paritySurfaceProvider serves every routed surface, so one target set can be
// ranked on all of them and the orders compared.
type paritySurfaceProvider struct{ mockProvider }

func (p *paritySurfaceProvider) CompleteStream(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}
func (p *paritySurfaceProvider) Embed(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	return &providers.EmbeddingResponse{Model: req.Model}, nil
}
func (p *paritySurfaceProvider) GenerateImage(context.Context, providers.ImageRequest) (*providers.ImageResponse, error) {
	return &providers.ImageResponse{}, nil
}
func (p *paritySurfaceProvider) Rerank(_ context.Context, req providers.RerankRequest) (*providers.RerankResponse, error) {
	return &providers.RerankResponse{Model: req.Model}, nil
}
func (p *paritySurfaceProvider) Moderate(_ context.Context, req providers.ModerationRequest) (*providers.ModerationResponse, error) {
	return &providers.ModerationResponse{Model: req.Model}, nil
}
func (p *paritySurfaceProvider) Transcribe(context.Context, providers.TranscriptionRequest) (*providers.TranscriptionResponse, error) {
	return &providers.TranscriptionResponse{Text: "ok"}, nil
}
func (p *paritySurfaceProvider) Speech(context.Context, providers.SpeechRequest) (*providers.SpeechResponse, error) {
	return &providers.SpeechResponse{Audio: []byte("ok"), ContentType: "audio/mpeg"}, nil
}

// parityCase is one target/model/health snapshot. wantOrder is the exact order
// every surface must produce; when the mode draws at random, randomised is set
// and the suite checks the candidate set and the never-first list instead.
type parityCase struct {
	name       string
	strategy   config.StrategyConfig
	targets    []config.Target
	catalog    models.Catalog
	latency    map[string]time.Duration
	model      string
	wantOrder  []string
	randomised bool
	neverFirst []string
}

const (
	parityModel   = "parity-model"
	parityModelNo = "parity-other-model"
)

func embeddingPrice(price float64) models.Model {
	return models.Model{Mode: models.ModeEmbedding, Pricing: models.Pricing{EmbeddingPerMTokens: ptrFloat64(price)}}
}

func parityCases() []parityCase {
	four := []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}, {VirtualKey: "d"}}
	return []parityCase{
		{
			name:      "single",
			strategy:  config.StrategyConfig{Mode: config.ModeSingle},
			targets:   four,
			model:     parityModel,
			wantOrder: []string{"a"},
		},
		{
			name:      "fallback keeps declared order",
			strategy:  config.StrategyConfig{Mode: config.ModeFallback},
			targets:   four,
			model:     parityModel,
			wantOrder: []string{"a", "b", "c", "d"},
		},
		{
			name:     "conditional rule names its target",
			strategy: config.StrategyConfig{Mode: config.ModeConditional, Conditions: []config.Condition{{Key: config.ConditionKeyModel, Value: parityModel, TargetKey: "c"}}},
			targets:  four,
			model:    parityModel,
			// The chain is the whole answer: nothing outside it is substituted.
			wantOrder: []string{"c"},
		},
		{
			// d serves a different model: it is a candidate nowhere, so it ends
			// the order on every surface, and never leads it.
			name:       "loadbalance never starts on a zero-weight target",
			strategy:   config.StrategyConfig{Mode: config.ModeLoadBalance},
			targets:    []config.Target{{VirtualKey: "a", Weight: 0}, {VirtualKey: "b", Weight: 50}, {VirtualKey: "c", Weight: 50}, {VirtualKey: "d", Weight: 50}},
			model:      parityModel,
			randomised: true,
			neverFirst: []string{"a", "d"},
		},
		{
			name:     "least-latency: unseen first, then ascending p50, then the tail",
			strategy: config.StrategyConfig{Mode: config.ModeLatency},
			targets:  four,
			latency:  map[string]time.Duration{"a": 300 * time.Millisecond, "b": 100 * time.Millisecond},
			model:    parityModel,
			// c is the only unseen target, so the unseen block has one order.
			wantOrder: []string{"c", "b", "a", "d"},
		},
		{
			name:     "cost-optimized fallback: priced ascending, unpriced excluded, tail appended",
			strategy: config.StrategyConfig{Mode: config.ModeCostOptimized},
			targets:  four,
			catalog: models.Catalog{
				"a/" + parityModel: embeddingPrice(10),
				"b/" + parityModel: embeddingPrice(1),
			},
			model:     parityModel,
			wantOrder: []string{"b", "a", "c", "d"},
		},
		{
			name:     "cost-optimized allow: unpriced ranks at zero among priced",
			strategy: config.StrategyConfig{Mode: config.ModeCostOptimized, UnpricedStrategy: config.UnpricedStrategyAllow},
			targets:  four,
			catalog: models.Catalog{
				"a/" + parityModel: embeddingPrice(10),
				"b/" + parityModel: embeddingPrice(1),
				"c/" + parityModel: {Mode: models.ModeEmbedding},
			},
			model:     parityModel,
			wantOrder: []string{"c", "b", "a", "d"},
		},
		{
			name:     "cost-optimized skip: only priced rank, unpriced follow as the tail",
			strategy: config.StrategyConfig{Mode: config.ModeCostOptimized, UnpricedStrategy: config.UnpricedStrategySkip},
			targets:  four,
			catalog: models.Catalog{
				"a/" + parityModel: embeddingPrice(10),
				"b/" + parityModel: embeddingPrice(1),
			},
			model:     parityModel,
			wantOrder: []string{"b", "a", "c", "d"},
		},
		{
			name:       "ab-test draws only from its variants",
			strategy:   config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{{TargetKey: "a", Weight: 50, Label: "x"}, {TargetKey: "b", Weight: 50, Label: "y"}}},
			targets:    four,
			model:      parityModel,
			randomised: true,
			neverFirst: []string{"c", "d"},
		},
	}
}

// parityGateway builds one gateway per case: targets a, b, c serve the model,
// d serves another one, and every target serves every surface.
func parityGateway(t *testing.T, tc parityCase) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{Strategy: tc.strategy, Targets: tc.targets})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	if tc.catalog != nil {
		gw.mu.Lock()
		gw.catalog = tc.catalog
		gw.mu.Unlock()
	}
	for _, target := range tc.targets {
		model := parityModel
		if target.VirtualKey == "d" {
			model = parityModelNo
		}
		gw.RegisterProvider(&paritySurfaceProvider{mockProvider{name: target.VirtualKey, models: []string{model}}})
	}
	for key, d := range tc.latency {
		gw.latencyTracker.Record(key, parityModel, d)
	}
	return gw
}

// paritySurfaces lists every routed surface and how it asks for its order.
// chat and stream start both resolve through the strategy directly; the
// others go through surfaceTargetOrder.
func paritySurfaces() map[string]func(t *testing.T, gw *Gateway, model string) []string {
	viaStrategy := func(t *testing.T, gw *Gateway, model string) []string {
		return streamTargetOrder(t, gw, providers.Request{Model: model})
	}
	viaSurface := func(surface string) func(t *testing.T, gw *Gateway, model string) []string {
		return func(t *testing.T, gw *Gateway, model string) []string {
			t.Helper()
			keys, err := gw.surfaceTargetOrder(providers.Request{Model: model}, surface)
			if err != nil {
				t.Fatalf("surfaceTargetOrder: %v", err)
			}
			return keys
		}
	}
	return map[string]func(t *testing.T, gw *Gateway, model string) []string{
		"chat":          viaStrategy,
		"stream":        viaStrategy,
		"embeddings":    viaSurface(surfaceEmbeddings),
		"images":        viaSurface(surfaceImages),
		"rerank":        viaSurface(surfaceRerank),
		"moderation":    viaSurface(surfaceModeration),
		"transcription": viaSurface(surfaceTranscription),
		"speech":        viaSurface(surfaceSpeech),
	}
}

// TestRankingParity_EverySurfaceOrdersTheSame is release gate 2 of v1.5.2: for
// one target/model/health snapshot, chat, stream start, embeddings, images,
// rerank, moderation, transcription and speech produce the same candidate
// order, with a zero-weight and an unpriced target among the cases.
func TestRankingParity_EverySurfaceOrdersTheSame(t *testing.T) {
	for _, tc := range parityCases() {
		t.Run(tc.name, func(t *testing.T) {
			gw := parityGateway(t, tc)
			reference := streamTargetOrder(t, gw, providers.Request{Model: tc.model})
			for surface, order := range paritySurfaces() {
				t.Run(surface, func(t *testing.T) {
					if !tc.randomised {
						requireKeys(t, order(t, gw, tc.model), tc.wantOrder...)
						return
					}
					const draws = 200
					for range draws {
						got := order(t, gw, tc.model)
						if len(got) != len(reference) {
							t.Fatalf("order %v, want the same %d candidates as chat %v", got, len(reference), reference)
						}
						for _, key := range reference {
							if !slices.Contains(got, key) {
								t.Fatalf("order %v lacks %q, which chat ranks: %v", got, key, reference)
							}
						}
						if slices.Contains(tc.neverFirst, got[0]) {
							t.Fatalf("order %v starts on %q, which must never lead", got, got[0])
						}
					}
				})
			}
		})
	}
}
