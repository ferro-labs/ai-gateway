package aigateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/plugin/budget"
	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
	_ "github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
	"github.com/ferro-labs/ai-gateway/providers"
)

// chainRequest is the same request every time, so the response cache hits from
// the second call onward — the condition under which the guardrails behind it
// used to stop existing.
func chainRequest() providers.Request {
	return providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
}

func cacheEntries() []config.PluginConfig {
	cfg := map[string]any{"max_age": 300, "max_entries": 1000}
	return []config.PluginConfig{
		{Name: "response-cache", Type: "transform", Stage: "before_request", Enabled: true, Config: cfg},
		{Name: "response-cache", Type: "transform", Stage: "after_request", Enabled: true, Config: cfg},
	}
}

// chainGateway wires a gateway whose plugin list is exactly the shipped example
// order — response-cache first, the governance plugins behind it — over a
// provider that counts how many calls actually left the gateway.
func chainGateway(t *testing.T, plugins []config.PluginConfig) (*Gateway, *atomic.Int64) {
	t.Helper()
	return chainGatewayWithLog(t, plugins, nil)
}

// chainGatewayWithLog is chainGateway with a request-log store installed, so a
// test can read the rows the real request-logger plugin writes rather than
// assert against a stand-in for it.
func chainGatewayWithLog(t *testing.T, plugins []config.PluginConfig, logWriter requestlog.Writer) (*Gateway, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Plugins:  plugins,
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			calls.Add(1)
			return &providers.Response{
				ID:      "resp",
				Model:   "gpt-4o",
				Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "hi"}}},
				Usage:   providers.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			}, nil
		},
	})
	if logWriter != nil {
		gw.SetRequestLogWriter(logWriter)
	}
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("load plugins: %v", err)
	}
	return gw, &calls
}

// TestPluginChain_CacheHitDoesNotDisarmTheRateLimiter is the CRITICAL the shipped
// example config carried: response-cache is listed before rate-limit, so from the
// second identical request onward the cache short-circuited the stage and the
// limiter never ran. A hundred requests over a one-per-second limit were all
// answered 200.
func TestPluginChain_CacheHitDoesNotDisarmTheRateLimiter(t *testing.T) {
	plugins := append(cacheEntries(), config.PluginConfig{
		Name: "rate-limit", Type: "guardrail", Stage: "before_request", Enabled: true,
		Config: map[string]any{"requests_per_second": 1.0, "burst": 1.0},
	})
	gw, calls := chainGateway(t, plugins)

	rejected := 0
	const total = 20
	for range total {
		if _, err := gw.Route(context.Background(), chainRequest()); err != nil {
			var rejection *plugin.RejectionError
			if !errors.As(err, &rejection) {
				t.Fatalf("unexpected error: %v", err)
			}
			rejected++
		}
	}

	if rejected == 0 {
		t.Fatalf("a cache hit disarmed the rate limiter: %d/%d requests served, 0 rejected", total, total)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (the cache should still be serving the rest)", got)
	}
}

// TestPluginChain_BudgetDoesNotBillACacheHit covers the other half. Once the
// guardrails run again on a cache hit, the after-request stage runs too — and a
// budget that billed each of those hits charged the key for provider work that
// never happened, exceeding its own ceiling by two orders of magnitude.
func TestPluginChain_BudgetDoesNotBillACacheHit(t *testing.T) {
	// One provider call at 100 prompt + 50 completion tokens costs $0.00105, so a
	// $0.005 ceiling survives the single real call and nothing else. Billing the
	// cache hits too reaches it on the fifth request.
	budgetCfg := map[string]any{
		"store_id":            "chain-test",
		"spend_limit_usd":     0.005,
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	}
	plugins := append(cacheEntries(),
		config.PluginConfig{Name: "budget", Type: "guardrail", Stage: "before_request", Enabled: true, Config: budgetCfg},
		config.PluginConfig{Name: "budget", Type: "guardrail", Stage: "after_request", Enabled: true, Config: budgetCfg},
	)
	gw, calls := chainGateway(t, plugins)

	budget.ResetStore("chain-test")
	ctx := authctx.WithKeyID(context.Background(), "chain-key")
	const total = 20
	for i := range total {
		if _, err := gw.Route(ctx, chainRequest()); err != nil {
			t.Fatalf("request %d rejected — the budget billed cache hits: %v", i, err)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

// chainLogWriter collects the rows the real request-logger plugin persists, so
// the assertions below are made against the trail an operator would read rather
// than against a stand-in for it.
type chainLogWriter struct {
	mu      sync.Mutex
	entries []requestlog.Entry
}

func (w *chainLogWriter) Write(_ context.Context, entry requestlog.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
	return nil
}

func (w *chainLogWriter) atStage(stage string) []requestlog.Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	rows := make([]requestlog.Entry, 0, len(w.entries))
	for _, e := range w.entries {
		if e.Stage == stage {
			rows = append(rows, e)
		}
	}
	return rows
}

func loggerEntries() []config.PluginConfig {
	cfg := map[string]any{"persist": true}
	return []config.PluginConfig{
		{Name: "request-logger", Type: "logging", Stage: "before_request", Enabled: true, Config: cfg},
		{Name: "request-logger", Type: "logging", Stage: "after_request", Enabled: true, Config: cfg},
		{Name: "request-logger", Type: "logging", Stage: "on_error", Enabled: true, Config: cfg},
	}
}

// A cache-served request is still a served request: its row has to name the
// credential, carry the tokens it received, and record a known zero cost rather
// than "unpriced". One key primes an entry and is served from it a second time,
// and the spend must not move, because no provider was contacted.
//
// The cache is keyed on the credential (SEC-001), so the priming and the served
// request are the same key by necessity. TestPluginChain_CacheIsNotSharedAcross
// Credentials covers the other half — that a different key is not served this
// entry at all.
func TestPluginChain_CacheHitIsAttributedToTheKeyItServed(t *testing.T) {
	budgetCfg := map[string]any{
		"store_id":            "chain-attribution",
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	}
	plugins := append(cacheEntries(),
		config.PluginConfig{Name: "budget", Type: "guardrail", Stage: "before_request", Enabled: true, Config: budgetCfg},
		config.PluginConfig{Name: "budget", Type: "guardrail", Stage: "after_request", Enabled: true, Config: budgetCfg},
	)
	plugins = append(plugins, loggerEntries()...)

	rows := &chainLogWriter{}
	gw, calls := chainGatewayWithLog(t, plugins, rows)
	// Price the target, so "spend did not move" is a claim about a counter that
	// can move rather than one that was always going to read zero.
	gw.catalog = models.Catalog{
		mockProviderName + "/gpt-4o": {
			Provider: mockProviderName,
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrFloat64(3),
				OutputPerMTokens: ptrFloat64(15),
			},
		},
	}
	spend := metrics.ForRequest(mockProviderName, gw.metricModel("gpt-4o")).CostUSD

	budget.ResetStore("chain-attribution")
	if _, err := gw.Route(authctx.WithKeyID(context.Background(), "key-a"), chainRequest()); err != nil {
		t.Fatalf("key A: %v", err)
	}
	primed := counterValue(t, spend)
	if primed <= 0 {
		t.Fatalf("the provider-served request recorded no spend (%v); nothing below would prove anything", primed)
	}

	if _, err := gw.Route(authctx.WithKeyID(context.Background(), "key-a"), chainRequest()); err != nil {
		t.Fatalf("key A, second request: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 — the second request must be served from the cache", got)
	}
	if got := counterValue(t, spend); got != primed {
		t.Errorf("spend moved on a cache hit: %v -> %v; no provider was contacted, so nothing was billed", primed, got)
	}

	served := rows.atStage("after_request")
	if len(served) != 2 {
		t.Fatalf("after_request rows = %d, want 2 (both requests were served)", len(served))
	}
	for i, want := range []struct {
		key      string
		costZero bool
	}{{"key-a", false}, {"key-a", true}} {
		got := served[i]
		if got.APIKeyID != want.key {
			t.Errorf("row %d api_key_id = %q, want %q — the row cannot say who consumed the response", i, got.APIKeyID, want.key)
		}
		if got.Provider != mockProviderName {
			t.Errorf("row %d provider = %q, want %q", i, got.Provider, mockProviderName)
		}
		if got.TotalTokens != 150 {
			t.Errorf("row %d total_tokens = %d, want 150 — the tokens were served either way", i, got.TotalTokens)
		}
		switch {
		case got.CostUSD == nil:
			// Nil means "could not be priced", which is a different claim from
			// "cost nothing" and is what the cache-served row used to record.
			t.Errorf("row %d recorded no cost at all; a served request has a known cost", i)
		case want.costZero && *got.CostUSD != 0:
			t.Errorf("row %d cost_usd = %v, want 0 for a request no provider was contacted for", i, *got.CostUSD)
		case !want.costZero && *got.CostUSD <= 0:
			t.Errorf("row %d cost_usd = %v, want the provider call's real price", i, *got.CostUSD)
		}
	}
}

// The response cache is keyed on the credential, so an entry one key primed is
// not served to another (SEC-001). Asserted through the wired gateway rather
// than against cacheKey, because the defect was that Execute never passed the
// credential in — a key-level test alone would not have caught that.
func TestPluginChain_CacheIsNotSharedAcrossCredentials(t *testing.T) {
	gw, calls := chainGateway(t, cacheEntries())

	for _, key := range []string{"key-a", "key-a", "key-b"} {
		if _, err := gw.Route(authctx.WithKeyID(context.Background(), key), chainRequest()); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}

	// key-a primes then hits; key-b must miss and reach the provider itself.
	if got := calls.Load(); got != 2 {
		t.Errorf("provider calls = %d, want 2 — key-a primes and hits, key-b must not be served key-a's entry", got)
	}
}

// A stream that dies after its first chunk is the failure with the least
// evidence anywhere else. Its row is written from plugin.Context alone, which
// carried no provider at all, so /admin/logs reported that something failed and
// never what.
func TestPluginChain_StreamFailureRecordsTheProvider(t *testing.T) {
	rows := &chainLogWriter{}
	gw, _ := chainGatewayWithLog(t, loggerEntries(), rows)
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{Model: "gpt-4o"}
			ch <- providers.StreamChunk{Error: errors.New("connection reset mid-stream")}
			close(ch)
			return ch, nil
		},
	})

	req := chainRequest()
	req.Stream = true
	ch, err := gw.RouteStream(authctx.WithKeyID(context.Background(), "key-a"), req)
	if err != nil {
		t.Fatalf("stream start: %v", err)
	}
	for range ch { //nolint:revive // the failure is delivered through the channel, so it must drain
	}

	failed := rows.atStage("on_error")
	if len(failed) != 1 {
		t.Fatalf("on_error rows = %d, want 1", len(failed))
	}
	if failed[0].Provider != mockProviderName {
		t.Errorf("provider = %q, want %q — a failure row that cannot name the provider is most of its value missing", failed[0].Provider, mockProviderName)
	}
	if failed[0].APIKeyID != "key-a" {
		t.Errorf("api_key_id = %q, want key-a", failed[0].APIKeyID)
	}
	if failed[0].ErrorMessage == "" {
		t.Error("the failure row carries no error message")
	}
}

// TestPluginChain_GuardrailDenialReachesTheErrorStage is F39: a request blocked
// by policy used to leave no row at all, so the dashboard reported it as neither
// a request nor a failure. The denial still ends the before stage; the on_error
// stage is what must survive it.
func TestPluginChain_GuardrailDenialReachesTheErrorStage(t *testing.T) {
	var recorded atomic.Int64
	plugin.RegisterFactory("chain-test-recorder", func() plugin.Plugin {
		return &recorderPlugin{seen: &recorded}
	})

	gw, calls := chainGateway(t, []config.PluginConfig{
		{
			Name: "word-filter", Type: "guardrail", Stage: "before_request", Enabled: true,
			Config: map[string]any{"blocked_words": []any{"hello"}},
		},
		{Name: "chain-test-recorder", Type: "logging", Stage: "before_request", Enabled: true},
		{Name: "chain-test-recorder", Type: "logging", Stage: "on_error", Enabled: true},
	})

	if _, err := gw.Route(context.Background(), chainRequest()); err == nil {
		t.Fatal("expected the blocked request to be rejected")
	}
	if calls.Load() != 0 {
		t.Error("a blocked request reached the provider")
	}
	if recorded.Load() == 0 {
		t.Fatal("a request blocked by policy left no record on any stage the operator can read")
	}
}

// recorderPlugin stands in for request-logger: it only counts that it ran, which
// is the whole question F39 asks.
type recorderPlugin struct{ seen *atomic.Int64 }

func (r *recorderPlugin) Name() string              { return "chain-test-recorder" }
func (r *recorderPlugin) Type() plugin.PluginType   { return plugin.TypeLogging }
func (r *recorderPlugin) Init(map[string]any) error { return nil }
func (r *recorderPlugin) Close() error              { return nil }
func (r *recorderPlugin) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Stage == plugin.StageOnError {
		r.seen.Add(1)
	}
	return nil
}

func init() {
	plugin.RegisterFactory("test-plugin", func() plugin.Plugin {
		return &testPlugin{name: "test-plugin", typ: plugin.TypeGuardrail}
	})
	plugin.RegisterFactory("test-log-receiver", func() plugin.Plugin {
		p := &logReceiverPlugin{}
		lastLogReceiver = p
		return p
	})
	plugin.RegisterFactory("test-stage-sharing", func() plugin.Plugin {
		p := &stageSharingPlugin{}
		stageSharingMu.Lock()
		stageSharingBuilt = append(stageSharingBuilt, p)
		stageSharingMu.Unlock()
		return p
	})
	plugin.RegisterFactory("test-close-plugin", func() plugin.Plugin {
		closePluginMu.Lock()
		counter := closePluginCounter
		closePluginMu.Unlock()
		return &testPlugin{
			name: "test-close-plugin",
			typ:  plugin.TypeLogging,
			closeFn: func() error {
				if counter != nil {
					counter.Add(1)
				}
				return nil
			},
		}
	})
}

// lastLogReceiver holds the most recently constructed logReceiverPlugin. The
// receiver tests below run sequentially and build exactly one, so this is a
// safe way to reach the instance buildPluginManager created and injected.
var lastLogReceiver *logReceiverPlugin

var (
	closePluginMu      sync.Mutex
	closePluginCounter *atomic.Int32
)

// logReceiverPlugin records the writer the gateway injects, to prove
// buildPluginManager hands the shared request-log store to receiver plugins.
type logReceiverPlugin struct {
	injected    requestlog.Writer
	wasInjected bool
}

func (p *logReceiverPlugin) Name() string                                   { return "test-log-receiver" }
func (p *logReceiverPlugin) Type() plugin.PluginType                        { return plugin.TypeLogging }
func (p *logReceiverPlugin) Init(map[string]any) error                      { return nil }
func (p *logReceiverPlugin) Execute(context.Context, *plugin.Context) error { return nil }
func (p *logReceiverPlugin) Close() error                                   { return nil }
func (p *logReceiverPlugin) SetRequestLogWriter(w requestlog.Writer) {
	p.injected = w
	p.wasInjected = true
}

func loadReceiverPlugin(t *testing.T, gw *Gateway) *logReceiverPlugin {
	t.Helper()
	lastLogReceiver = nil
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}
	if lastLogReceiver == nil {
		t.Fatal("receiver plugin was never constructed")
	}
	return lastLogReceiver
}

// The shared request-log store the gateway holds is injected into a receiver
// plugin as it loads.
func TestGateway_InjectsRequestLogWriterIntoPlugins(t *testing.T) {
	rec := &recordingLogWriter{}
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Plugins: []config.PluginConfig{
			{Name: "test-log-receiver", Type: "logging", Stage: "after_request", Enabled: true},
		},
	})

	gw.SetRequestLogWriter(rec)

	p := loadReceiverPlugin(t, gw)
	if p.injected != requestlog.Writer(rec) {
		t.Fatalf("plugin injected writer = %v, want the store set via SetRequestLogWriter", p.injected)
	}
}

// Without a shared store, buildPluginManager does not call the receiver at all,
// so the plugin decides its own fallback rather than being handed nil.
func TestGateway_NoRequestLogWriter_PluginNotInjected(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Plugins: []config.PluginConfig{
			{Name: "test-log-receiver", Type: "logging", Stage: "after_request", Enabled: true},
		},
	})

	p := loadReceiverPlugin(t, gw)
	if p.wasInjected {
		t.Fatalf("SetRequestLogWriter was called with %v; want no injection when the gateway has no store", p.injected)
	}
}

type recordingLogWriter struct {
	entries []requestlog.Entry
}

func (w *recordingLogWriter) Write(_ context.Context, e requestlog.Entry) error {
	w.entries = append(w.entries, e)
	return nil
}

// stageSharingPlugin records the instances a load builds and the order they
// execute in, so a test can tell one plugin registered at two stages from two
// plugins that each keep their own state.
type stageSharingPlugin struct {
	inits  int
	closes int
}

var (
	stageSharingMu    sync.Mutex
	stageSharingBuilt []*stageSharingPlugin
	stageSharingRan   []*stageSharingPlugin
)

func (p *stageSharingPlugin) Name() string            { return "test-stage-sharing" }
func (p *stageSharingPlugin) Type() plugin.PluginType { return plugin.TypeTransform }
func (p *stageSharingPlugin) Init(map[string]any) error {
	p.inits++
	return nil
}

func (p *stageSharingPlugin) Execute(context.Context, *plugin.Context) error {
	stageSharingMu.Lock()
	stageSharingRan = append(stageSharingRan, p)
	stageSharingMu.Unlock()
	return nil
}

func (p *stageSharingPlugin) Close() error {
	p.closes++
	return nil
}

func resetStageSharing(t *testing.T) {
	t.Helper()
	stageSharingMu.Lock()
	stageSharingBuilt = nil
	stageSharingRan = nil
	stageSharingMu.Unlock()
}

// A plugin that works across the request lifecycle is listed once per stage,
// and both entries mean one instance: two would each hold their own state, so
// a cache lookup would never see what the store wrote.
func TestGateway_LoadPlugins_SharesInstanceAcrossStages(t *testing.T) {
	entry := func(stage string, cfg map[string]any) config.PluginConfig {
		return config.PluginConfig{
			Name:    "test-stage-sharing",
			Type:    "cache",
			Stage:   stage,
			Enabled: true,
			Config:  cfg,
		}
	}

	tests := []struct {
		name          string
		plugins       []config.PluginConfig
		wantInstances int
		wantShared    bool
	}{
		{
			name: "same config at both stages is one instance",
			plugins: []config.PluginConfig{
				entry("before_request", map[string]any{"ttl_seconds": 60, "max_entries": 1000}),
				entry("after_request", map[string]any{"max_entries": 1000, "ttl_seconds": 60}),
			},
			wantInstances: 1,
			wantShared:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetStageSharing(t)
			gw, _ := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: mockProviderName}},
				Plugins:  tt.plugins,
			})

			if err := gw.LoadPlugins(); err != nil {
				t.Fatalf("LoadPlugins failed: %v", err)
			}

			built := append([]*stageSharingPlugin(nil), stageSharingBuilt...)
			if len(built) != tt.wantInstances {
				t.Fatalf("instances built = %d, want %d", len(built), tt.wantInstances)
			}

			pctx := &plugin.Context{Request: &providers.Request{Model: "gpt-4o"}}
			if err := gw.plugins.RunBefore(context.Background(), pctx); err != nil {
				t.Fatalf("RunBefore failed: %v", err)
			}
			pctx.Response = &providers.Response{ID: "ok"}
			if err := gw.plugins.RunAfter(context.Background(), pctx); err != nil {
				t.Fatalf("RunAfter failed: %v", err)
			}

			ran := append([]*stageSharingPlugin(nil), stageSharingRan...)
			if len(ran) != 2 {
				t.Fatalf("plugin executions = %d, want one per stage", len(ran))
			}
			if shared := ran[0] == ran[1]; shared != tt.wantShared {
				t.Fatalf("before and after ran the same instance = %v, want %v", shared, tt.wantShared)
			}

			for i, p := range built {
				if p.inits != 1 {
					t.Errorf("instance %d Init calls = %d, want 1", i, p.inits)
				}
			}

			if err := closePluginManager(gw.plugins); err != nil {
				t.Fatalf("close plugins failed: %v", err)
			}
			for i, p := range built {
				if p.closes != 1 {
					t.Errorf("instance %d Close calls = %d, want 1", i, p.closes)
				}
			}
		})
	}
}

// One plugin listed at several stages under entries that disagree is refused at
// load. It used to become two instances that never shared state — a cache whose
// store and lookup were different caches, a budget whose recorder and checker
// counted into different stores — with a "plugin registered" line for each and no
// symptom until the limit that was supposed to fire did not.
func TestGateway_LoadPlugins_RejectsDisagreeingStageEntries(t *testing.T) {
	entry := func(stage string, cfg map[string]any) config.PluginConfig {
		return config.PluginConfig{
			Name: "test-stage-sharing", Type: "cache", Stage: stage, Enabled: true, Config: cfg,
		}
	}

	tests := []struct {
		name    string
		plugins []config.PluginConfig
		wantErr bool
	}{
		{
			name: "one character of drift between the two entries",
			plugins: []config.PluginConfig{
				entry("before_request", map[string]any{"ttl_seconds": 60}),
				entry("after_request", map[string]any{"ttl_seconds": 120}),
			},
			wantErr: true,
		},
		{
			name: "a store_id that does not match",
			plugins: []config.PluginConfig{
				entry("before_request", map[string]any{"store_id": "a"}),
				entry("after_request", map[string]any{"store_id": "b"}),
			},
			wantErr: true,
		},
		{
			name: "two entries at the SAME stage may differ — they are two plugins",
			plugins: []config.PluginConfig{
				entry("before_request", map[string]any{"ttl_seconds": 60}),
				entry("before_request", map[string]any{"ttl_seconds": 120}),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetStageSharing(t)
			// The rule is config.ValidateMultiStagePlugins, so New rejects it
			// before a Gateway exists — which is also why `ferrogw validate`
			// now catches it. LoadPlugins re-checks for callers that bypass
			// New, and is asserted below for the config New accepts.
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: mockProviderName}},
				Plugins:  tt.plugins,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("disagreeing entries for one plugin were accepted; they load as two instances that never share state")
				}
				return
			}
			if err != nil {
				t.Fatalf("newTestGateway: %v", err)
			}
			if err := gw.LoadPlugins(); err != nil {
				t.Fatalf("LoadPlugins failed: %v", err)
			}
			_ = closePluginManager(gw.plugins)
		})
	}
}

func TestGateway_LoadPlugins(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Plugins: []config.PluginConfig{
			{
				Name:    "test-plugin",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
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
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Plugins: []config.PluginConfig{
			{
				Name:    "does-not-exist",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]any{},
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

func TestGateway_ReloadConfig_ClosesOldPlugins(t *testing.T) {
	var oldClosed atomic.Int32
	closePluginMu.Lock()
	closePluginCounter = &oldClosed
	closePluginMu.Unlock()
	t.Cleanup(func() {
		closePluginMu.Lock()
		closePluginCounter = nil
		closePluginMu.Unlock()
	})

	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Plugins: []config.PluginConfig{
			{
				Name:    "test-close-plugin",
				Type:    "logging",
				Stage:   "after_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})

	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}

	if err := gw.ReloadConfig(context.Background(), config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	}); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}
	if got := oldClosed.Load(); got != 1 {
		t.Fatalf("old plugin closes = %d, want 1", got)
	}
	if gw.plugins.HasPlugins() {
		t.Fatal("expected reload without plugin configs to clear registered plugins")
	}
}

func TestGateway_ReloadConfig_DefersOldPluginCloseUntilInFlightRouteFinishes(t *testing.T) {
	provider := newGateMockProvider(t, &providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	t.Cleanup(provider.releaseAll)
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: provider.Name()}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(provider)

	oldClosed := make(chan struct{})
	var closeOnce sync.Once
	afterRan := make(chan struct{})
	var afterOnce sync.Once
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "in-flight-after",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			afterOnce.Do(func() { close(afterRan) })
			return nil
		},
		closeFn: func() error {
			closeOnce.Do(func() { close(oldClosed) })
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	routeDone := make(chan error, 1)
	go func() {
		_, routeErr := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- routeErr
	}()

	provider.waitActive(t, 1)
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- gw.ReloadConfig(context.Background(), config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: provider.Name()}},
		})
	}()

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadConfig: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload blocked behind in-flight route")
	}
	select {
	case <-oldClosed:
		t.Fatal("old plugin manager closed while an in-flight route could still run after/error plugins")
	default:
	}

	provider.releaseAll()

	select {
	case err := <-routeDone:
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for route")
	}
	select {
	case <-afterRan:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for after plugin")
	}
	select {
	case <-oldClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old plugin close")
	}
}
