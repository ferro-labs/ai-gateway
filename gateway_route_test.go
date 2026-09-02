package aigateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestGateway_RouteStreamMatchesRouteTargetOrder asserts Route (Strategy.Execute) and
// RouteStream (Strategy.SelectTargets) resolve consistently per strategy: for
// deterministic strategies both pick the same first target and SelectTargets
// exposes every configured target; for weighted-random strategies both pick
// within the configured target set from the one shared selection implementation.
func TestGateway_RouteStreamMatchesRouteTargetOrder(t *testing.T) {
	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "please write code"}},
	}

	tests := []struct {
		name          string
		strategy      config.StrategyConfig
		targets       []config.Target
		deterministic bool
		wantFirst     string
		// exposesAllTargets is false only for single, which intentionally offers
		// no fallbacks — every other strategy appends the remaining targets.
		exposesAllTargets bool
	}{
		{
			name:          "single",
			strategy:      config.StrategyConfig{Mode: config.ModeSingle},
			targets:       []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			deterministic: true,
			wantFirst:     "a",
		},
		{
			name:              "fallback",
			strategy:          config.StrategyConfig{Mode: config.ModeFallback},
			targets:           []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}},
			deterministic:     true,
			wantFirst:         "a",
			exposesAllTargets: true,
		},
		{
			name:          "conditional",
			strategy:      config.StrategyConfig{Mode: config.ModeConditional, Conditions: []config.Condition{{Key: "model", Value: "gpt-4o", TargetKey: "b"}}},
			targets:       []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			deterministic: true,
			wantFirst:     "b",
			// The rule's chain is the whole answer; nothing outside it is exposed.
			exposesAllTargets: false,
		},
		{
			name:          "content-based",
			strategy:      config.StrategyConfig{Mode: config.ModeContentBased, ContentConditions: []config.ContentCondition{{Type: "prompt_contains", Value: "code", TargetKey: "b"}}},
			targets:       []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			deterministic: true,
			wantFirst:     "b",
			// The rule's chain is the whole answer; nothing outside it is exposed.
			exposesAllTargets: false,
		},
		{
			name:              "load-balance",
			strategy:          config.StrategyConfig{Mode: config.ModeLoadBalance},
			targets:           []config.Target{{VirtualKey: "a", Weight: 1}, {VirtualKey: "b", Weight: 1}},
			exposesAllTargets: true,
		},
		{
			name:              "ab-test",
			strategy:          config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{{TargetKey: "a", Weight: 1, Label: "control"}, {TargetKey: "b", Weight: 1, Label: "challenger"}}},
			targets:           []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			exposesAllTargets: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{Strategy: tt.strategy, Targets: tt.targets})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			var served string
			for _, tgt := range tt.targets {
				name := tgt.VirtualKey
				gw.RegisterProvider(&mockStreamProvider{
					mockProvider: mockProvider{name: name, models: []string{"gpt-4o"}, resp: &providers.Response{ID: name, Model: "gpt-4o"}},
					streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
						served = name
						ch := make(chan providers.StreamChunk)
						close(ch)
						return ch, nil
					},
				})
			}

			order := streamTargetOrder(t, gw, req)
			if tt.exposesAllTargets {
				assertSameTargetSet(t, order, tt.targets)
			}

			resp, err := gw.Route(context.Background(), req)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			ch, err := gw.RouteStream(context.Background(), req)
			if err != nil {
				t.Fatalf("RouteStream: %v", err)
			}
			drainStream(t, ch)

			if tt.deterministic {
				if order[0] != tt.wantFirst {
					t.Fatalf("SelectTargets first = %q, want %q", order[0], tt.wantFirst)
				}
				if resp.Provider != tt.wantFirst {
					t.Fatalf("Route provider = %q, want %q", resp.Provider, tt.wantFirst)
				}
				if served != tt.wantFirst {
					t.Fatalf("RouteStream served %q, want %q", served, tt.wantFirst)
				}
				return
			}
			assertInTargets(t, resp.Provider, tt.targets)
			assertInTargets(t, served, tt.targets)
		})
	}
}

func TestGateway_Route_Single(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
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

func TestGateway_Route_NormalizesCompletionTokenLimitsBeforePlugins(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Plugins: []config.PluginConfig{
			{
				Name:    "max-token",
				Enabled: true,
				Stage:   "before_request",
				Config:  map[string]any{"max_tokens": 100},
			},
		},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o"},
	})
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}

	maxCompletionTokens := 200
	_, err := gw.Route(context.Background(), providers.Request{
		Model:               "gpt-4o",
		Messages:            []providers.Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: &maxCompletionTokens,
	})
	if err == nil {
		t.Fatal("expected max-token plugin rejection")
	}
	if !strings.Contains(err.Error(), "max_tokens 200 exceeds limit of 100") {
		t.Fatalf("error = %q, want max-token rejection with normalized max_tokens", err.Error())
	}
}

func TestGateway_Route_NormalizesCompletionTokenLimitsBeforeProvider(t *testing.T) {
	var captured providers.Request
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			captured = req
			return &providers.Response{ID: "r1", Model: req.Model}, nil
		},
	})

	maxCompletionTokens := 17
	resp, err := gw.Route(context.Background(), providers.Request{
		Model:               "gpt-4o",
		Messages:            []providers.Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: &maxCompletionTokens,
	})
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("response ID = %q, want r1", resp.ID)
	}
	if captured.MaxTokens == nil || *captured.MaxTokens != maxCompletionTokens {
		t.Fatalf("captured MaxTokens = %v, want %d", captured.MaxTokens, maxCompletionTokens)
	}
	if captured.MaxCompletionTokens == nil || *captured.MaxCompletionTokens != maxCompletionTokens {
		t.Fatalf("captured MaxCompletionTokens = %v, want preserved %d", captured.MaxCompletionTokens, maxCompletionTokens)
	}
}

// intTokens returns a pointer to a token-count literal, for the pointer-typed
// completion-length fields.
func intTokens(v int) *int { return &v }

// TestGateway_TokenCeilingIsOneValueAcrossBothFields covers a request that
// expresses its completion length in both fields at once.
//
// max_tokens 5 with max_completion_tokens 500000 used to pass a cap of 10 and
// then have 500000 forwarded upstream: the guardrail read max_tokens, and
// providers on the OpenAI API surface forward max_completion_tokens and clear
// max_tokens. Both routing surfaces reconcile the pair before any plugin runs,
// so the value a guardrail approves is the value that travels.
func TestGateway_TokenCeilingIsOneValueAcrossBothFields(t *testing.T) {
	tests := []struct {
		name                string
		maxTokens           *int
		maxCompletionTokens *int
		wantReject          bool
		wantForwarded       int
	}{
		{
			name:                "both fields, the larger over the cap",
			maxTokens:           intTokens(5),
			maxCompletionTokens: intTokens(500000),
			wantReject:          true,
		},
		{
			// max_completion_tokens supersedes max_tokens, so this request can
			// only obtain 5 tokens; the superseded 500000 must not travel.
			name:                "huge max_tokens superseded by a small max_completion_tokens",
			maxTokens:           intTokens(500000),
			maxCompletionTokens: intTokens(5),
			wantForwarded:       5,
		},
		{
			name:                "both fields under the cap: max_completion_tokens travels in both",
			maxTokens:           intTokens(4),
			maxCompletionTokens: intTokens(9),
			wantForwarded:       9,
		},
		{
			name:                "only max_completion_tokens: it travels in both fields",
			maxCompletionTokens: intTokens(7),
			wantForwarded:       7,
		},
	}

	for _, tt := range tests {
		for _, surface := range []string{"chat", "stream"} {
			t.Run(tt.name+"/"+surface, func(t *testing.T) {
				var captured providers.Request
				var called bool
				gw, _ := newTestGateway(t, config.Config{
					Strategy: config.StrategyConfig{Mode: config.ModeSingle},
					Targets:  []config.Target{{VirtualKey: mockProviderName}},
					Plugins: []config.PluginConfig{{
						Name:    "max-token",
						Enabled: true,
						Stage:   "before_request",
						Config:  map[string]any{"max_tokens": 10},
					}},
				})
				gw.RegisterProvider(&mockStreamProvider{
					mockProvider: mockProvider{
						name:   mockProviderName,
						models: []string{"gpt-4o"},
						completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
							captured, called = req, true
							return &providers.Response{ID: "r1", Model: req.Model}, nil
						},
					},
					streamFn: func(_ context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
						captured, called = req, true
						ch := make(chan providers.StreamChunk)
						close(ch)
						return ch, nil
					},
				})
				if err := gw.LoadPlugins(); err != nil {
					t.Fatalf("LoadPlugins failed: %v", err)
				}

				req := providers.Request{
					Model:               "gpt-4o",
					Messages:            []providers.Message{{Role: "user", Content: "hi"}},
					MaxTokens:           tt.maxTokens,
					MaxCompletionTokens: tt.maxCompletionTokens,
				}
				var err error
				if surface == "chat" {
					_, err = gw.Route(context.Background(), req)
				} else {
					req.Stream = true
					var ch <-chan providers.StreamChunk
					// A rejected stream returns a nil channel; only drain one
					// that exists.
					if ch, err = gw.RouteStream(context.Background(), req); err == nil {
						drainStream(t, ch)
					}
				}

				if tt.wantReject {
					if err == nil {
						t.Fatal("expected the max-token guardrail to reject the request")
					}
					if !strings.Contains(err.Error(), "exceeds limit of 10") {
						t.Fatalf("error = %q, want a max-token rejection", err.Error())
					}
					if called {
						t.Errorf("provider was called with max_tokens=%v max_completion_tokens=%v; a rejected request must not reach it",
							captured.MaxTokens, captured.MaxCompletionTokens)
					}
					return
				}

				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !called {
					t.Fatal("provider was never called")
				}
				if captured.MaxTokens == nil || *captured.MaxTokens != tt.wantForwarded {
					t.Errorf("forwarded max_tokens = %v, want %d", captured.MaxTokens, tt.wantForwarded)
				}
				if captured.MaxCompletionTokens == nil || *captured.MaxCompletionTokens != tt.wantForwarded {
					t.Errorf("forwarded max_completion_tokens = %v, want %d", captured.MaxCompletionTokens, tt.wantForwarded)
				}
			})
		}
	}
}

func TestGateway_Route_Fallback(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
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

func TestGateway_Route_CostOptimizedPassesUnpricedStrategy(t *testing.T) {
	tests := []struct {
		name             string
		unpricedStrategy string
		wantProvider     string
	}{
		{
			name:             "skip routes to priced provider",
			unpricedStrategy: config.UnpricedStrategySkip,
			wantProvider:     "priced",
		},
		{
			name:             "allow routes to unpriced provider",
			unpricedStrategy: config.UnpricedStrategyAllow,
			wantProvider:     "unpriced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{
					Mode:             config.ModeCostOptimized,
					UnpricedStrategy: tt.unpricedStrategy,
				},
				Targets: []config.Target{
					{VirtualKey: "unpriced"},
					{VirtualKey: "priced"},
				},
			})

			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gw.catalog = models.Catalog{
				"unpriced/gpt-4o": {
					Provider: "unpriced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing:  models.Pricing{},
				},
				"priced/gpt-4o": {
					Provider: "priced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing: models.Pricing{
						InputPerMTokens: ptrFloat64(1.0),
					},
				},
			}
			gw.RegisterProvider(&mockProvider{
				name:   "unpriced",
				models: []string{"gpt-4o"},
				resp:   &providers.Response{ID: "unpriced", Model: "gpt-4o"},
			})
			gw.RegisterProvider(&mockProvider{
				name:   "priced",
				models: []string{"gpt-4o"},
				resp:   &providers.Response{ID: "priced", Model: "gpt-4o"},
			})

			resp, err := gw.Route(context.Background(), providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello"}},
			})
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if resp.Provider != tt.wantProvider {
				t.Fatalf("got provider %q, want %q", resp.Provider, tt.wantProvider)
			}
			if resp.ID != tt.wantProvider {
				t.Fatalf("got response ID %q, want %q", resp.ID, tt.wantProvider)
			}
		})
	}
}

func TestGateway_Route_NoTargets(t *testing.T) {
	// After #256, New() validates the config eagerly, so the "no targets"
	// error is now returned at construction time rather than at route time.
	_, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
	})

	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

// wildcardProvider is the non-streaming counterpart of wildcardStreamProvider:
// it declares core.AnyModelProvider and echoes the caller's string back in the
// response, the way ollama and databricks do.
type wildcardProvider struct {
	mockProvider
}

func (p *wildcardProvider) SupportsModel(string) bool { return true }
func (p *wildcardProvider) ServesAnyModel()           {}

// newWildcardGateway returns a gateway whose only target answers any model with
// a response naming that same model.
func newWildcardGateway(t *testing.T) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&wildcardProvider{mockProvider: mockProvider{
		name:   mockProviderName,
		models: []string{"known-model"},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			return &providers.Response{ID: "resp-1", Model: req.Model}, nil
		},
	}})
	return gw
}

// The non-streaming success path stamps the provider-reported model, which a
// wildcard provider sources from the caller — one new time series per request
// unless the label is bucketed.
func TestGateway_Route_SuccessBucketsMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-route-success"
	if requestMetricLabelExists(t, mockProviderName, rawModel, "success") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw := newWildcardGateway(t)
	unknownCounter := metrics.ForRequest(mockProviderName, metrics.UnknownModelLabel).Success
	before := counterValue(t, unknownCounter)

	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown success counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, mockProviderName, rawModel, "success") {
		t.Fatalf("raw success model label %q should not be created", rawModel)
	}
}

// An after_request rejection counts the response's model, so it carries the
// same raw value the success path does.
func TestGateway_Route_AfterPluginRejectsBucketsMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-after-reject"
	if requestMetricLabelExists(t, mockProviderName, rawModel, "rejected") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw := newWildcardGateway(t)
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after-blocker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "blocked"
			return nil
		},
	})

	unknownCounter := metrics.ForRequest(mockProviderName, metrics.UnknownModelLabel).Rejected
	before := counterValue(t, unknownCounter)

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected rejection error")
	}

	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown rejected counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, mockProviderName, rawModel, "rejected") {
		t.Fatalf("raw rejected model label %q should not be created", rawModel)
	}
}

// TestGateway_Route_SaturatedTargetIsNotAProviderError covers the contradiction
// this classifier exists to remove.
//
// A request shed by targets[].concurrency is a 429 the provider never saw, and
// shouldRecordCircuitBreakerFailure already exonerates it — yet the unary paths
// counted it as gateway_provider_errors_total{error_type=provider_error}, the
// series a per-provider outage alert fires on. A saturation event on a healthy
// upstream therefore paged for that upstream.
func TestGateway_Route_SaturatedTargetIsNotAProviderError(t *testing.T) {
	ep := newBlockingProvider(mockProviderName)
	gw := newConcurrencyBoundGateway(t, ep, 1, 1)

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	// Take the only in-flight slot, then the single queue seat, so the next
	// caller is shed rather than queued.
	go func() { _, _ = gw.Route(context.Background(), req) }()
	waitFor(t, func() bool { return ep.inFlight.Load() == 1 })
	queued := make(chan error, 1)
	go func() {
		_, err := gw.Route(context.Background(), req)
		queued <- err
	}()
	waitFor(t, func() bool { return gw.limiters[mockProviderName].waiting.Load() == 1 })

	providerErrs := metrics.ForProviderError(mockProviderName, metrics.ErrTypeProvider)
	backpressure := metrics.ForProviderError(mockProviderName, metrics.ErrTypeBackpressure)
	providerErrsBefore := counterValue(t, providerErrs)
	backpressureBefore := counterValue(t, backpressure)

	_, shedErr := gw.Route(context.Background(), req)
	close(ep.release)
	if err := <-queued; err != nil {
		t.Errorf("queued Route = %v, want nil once a slot freed", err)
	}
	if !errors.Is(shedErr, providers.ErrProviderSaturated) {
		t.Fatalf("Route on a saturated target = %v, want ErrProviderSaturated", shedErr)
	}

	if delta := counterValue(t, providerErrs) - providerErrsBefore; delta != 0 {
		t.Errorf("provider_errors{error_type=provider_error} delta = %v, want 0 — the provider was never called", delta)
	}
	if delta := counterValue(t, backpressure) - backpressureBefore; delta != 1 {
		t.Errorf("provider_errors{error_type=backpressure} delta = %v, want 1", delta)
	}
}

// TestGateway_Route_ClientCancelIsNotAProviderError is the same rule for the
// other failure the breaker exonerates. The provider is free to report a
// cancelled call as any error shape it likes — an SDK that returns its own
// "request aborted" drops the sentinel entirely — so the context, not the
// error, is what says the caller went away.
func TestGateway_Route_ClientCancelIsNotAProviderError(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		completeFn: func(ctx context.Context, _ providers.Request) (*providers.Response, error) {
			<-ctx.Done()
			// The sentinel deliberately withheld: this is the SDK shape that
			// made a client disconnect look like an upstream failure.
			return nil, errors.New("request aborted")
		},
	})

	providerErrs := metrics.ForProviderError(mockProviderName, metrics.ErrTypeProvider)
	canceled := metrics.ForProviderError(mockProviderName, metrics.ErrTypeClientCanceled)
	providerErrsBefore := counterValue(t, providerErrs)
	canceledBefore := counterValue(t, canceled)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := gw.Route(ctx, providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err == nil {
		t.Fatal("Route on a cancelled request = nil, want an error")
	}

	if delta := counterValue(t, providerErrs) - providerErrsBefore; delta != 0 {
		t.Errorf("provider_errors{error_type=provider_error} delta = %v, want 0 — the caller walked away", delta)
	}
	if delta := counterValue(t, canceled) - canceledBefore; delta != 1 {
		t.Errorf("provider_errors{error_type=client_canceled} delta = %v, want 1", delta)
	}
}

// TestGateway_RecordCacheHit_DoesNotCreditThePrimingProvider covers the
// throughput half of a cache hit.
//
// The response came from a provider that was NOT called this time. Crediting it
// adds tokens it never generated, successes it never served, and a
// sub-millisecond latency sample that pulls its quantiles down — a hundred hits
// of a thousand-token prompt is a hundred thousand phantom input tokens on an
// upstream that may be down. Provenance still names it everywhere that
// describes the response; only the per-provider throughput series read "cache".
func TestGateway_RecordCacheHit_DoesNotCreditThePrimingProvider(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	const model = "gpt-4o"
	// Registered so the model is routable and therefore survives metricModel's
	// bucketing; an unroutable one collapses to "unknown" and the assertions
	// below would compare two labels nothing writes to.
	gw.RegisterProvider(&mockProvider{name: mockProviderName, models: []string{model}})

	primed := metrics.ForRequest(mockProviderName, model)
	cached := metrics.ForRequest(metrics.CacheProviderLabel, model)
	primedSuccess := counterValue(t, primed.Success)
	primedTokensIn := counterValue(t, primed.TokensIn)
	cachedSuccess := counterValue(t, cached.Success)
	cachedTokensIn := counterValue(t, cached.TokensIn)

	ctx, span := observability.NoOp().StartRequestSpan(context.Background(), observability.RequestAttrs{})
	gw.recordCacheHit(ctx, span, observability.NoOp(), &providers.Response{
		ID:       "cached-1",
		Model:    model,
		Provider: mockProviderName,
		Usage:    providers.Usage{PromptTokens: 1000, CompletionTokens: 50},
	}, time.Millisecond, false, false, false)

	if delta := counterValue(t, primed.Success) - primedSuccess; delta != 0 {
		t.Errorf("requests_total{provider=%s,status=success} delta = %v, want 0", mockProviderName, delta)
	}
	if delta := counterValue(t, primed.TokensIn) - primedTokensIn; delta != 0 {
		t.Errorf("tokens_input_total{provider=%s} delta = %v, want 0", mockProviderName, delta)
	}
	if delta := counterValue(t, cached.Success) - cachedSuccess; delta != 1 {
		t.Errorf("requests_total{provider=cache,status=success} delta = %v, want 1", delta)
	}
	if delta := counterValue(t, cached.TokensIn) - cachedTokensIn; delta != 1000 {
		t.Errorf("tokens_input_total{provider=cache} delta = %v, want 1000", delta)
	}
}
