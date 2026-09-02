package aigateway

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/plugin/cache"
	"github.com/ferro-labs/ai-gateway/plugin/wordfilter"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestGateway_Embed_LoadBalanceDrainsZeroWeight is the drain contract end to
// end: a target weighted 0 must receive no embedding traffic while a weighted
// sibling serves it all.
func TestGateway_Embed_LoadBalanceDrainsZeroWeight(t *testing.T) {
	drained := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "drained", models: []string{"text-embedding-3-small"}},
	}
	active := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "active", models: []string{"text-embedding-3-small"}},
	}
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLoadBalance},
		Targets: []config.Target{
			{VirtualKey: "drained", Weight: 0},
			{VirtualKey: "active", Weight: 100},
		},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(drained)
	gw.RegisterProvider(active)

	const n = 200
	for range n {
		if _, embedErr := gw.Embed(context.Background(), providers.EmbeddingRequest{
			Model: "text-embedding-3-small",
			Input: "hello",
		}); embedErr != nil {
			t.Fatalf("Embed: %v", embedErr)
		}
	}

	if drained.calls != 0 {
		t.Errorf("drained target took %d of %d requests; weight 0 must mean zero", drained.calls, n)
	}
	if active.calls != n {
		t.Errorf("active target took %d of %d requests", active.calls, n)
	}
}

// TestSurfaceTargetOrder_UnpricedSkipIsNoCapableProvider covers the classification
// half of the cost-optimized skip path. "Skip everything unpriced and nothing
// priced is left" is the caller's 404, the same answer chat gives; unwrapped it
// fell to the classifier's catch-all and answered 500, which every OpenAI SDK
// retries.
func TestSurfaceTargetOrder_UnpricedSkipIsNoCapableProvider(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "embed-provider", models: []string{"unpriced-embed-model"}},
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode:             config.ModeCostOptimized,
			UnpricedStrategy: config.UnpricedStrategySkip,
		},
		Targets: []config.Target{{VirtualKey: "embed-provider"}},
	})
	gw.RegisterProvider(ep)

	_, err := gw.surfaceTargetOrder("unpriced-embed-model", surfaceEmbeddings)
	if err == nil {
		t.Fatal("expected skip mode with no priced candidate to fail")
	}
	if !errors.Is(err, core.ErrNoCapableProvider) {
		t.Fatalf("error = %v, want one matching core.ErrNoCapableProvider (a 404, not a 500)", err)
	}

	// The whole surface must answer the same way, not just the ranking helper.
	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "unpriced-embed-model",
		Input: "hello",
	}); !errors.Is(err, core.ErrNoCapableProvider) {
		t.Fatalf("Embed error = %v, want one matching core.ErrNoCapableProvider", err)
	}
	if ep.calls != 0 {
		t.Errorf("provider was called %d times; skip mode must contact nobody", ep.calls)
	}
}

func TestGateway_Embed_CostOptimizedUsesMappedUpstreamPrice(t *testing.T) {
	first := &mockEmbeddingProvider{mockProvider: mockProvider{name: "first", models: []string{"expensive-upstream"}}}
	second := &mockEmbeddingProvider{mockProvider: mockProvider{name: "second", models: []string{"cheap-upstream"}}}
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized, UnpricedStrategy: config.UnpricedStrategySkip},
		Targets: []config.Target{
			{VirtualKey: "first", ModelMap: map[string]string{visibleMappedModel: "expensive-upstream"}},
			{VirtualKey: "second", ModelMap: map[string]string{visibleMappedModel: "cheap-upstream"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.catalog = models.Catalog{
		"first/expensive-upstream": {Provider: "first", ModelID: "expensive-upstream", Mode: models.ModeEmbedding, Pricing: models.Pricing{EmbeddingPerMTokens: ptrFloat64(10)}},
		"second/cheap-upstream":    {Provider: "second", ModelID: "cheap-upstream", Mode: models.ModeEmbedding, Pricing: models.Pricing{EmbeddingPerMTokens: ptrFloat64(1)}},
	}
	gw.RegisterProvider(first)
	gw.RegisterProvider(second)

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: visibleMappedModel, Input: "hello"}); err != nil {
		t.Fatal(err)
	}
	if first.calls != 0 || second.calls != 1 {
		t.Fatalf("provider calls: first=%d second=%d, want the cheaper mapped target only", first.calls, second.calls)
	}
}

// TestGateway_Embed_RejectionCountsAsRejected covers the counter half of a plugin
// denial. The wire says 429; the counter used to say error, so a wave of
// throttled callers read as a provider outage on these two surfaces alone.
func TestGateway_Embed_RejectionCountsAsRejected(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "embed-provider", models: []string{"text-embedding-3-small"}},
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "embed-provider"}},
	})
	gw.RegisterProvider(ep)
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "limiter",
		typ:  plugin.TypeRateLimit,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "rate limit exceeded"
			return nil
		},
	})

	handles := metrics.ForRequest("", "text-embedding-3-small")
	rejectedBefore := counterValue(t, handles.Rejected)
	errorsBefore := counterValue(t, handles.Error)
	durationsBefore := durationSampleCount(t, map[string]string{"provider": metrics.NoProviderLabel, "model": "text-embedding-3-small"})

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	}); err == nil {
		t.Fatal("expected the rate-limit plugin to reject the request")
	}

	if got := counterValue(t, handles.Rejected); got != rejectedBefore+1 {
		t.Errorf("Rejected counter = %v, want %v", got, rejectedBefore+1)
	}
	if got := counterValue(t, handles.Error); got != errorsBefore {
		t.Errorf("Error counter = %v, want it unchanged at %v: a denial is not a failure", got, errorsBefore)
	}
	// A rejected request took time too, and the histogram is meant to answer
	// "how fast is the gateway", not "how fast are the requests that worked".
	if got := durationSampleCount(t, map[string]string{"provider": metrics.NoProviderLabel, "model": "text-embedding-3-small"}); got != durationsBefore+1 {
		t.Errorf("duration observations = %d, want %d", got, durationsBefore+1)
	}
}

// TestGateway_Embed_FailureObservesDurationAndProviderError covers the two
// signals a failed embedding used to emit nowhere: how long it took to fail, and
// which provider failed. Without the second, gateway_provider_errors_total had no
// samples from these surfaces at all.
func TestGateway_Embed_FailureObservesDurationAndProviderError(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "embed-provider", models: []string{"text-embedding-3-small"}},
		err:          errors.New("upstream exploded"),
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "embed-provider"}},
	})
	gw.RegisterProvider(ep)

	labels := map[string]string{"provider": "embed-provider", "model": "text-embedding-3-small"}
	durationsBefore := durationSampleCount(t, labels)
	providerErrorsBefore := counterValue(t, metrics.ForProviderError("embed-provider", metrics.ErrTypeProvider))

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	}); err == nil {
		t.Fatal("expected Embed to fail")
	}

	if got := durationSampleCount(t, labels); got != durationsBefore+1 {
		t.Errorf("duration observations = %d, want %d: a failure took time too", got, durationsBefore+1)
	}
	if got := counterValue(t, metrics.ForProviderError("embed-provider", metrics.ErrTypeProvider)); got != providerErrorsBefore+1 {
		t.Errorf("provider error counter = %v, want %v", got, providerErrorsBefore+1)
	}
}

// TestGateway_Embed_FailureLabelsLastTargetAttempted covers the provider label
// under fallback. Every candidate failing used to report provider="", putting
// every non-chat failure in one unlabelled bucket — the one thing per-provider
// alerting needs.
func TestGateway_Embed_FailureLabelsLastTargetAttempted(t *testing.T) {
	first := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "first", models: []string{"text-embedding-3-small"}},
		err:          errors.New("first down"),
	}
	second := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "second", models: []string{"text-embedding-3-small"}},
		err:          errors.New("second down"),
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "first"}, {VirtualKey: "second"}},
	})
	gw.RegisterProvider(first)
	gw.RegisterProvider(second)

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	}); err == nil {
		t.Fatal("expected Embed to fail once every target failed")
	}

	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = first:%d second:%d, want 1 each", first.calls, second.calls)
	}
	if !requestMetricLabelExists(t, "second", "text-embedding-3-small", "error") {
		t.Error("no error series for the last target attempted; the failure is unlabelled")
	}
}

// TestGateway_Embed_AfterPluginSeesRecordedRequest is the request-logger
// contract. The logger's after-request branch requires a non-nil Response and
// reads model, provider, tokens and duration off it and the Measurements — none
// of which this surface used to supply, so a successful embedding produced no
// request-log row at all and only its failures were ever recorded.
func TestGateway_Embed_AfterPluginSeesRecordedRequest(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "embed-provider", models: []string{"text-embedding-3-small"}},
		promptTokens: 7,
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "embed-provider"}},
	})
	gw.RegisterProvider(ep)

	var seen *providers.Response
	var measured plugin.Measurements
	var seenRequestModel string
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "recorder",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			if pctx.Response != nil {
				clone := *pctx.Response
				seen = &clone
			}
			if pctx.Request != nil {
				seenRequestModel = pctx.Request.Model
			}
			measured = pctx.Measurements
			return nil
		},
	})

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if seen == nil {
		t.Fatal("after_request saw a nil Response, so nothing records this request")
	}
	if seen.Model != "text-embedding-3-small" {
		t.Errorf("Response.Model = %q, want text-embedding-3-small", seen.Model)
	}
	if seen.Provider != "embed-provider" {
		t.Errorf("Response.Provider = %q, want embed-provider", seen.Provider)
	}
	if seen.Usage.PromptTokens != 7 || seen.Usage.TotalTokens != 7 {
		t.Errorf("Response.Usage = %+v, want 7 prompt / 7 total", seen.Usage)
	}
	if len(seen.Choices) != 0 {
		t.Error("the projection must not invent chat choices; an embedding has none")
	}
	if seenRequestModel != "text-embedding-3-small" {
		t.Errorf("Request.Model = %q, want text-embedding-3-small", seenRequestModel)
	}
	if measured.DurationMs <= 0 {
		t.Error("Measurements.DurationMs was not set, so the row records a NULL duration")
	}
}

// TestGateway_Embed_SkipProviderCannotSuppressTheCall covers the one thing a
// before-plugin must not be able to do here. SkipProvider offers a
// providers.Response in place of the upstream call, and there is no embedding
// inside one — so the call still happens. Leaving the flag set would read
// downstream as "served from cache", and budget bills nothing for such a
// request, so a call that did reach a provider would go uncharged.
func TestGateway_Embed_SkipProviderCannotSuppressTheCall(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: "embed-provider", models: []string{"text-embedding-3-small"}},
		promptTokens: 5,
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "embed-provider"}},
	})
	gw.RegisterProvider(ep)

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "cache",
		typ:  plugin.TypeTransform,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.SkipProvider = true
			pctx.Response = &providers.Response{Model: "someone-elses-model", Provider: "elsewhere"}
			return nil
		},
	})

	var skipAfter bool
	var afterProvider string
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "observer",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			skipAfter = pctx.SkipProvider
			if pctx.Response != nil {
				afterProvider = pctx.Response.Provider
			}
			return nil
		},
	})

	resp, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if ep.calls != 1 {
		t.Fatalf("provider calls = %d, want 1: SkipProvider has nothing to serve on this surface", ep.calls)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("usage = %+v, want the provider's own 5 prompt tokens", resp.Usage)
	}
	if skipAfter {
		t.Error("SkipProvider survived into after_request; budget would read this as an uncharged cache hit")
	}
	if afterProvider != "embed-provider" {
		t.Errorf("after_request Response.Provider = %q, want embed-provider", afterProvider)
	}
}

// TestGateway_GenerateImage_AfterPluginSeesRecordedRequest is the images half of
// the request-logger contract. Image responses report no tokens, so the row is
// identified by its model and provider alone — which is exactly what was missing.
func TestGateway_GenerateImage_AfterPluginSeesRecordedRequest(t *testing.T) {
	ip := &mockImageProvider{
		mockProvider: mockProvider{name: "image-provider", models: []string{"dall-e-3"}},
		images:       2,
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "image-provider"}},
	})
	gw.RegisterProvider(ip)

	var seen *providers.Response
	var measured plugin.Measurements
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "recorder",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			if pctx.Response != nil {
				clone := *pctx.Response
				seen = &clone
			}
			measured = pctx.Measurements
			return nil
		},
	})

	if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
		Model:  "dall-e-3",
		Prompt: "a rig",
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if seen == nil {
		t.Fatal("after_request saw a nil Response, so nothing records this request")
	}
	if seen.Model != "dall-e-3" || seen.Provider != "image-provider" {
		t.Errorf("Response = model %q provider %q, want dall-e-3 / image-provider", seen.Model, seen.Provider)
	}
	if seen.Usage != (providers.Usage{}) {
		t.Errorf("Response.Usage = %+v, want zero: image generation reports no tokens", seen.Usage)
	}
	if measured.DurationMs <= 0 {
		t.Error("Measurements.DurationMs was not set, so the row records a NULL duration")
	}
}

// TestGateway_Embed_RetryIsHonouredOutsideFallback pins the uniform retry the
// pipeline owns: how many times ONE target is asked is targets[].retry's answer
// and nothing else's, so it cannot come to depend on the routing mode again.
func TestGateway_Embed_RetryIsHonouredOutsideFallback(t *testing.T) {
	for _, mode := range []config.StrategyMode{
		config.ModeSingle,
		config.ModeFallback,
		config.ModeLoadBalance,
		config.ModeLatency,
		config.ModeCostOptimized,
	} {
		t.Run(string(mode), func(t *testing.T) {
			ep := &mockEmbeddingProvider{
				mockProvider: mockProvider{name: "embed-provider", models: []string{"text-embedding-3-small"}},
				err:          errors.New("down"),
			}
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: mode},
				Targets: []config.Target{{
					VirtualKey: "embed-provider",
					// ValidateConfig requires a positive total weight under
					// loadbalance; every other mode ignores it.
					Weight: 1,
					Retry:  &config.RetryConfig{Attempts: 3, InitialBackoffMs: 1},
				}},
			})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			gw.RegisterProvider(ep)

			if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
				Model: "text-embedding-3-small",
				Input: "hello",
			}); err == nil {
				t.Fatal("expected Embed to fail")
			}
			if ep.calls != 3 {
				t.Errorf("provider calls = %d, want exactly 3 (attempts: 3)", ep.calls)
			}
		})
	}
}

// A content guardrail configured at before_request screens what a request
// carries, whichever surface carries it. Embeddings and image generation used to
// be exempt by construction: the request handed to the plugin stage named the
// model and nothing else, so a word filter inspected an empty message list,
// found nothing, and the blocked content reached the provider under a 200.

const blockedWord = "hunter2"

// contentModel is served by every provider in this file, on every surface.
const contentModel = "content-model"

func newContentGateway(t *testing.T, p providers.Provider, before ...plugin.Plugin) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(p)
	for _, pl := range before {
		if err := gw.RegisterPlugin(plugin.StageBeforeRequest, pl); err != nil {
			t.Fatalf("register %s: %v", pl.Name(), err)
		}
	}
	return gw
}

func newBlockingWordFilter(t *testing.T) *wordfilter.WordFilter {
	t.Helper()
	w := &wordfilter.WordFilter{}
	if err := w.Init(map[string]any{"blocked_words": []string{blockedWord}}); err != nil {
		t.Fatalf("init word-filter: %v", err)
	}
	return w
}

// assertRefused checks the request was denied by a plugin — a rejection, which
// the client sees as a 4xx — rather than by a plugin FAILURE, which is a 500 and
// would tell a caller who sent blocked content that the gateway is broken.
func assertRefused(t *testing.T, err error) {
	t.Helper()
	var rejection *plugin.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("err = %v, want a *plugin.RejectionError", err)
	}
	if status, _, _ := apierror.RouteErrorDetails(err); status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestEmbed_ContentGuardrailScreensTheInput(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		refused bool
	}{
		{name: "blocked word in a string input", input: "the password is " + blockedWord, refused: true},
		{name: "blocked word in a []string input", input: []string{"harmless", blockedWord}, refused: true},
		// The shape encoding/json actually produces for a JSON array.
		{name: "blocked word in a []any input", input: []any{"harmless", "say " + blockedWord}, refused: true},
		{name: "clean string input", input: "say hello"},
		{name: "clean []any input", input: []any{"a", "b"}},
		// Token IDs are a LOSSLESS encoding of the very text under policy: anyone
		// can encode a blocked word client-side and send the ids. A blocklist a
		// one-line transform evades is not a control, so uninspectable content is
		// refused when — and only when — a content guardrail is configured.
		{name: "token-id input cannot be inspected", input: []any{float64(10121), float64(1178)}, refused: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := &mockEmbeddingProvider{
				mockProvider: mockProvider{name: mockProviderName, models: []string{contentModel}},
			}
			gw := newContentGateway(t, ep, newBlockingWordFilter(t))

			_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
				Model: contentModel,
				Input: tc.input,
			})
			if !tc.refused {
				if err != nil {
					t.Fatalf("Embed = %v, want it served", err)
				}
				return
			}
			assertRefused(t, err)
			if ep.calls != 0 {
				t.Errorf("provider called %d times; blocked content must never reach it", ep.calls)
			}
		})
	}
}

func TestGenerateImage_ContentGuardrailScreensThePrompt(t *testing.T) {
	ip := &mockImageProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{contentModel}},
	}
	gw := newContentGateway(t, ip, newBlockingWordFilter(t))

	_, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
		Model:  contentModel,
		Prompt: "draw " + blockedWord,
	})
	assertRefused(t, err)
	if ip.calls != 0 {
		t.Errorf("provider called %d times; a blocked prompt must never reach it", ip.calls)
	}
}

// Refusing content that cannot be inspected is scoped to deployments that asked
// for inspection. A gateway with no content guardrail keeps serving a token-id
// embeddings input exactly as it did before — the plugin stage still runs, so
// this is not the trivial no-plugins path.
func TestEmbed_TokenIDInputIsServedWithoutAContentGuardrail(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{contentModel}},
	}
	observed := 0
	spy := &testPlugin{
		name: "observer",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			observed++
			return nil
		},
	}
	gw := newContentGateway(t, ep, spy)

	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: contentModel,
		Input: []any{float64(10121), float64(1178)},
	}); err != nil {
		t.Fatalf("Embed = %v, want it served", err)
	}
	if observed == 0 {
		t.Fatal("the before_request stage did not run; this case proves nothing")
	}
	if ep.calls != 1 {
		t.Errorf("provider called %d times, want 1", ep.calls)
	}
}

// The projection is content, not a rewrite: what the provider receives is the
// caller's own input, untouched.
func TestEmbed_ProjectionDoesNotAlterTheProviderRequest(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{contentModel}},
	}
	var got any
	ep.embedFn = func(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
		got = req.Input
		return &providers.EmbeddingResponse{Model: req.Model}, nil
	}
	gw := newContentGateway(t, ep, newBlockingWordFilter(t))

	input := []string{"alpha", "beta"}
	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: contentModel, Input: input}); err != nil {
		t.Fatalf("Embed = %v", err)
	}
	forwarded, ok := got.([]string)
	if !ok || len(forwarded) != 2 || forwarded[0] != "alpha" || forwarded[1] != "beta" {
		t.Errorf("provider received input %#v, want the caller's %#v", got, input)
	}
}

// Projecting the content into the chat request shape makes an embeddings request
// hash exactly as the chat request asking the same question, so the response
// cache must not treat the two as one request. The surface projection carries no
// Choices — serving it to a chat completion returns an empty answer, and never
// calls the provider that would have produced a real one.
func TestResponseCache_SurfaceRequestDoesNotServeAChatCompletion(t *testing.T) {
	completes := 0
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{contentModel},
			completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
				completes++
				return &providers.Response{
					ID:      "chat",
					Model:   contentModel,
					Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "a real answer"}}},
				}, nil
			},
		},
	}

	gw := newContentGateway(t, ep)
	responseCache := &cache.ResponseCache{}
	if err := responseCache.Init(map[string]any{}); err != nil {
		t.Fatalf("init response-cache: %v", err)
	}
	for _, stage := range []plugin.Stage{plugin.StageBeforeRequest, plugin.StageAfterRequest} {
		if err := gw.RegisterPlugin(stage, responseCache); err != nil {
			t.Fatalf("register response-cache at %s: %v", stage, err)
		}
	}

	const shared = "hello"
	if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: contentModel,
		Input: shared,
	}); err != nil {
		t.Fatalf("Embed = %v", err)
	}

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    contentModel,
		Messages: []providers.Message{{Role: roleUser, Content: shared}},
	})
	if err != nil {
		t.Fatalf("Route = %v", err)
	}
	if completes != 1 {
		t.Errorf("provider completed %d chat requests, want 1 — the embeddings request answered this one from cache", completes)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("chat response carries no choices: it was served the embeddings request's cache entry")
	}
}

// A before_request transform rewriting the model must change which model is
// routed, on every surface. Admission already stands down for a transform, so
// before this the request reached the plugin stage, the plugin renamed the model
// in the request the plugins share, and the surface then routed the ORIGINAL
// name anyway — a 404 for a rewrite that had already happened.
func TestSurfaceGovernance_TransformRewritesTheRoutedModel(t *testing.T) {
	const alias = "team-alias"

	t.Run("embeddings", func(t *testing.T) {
		ep := &mockEmbeddingProvider{
			mockProvider: mockProvider{name: mockProviderName, models: []string{contentModel}},
		}
		gw := newContentGateway(t, ep, modelRewritePlugin(plugin.TypeTransform, alias, contentModel))
		if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{Model: alias, Input: "hi"}); err != nil {
			t.Fatalf("Embed = %v, want the rewritten model to be routed", err)
		}
		if ep.capturedModel != contentModel {
			t.Errorf("provider received model %q, want %q", ep.capturedModel, contentModel)
		}
	})

	t.Run("images", func(t *testing.T) {
		ip := &mockImageProvider{
			mockProvider: mockProvider{name: mockProviderName, models: []string{contentModel}},
		}
		gw := newContentGateway(t, ip, modelRewritePlugin(plugin.TypeTransform, alias, contentModel))
		if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{Model: alias, Prompt: "hi"}); err != nil {
			t.Fatalf("GenerateImage = %v, want the rewritten model to be routed", err)
		}
		if ip.capturedModel != contentModel {
			t.Errorf("provider received model %q, want %q", ip.capturedModel, contentModel)
		}
	})
}

// imageCostCatalog is a synthetic catalog carrying one row of each image
// billing shape the real catalog holds, so these tests assert on fixed figures
// rather than on prices a catalog refresh can move. The shapes themselves are
// real: of 96 image rows, 74 carry a per-tile price, 18 carry only a per-token
// one (the gpt-image family and fireworks' image models), 4 carry neither, and
// 10 of the 74 carry both.
func imageCostCatalog() models.Catalog {
	perM := func(v float64) *float64 { return &v }
	return models.Catalog{
		// Token-priced only — openai/gpt-image-1's real shape.
		"tokens/gpt-image-1": {
			Provider: "tokens",
			ModelID:  "gpt-image-1",
			Mode:     models.ModeImage,
			Pricing:  models.Pricing{InputPerMTokens: perM(5.0), OutputPerMTokens: perM(10.0)},
		},
		// Per-tile priced, and also per-token — the gemini/vertex-ai shape.
		"tiles/image-model": {
			Provider: "tiles",
			ModelID:  "image-model",
			Mode:     models.ModeImage,
			Pricing: models.Pricing{
				ImagePerTile:     perM(0.04),
				InputPerMTokens:  perM(5.0),
				OutputPerMTokens: perM(10.0),
			},
		},
		// Priced no way at all — openai/dall-e-3's real shape.
		"unpriced/image-model": {
			Provider: "unpriced",
			ModelID:  "image-model",
			Mode:     models.ModeImage,
		},
	}
}

// imageCostRun generates one image against a provider reporting the given token
// usage and returns what the request was costed at, read from the same
// Measurements the request logger persists and the cost metric reads.
func imageCostRun(t *testing.T, provider, model string, usage providers.Usage, images int) plugin.Measurements {
	t.Helper()

	ip := &mockImageProvider{
		mockProvider: mockProvider{name: provider, models: []string{model}},
		imageFn: func(_ context.Context, _ providers.ImageRequest) (*providers.ImageResponse, error) {
			return &providers.ImageResponse{
				Data:  make([]providers.GeneratedImage, images),
				Usage: usage,
			}, nil
		},
	}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: provider}},
	})
	gw.RegisterProvider(ip)

	gw.mu.Lock()
	gw.catalog = imageCostCatalog()
	gw.mu.Unlock()

	var measured plugin.Measurements
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "recorder",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			measured = pctx.Measurements
			return nil
		},
	})

	if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
		Model:  model,
		Prompt: "a rig",
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	return measured
}

// TestGateway_GenerateImage_TokenPricedModelBillsItsTokens is the whole point of
// the chain. A gpt-image generation carries no per-tile catalog price and is
// billed per token, and every link used to drop the tokens: the response type
// had no usage field, the OpenAI provider discarded the SDK's, the gateway
// passed an empty literal, and the calculator had no token arm. The result was a
// real, billed generation recorded at nothing.
func TestGateway_GenerateImage_TokenPricedModelBillsItsTokens(t *testing.T) {
	measured := imageCostRun(t, "tokens", "gpt-image-1",
		providers.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000}, 1)

	if !measured.HasCost {
		t.Error("HasCost = false: a model the catalog prices per token was recorded as unpriced")
	}
	if measured.CostUSD != 15.0 {
		t.Errorf("CostUSD = %v, want 15.0 (1M input at $5/M + 1M output at $10/M)", measured.CostUSD)
	}
}

// TestGateway_GenerateImage_NoUsageIsUnpricedNotFree covers every image provider
// other than OpenAI and Azure OpenAI. They report no tokens, so a token-priced
// model has nothing to bill — and the honest answer is unpriced. Reporting
// priced-at-$0.00 would be the known-zero the mode-aware Priced flag exists to
// prevent, and is strictly worse than saying nothing.
func TestGateway_GenerateImage_NoUsageIsUnpricedNotFree(t *testing.T) {
	measured := imageCostRun(t, "tokens", "gpt-image-1", providers.Usage{}, 1)

	if measured.HasCost {
		t.Error("HasCost = true with no usage reported: a $0.00 bill recorded as though it were the real figure")
	}
	if measured.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0: nothing was reported to bill", measured.CostUSD)
	}
}

// TestGateway_GenerateImage_TilePricedModelIsUnaffected pins the direction of
// the fallback. Ten catalog image rows carry a per-token price alongside their
// per-tile one; per-image is what those providers bill, so the tokens must not
// displace it.
func TestGateway_GenerateImage_TilePricedModelIsUnaffected(t *testing.T) {
	measured := imageCostRun(t, "tiles", "image-model",
		providers.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}, 2)

	if !measured.HasCost {
		t.Error("HasCost = false for a per-tile priced model")
	}
	if measured.CostUSD != 0.08 {
		t.Errorf("CostUSD = %v, want 0.08 (2 images at $0.04): the token fallback displaced the per-tile price", measured.CostUSD)
	}
}

// TestGateway_GenerateImage_UnpricedModelStaysUnpriced holds the COST-001
// invariant: reported usage must not conjure a price the catalog does not carry.
func TestGateway_GenerateImage_UnpricedModelStaysUnpriced(t *testing.T) {
	measured := imageCostRun(t, "unpriced", "image-model",
		providers.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}, 1)

	if measured.HasCost {
		t.Error("HasCost = true for a model the catalog prices no way at all")
	}
	if measured.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", measured.CostUSD)
	}
}
