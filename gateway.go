// Package aigateway provides a high-performance, self-contained AI gateway
// for routing requests to large language model (LLM) providers.
//
// The Gateway type is the main entry point: create one with New, register
// providers with RegisterProvider, load plugins from config with LoadPlugins,
// and route requests with Route or RouteStream.
//
// Plugins and routing strategies (single, fallback, load-balance, conditional,
// content-based, ab-test) are configured via [Config] which can be loaded
// from a YAML or JSON file using [LoadConfig].
package aigateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/envref"
	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Gateway is the main entry point for routing LLM requests.
type Gateway struct {
	mu sync.RWMutex
	// log is the gateway's own logger, injected via WithLogger and defaulted to
	// logger.Default() in New. Set once at construction and never mutated, so it
	// is read without holding mu.
	log              *logger.Logger
	constructionCtx  context.Context
	config           config.Config
	catalog          models.Catalog
	providers        map[string]providers.Provider
	providerNames    []string
	strategy         strategies.Strategy
	plugins          *plugin.Manager
	requestLogWriter requestlog.Writer
	closeOnce        sync.Once
	// closed is set under mu by Close. ReloadConfig checks it because closeOnce
	// has already fired by then: a reload landing after shutdown would build a
	// fresh MCP registry, spawn its subprocesses, and leave nothing to ever
	// close them.
	closed             bool
	hooks              *hookBus
	catalogRefreshDone sync.WaitGroup
	// pendingMCPCloses tracks registries retired by a config reload, whose
	// teardown runs asynchronously. Close waits on it so a reload landing just
	// before shutdown cannot leave a subprocess termination ladder running past
	// process exit.
	pendingMCPCloses sync.WaitGroup
	// shutdownCtx is a lifecycle context, not a request context. Storing it on the
	// struct is the intended idiom here: it is created once in New, parents the
	// gateway's background workers (hook dispatch, catalog refresh, MCP init), and
	// is cancelled by Close() to signal shutdown. It is never a per-request context.
	shutdownCtx      context.Context
	shutdownCancel   context.CancelFunc
	circuitBreakers  map[string]*circuitbreaker.CircuitBreaker
	limiters         map[string]*providerLimiter
	discoveredModels map[string][]providers.ModelInfo

	// catalogModels caches catalog.ModelsForProvider per registered provider.
	// Derived state: rebuilt under g.mu by rebuildModelIndexesLocked whenever
	// the catalog or the provider set changes. Its slices are read-only.
	catalogModels map[string][]string

	// declaredModels caches targets[].models per provider name. Derived state on
	// the same terms as catalogModels: rebuilt under g.mu by
	// rebuildModelIndexesLocked, read-only slices.
	declaredModels map[string][]string
	latencyTracker *latency.Tracker
	modelIndex     modelLookupIndex

	// routingSnap publishes the provider set and its derived indexes to readers
	// without a lock. Written only by publishRoutingLocked under g.mu; the
	// fields above remain the mutable source of truth that it is built from.
	routingSnap atomic.Pointer[routingSnapshot]

	// obs is the observability provider used to emit per-request spans.
	// Defaults to observability.NoOp() when SetObservability has not
	// been called, which guarantees zero allocations on the hot path
	// (issue #49 acceptance criterion).
	obs observability.Provider

	// obsEventsActive is true when the installed Provider implements
	// observability.EventRecordingProvider and RecordingEnabled() returned
	// true at the time SetObservability was called. It is written under g.mu
	// in SetObservability and read under g.mu (alongside g.obs) at the top of
	// Route and RouteStream, so the same lock that guards g.obs guards it.
	obsEventsActive bool

	// obsAttemptsActive is true when the installed Provider also implements
	// observability.RoutingAttemptRecordingProvider and RoutingAttemptsEnabled()
	// returned true at the time SetObservability was called. Guarded like
	// obsEventsActive and read by routeTargets, which records one event per
	// physical provider call only while it is set.
	obsAttemptsActive bool

	// MCP fields — nil when no MCPServers are configured.
	mcpRegistry *mcp.Registry
	mcpExecutor *mcp.Executor
	mcpInitDone chan struct{} // closed when background MCP init goroutine completes
}

const (
	hookDispatchQueueSize  = 256
	catalogRefreshInterval = 24 * time.Hour
)

// Option configures a Gateway during New.
type Option func(*Gateway)

// WithLogger injects the logger the gateway logs through. Pass nil to keep the
// default (logger.Default()).
func WithLogger(l *logger.Logger) Option {
	return func(g *Gateway) { g.log = l }
}

// WithContext sets the context used by work performed during construction.
func WithContext(ctx context.Context) Option {
	return func(g *Gateway) { g.constructionCtx = ctx }
}

// New creates a new Gateway instance with the given configuration.
// It validates cfg with ValidateConfig before initialising any resources,
// returning an error immediately if the config is invalid. This matches the
// fail-fast behaviour already present in ReloadConfig and the CLI.
func New(cfg config.Config, opts ...Option) (*Gateway, error) {
	if err := config.ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Resolve the logger first so every step below (catalog load included) logs
	// through the injected logger rather than logger.Default().
	gw := &Gateway{}
	for _, opt := range opts {
		opt(gw)
	}
	if gw.log == nil {
		gw.log = logger.Default()
	}
	if gw.constructionCtx == nil {
		gw.constructionCtx = context.Background()
	}

	constructionCtx := gw.constructionCtx
	catalogResult, err := models.LoadWithInfoContext(constructionCtx)
	gw.constructionCtx = nil
	if err := constructionCtx.Err(); err != nil {
		return nil, err
	}
	recordCatalogLoad(catalogResult.Source, err)
	catalog := catalogResult.Catalog
	if err != nil {
		// Non-fatal: operate without model metadata (no enrichment / cost reporting).
		gw.log.Error("model catalog unavailable; continuing without catalog metadata", "url", catalogResult.URLForLog(), "error", err)
		catalog = models.Catalog{}
	}

	gw.config = cfg
	gw.catalog = catalog
	gw.providers = make(map[string]providers.Provider)
	gw.plugins = plugin.NewManager(gw.log)
	gw.circuitBreakers = make(map[string]*circuitbreaker.CircuitBreaker)
	gw.limiters = make(map[string]*providerLimiter)
	gw.discoveredModels = make(map[string][]providers.ModelInfo)
	gw.latencyTracker = latency.New(0) // default window size (100 samples)
	gw.modelIndex = modelLookupIndex{
		exactProviders:       make(map[string][]string),
		exactStreamProviders: make(map[string][]string),
		exactEmbedProviders:  make(map[string][]string),
		exactImageProviders:  make(map[string][]string),
	}
	gw.hooks = newHookBus(hookDispatchQueueSize)
	gw.obs = observability.NoOp()
	gw.shutdownCtx, gw.shutdownCancel = context.WithCancel(context.Background()) //nolint:gosec // canceled by Gateway.Close()
	gw.hooks.start(gw.shutdownCtx)
	gw.startCatalogRefresh()

	// Wire MCP from config. In New the gateway is not yet published, so no lock
	// is held here; the field writes are safe.
	gw.wireMCPLocked(cfg, "mcp: server initialization failed")

	gw.mu.Lock()
	gw.ensureCircuitBreakersLocked()
	gw.ensureProviderLimitersLocked()
	gw.mu.Unlock()

	return gw, nil
}

// SetObservability installs an observability.Provider on the gateway.
// Pass observability.NoOp() to disable. The provider's StartRequestSpan
// is called at the top of Route and RouteStream; span attributes are
// populated incrementally as the request progresses through routing,
// provider execution, plugins, and final cost/usage calculation.
//
// Safe to call only at startup, before serving traffic. The cmd/ferrogw
// wire-up constructs the provider via internal/otel.Init.
func (g *Gateway) SetObservability(p observability.Provider) {
	if p == nil {
		p = observability.NoOp()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.obs = p
	// Cache whether the provider will receive RecordEvent calls so the
	// hot path can skip Event construction when nothing is listening, and
	// whether it also wants the per-attempt events.
	g.obsEventsActive = false
	g.obsAttemptsActive = false
	if er, ok := p.(observability.EventRecordingProvider); ok {
		g.obsEventsActive = er.RecordingEnabled()
	}
	if ar, ok := p.(observability.RoutingAttemptRecordingProvider); ok {
		g.obsAttemptsActive = g.obsEventsActive && ar.RoutingAttemptsEnabled()
	}
}

// SetRequestLogWriter installs the shared request-log store that logging
// plugins write through, instead of each opening its own. Pass the store built
// from REQUEST_LOG_STORE_BACKEND / REQUEST_LOG_STORE_DSN, or nil to leave
// logging plugins without a persistence target.
//
// Safe to call only at startup, before serving traffic and before LoadPlugins,
// since the writer is injected into plugins as they are built. The store is
// owned by the caller (closed on shutdown); the gateway only hands it to
// plugins.
func (g *Gateway) SetRequestLogWriter(w requestlog.Writer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requestLogWriter = w
}

// Logger returns the gateway's logger. Always non-nil; defaults to
// logger.Default(). Used by HTTP handlers wired to the same logger.
func (g *Gateway) Logger() *logger.Logger {
	return g.log
}

// Observability returns the current observability.Provider. Always
// non-nil; defaults to NoOp.
func (g *Gateway) Observability() observability.Provider {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.obs
}

// Catalog returns a shallow copy of the loaded model catalog.
// A copy is returned so callers cannot mutate the gateway's internal catalog.
func (g *Gateway) Catalog() models.Catalog {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cp := make(models.Catalog, len(g.catalog))
	maps.Copy(cp, g.catalog)
	return cp
}

func (g *Gateway) startCatalogRefresh() {
	g.catalogRefreshDone.Add(1)
	go func() {
		defer g.catalogRefreshDone.Done()
		ticker := time.NewTicker(catalogRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-g.shutdownCtx.Done():
				return
			case <-ticker.C:
				g.refreshCatalog(g.shutdownCtx)
			}
		}
	}()
}

// refreshCatalog fetches the latest model catalog. ctx is g.shutdownCtx, so a
// fetch already in flight when the gateway shuts down is canceled immediately
// instead of running to fetchRemote's own client timeout.
func (g *Gateway) refreshCatalog(ctx context.Context) {
	result, err := models.LoadWithInfoContext(ctx)
	recordCatalogLoad(result.Source, err)
	if err != nil {
		g.log.Error("model catalog refresh failed", "url", result.URLForLog(), "error", err)
		return
	}
	if result.Source != models.LoadSourceRemote {
		g.log.Warn("model catalog refresh skipped; keeping current catalog", "url", result.URLForLog(), "source", result.Source)
		return
	}

	g.mu.Lock()
	g.catalog = result.Catalog
	// The exact-match routing index is derived from the catalog (issue #146),
	// so it must be rebuilt whenever the catalog is replaced — otherwise the
	// 24h refresh would leave routing frozen at the startup catalog while
	// /v1/models reflects the new one. Rebuilding it also drops the built
	// strategy, which is what the cost-optimized mode needs too: it prices
	// targets against the catalog that was current when it was built.
	g.rebuildModelIndexesLocked()
	g.mu.Unlock()

	g.log.Info("model catalog refreshed", "url", result.URLForLog(), "models", len(result.Catalog))
}

func recordCatalogLoad(source models.LoadSource, err error) {
	if source == "" {
		source = models.LoadSourceFallback
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	metrics.CatalogLoadsTotal.WithLabelValues(string(source), result).Inc()
}

// Event subject constants used when invoking gateway hooks.
const (
	SubjectRequestCompleted = "gateway.request.completed"
	SubjectRequestFailed    = "gateway.request.failed"

	roleUser = "user"
)

// RegisterProvider registers a provider with the gateway under its canonical name.
func (g *Gateway) RegisterProvider(p providers.Provider) {
	g.RegisterProviderAs(p.Name(), p)
}

// RegisterProviderAs registers a provider under name while retaining the
// provider's canonical identity and every optional capability it implements.
// This is intended for deployments that bind multiple credentials for one
// canonical provider to distinct routing targets.
func (g *Gateway) RegisterProviderAs(name string, p providers.Provider) {
	if name == "" {
		name = p.Name()
	}
	p = providers.WithName(p, name)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.providers[name]; !exists {
		g.providerNames = append(g.providerNames, name)
	}
	g.providers[name] = p
	g.rebuildModelIndexesLocked()
	g.strategy = nil // force strategy rebuild
}

// RegisterPlugin registers a plugin at the given lifecycle stage.
func (g *Gateway) RegisterPlugin(stage plugin.Stage, p plugin.Plugin) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.plugins.Register(stage, p)
}

// ReloadConfig validates and applies a new configuration, forcing strategy rebuild on next request.
//
// The context satisfies the admin ConfigManager seam; the in-memory reload below
// performs no request-scoped store/IO of its own (MCP re-initialization is
// parented on the gateway shutdown context), so ctx is accepted but not used here.
func (g *Gateway) ReloadConfig(ctx context.Context, cfg config.Config) error {
	_ = ctx
	if err := config.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	plugins, err := g.buildPluginManager(cfg.Plugins)
	if err != nil {
		return err
	}
	pluginsInstalled := false
	defer func() {
		if pluginsInstalled {
			return
		}
		if closeErr := closePluginManager(plugins); closeErr != nil {
			g.log.Warn("plugin close failed after config reload error", "error", closeErr)
		}
	}()
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errors.New("gateway is closed")
	}
	oldPlugins := g.plugins
	oldTargets := g.config.Targets
	g.config = cfg
	g.plugins = plugins
	pluginsInstalled = true
	// Rebuilding the model index also drops the built strategy, forcing a rebuild
	// on the next request. It runs on every reload because targets[].models is
	// part of the index: without it a reload that adds or removes a declared
	// model would leave routing answering from the superseded target list while
	// /admin/config already reports the new one.
	g.rebuildModelIndexesLocked()
	g.retireResilienceLocked(oldTargets)
	g.ensureCircuitBreakersLocked()
	g.ensureProviderLimitersLocked()

	// Re-register MCP servers from the new config (clears MCP state when none).
	g.wireMCPLocked(cfg, "mcp: server initialization failed after reload")

	g.mu.Unlock()
	if err := closePluginManager(oldPlugins); err != nil {
		g.log.Warn("plugin close failed during config reload", "error", err)
	}
	return nil
}

// retireResilienceLocked drops the circuit breakers and concurrency limiters the
// new config invalidates, leaving the survivors in place for
// ensureCircuitBreakersLocked / ensureProviderLimitersLocked to skip over.
// Rebuilding them all instead would throw away live state an operator relies on:
// an open circuit would silently close and re-admit traffic to a provider that is
// still failing, and a replacement limiter admits its full concurrency alongside
// the requests still in flight under the old one, briefly doubling the cap.
//
// An entry is retired only when its target is gone or its own block changed, since
// keeping a stale instance across a settings change would ignore the value the
// operator just set — a worse outcome than rebuilding.
//
// prev is the target list the current instances were built from; the caller has
// already installed the new config. Caller must hold g.mu.
func (g *Gateway) retireResilienceLocked(prev []config.Target) {
	was := make(map[string]config.Target, len(prev))
	for _, t := range prev {
		was[t.VirtualKey] = t
	}
	now := make(map[string]config.Target, len(g.config.Targets))
	for _, t := range g.config.Targets {
		now[t.VirtualKey] = t
	}
	// A target the new config dropped resolves to the zero Target, whose nil
	// block never matches the non-nil one that built the instance.
	for key := range g.circuitBreakers {
		if !sameBlock(was[key].CircuitBreaker, now[key].CircuitBreaker) {
			delete(g.circuitBreakers, key)
		}
	}
	for key := range g.limiters {
		if !sameBlock(was[key].Concurrency, now[key].Concurrency) {
			delete(g.limiters, key)
		}
	}
	// Latency samples are retired on a different test to the two above: a
	// breaker or a limiter survives while its own settings block is unchanged,
	// because it holds live state an operator relies on, but a latency window
	// describes a TARGET and means nothing once that target is gone. Keeping it
	// leaked for the life of the process, and a target removed and re-added came
	// back ranked on measurements taken before the change.
	//
	// Nil-checked because a reload does not require a fully built Gateway: New
	// always installs a tracker, but a hand-assembled one need not, and reload is
	// reachable without ever having routed a request.
	if g.latencyTracker != nil {
		g.latencyTracker.Retain(slices.Collect(maps.Keys(now)))
	}
}

// sameBlock reports whether two optional config blocks describe the same setting.
func sameBlock[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// GetConfig returns a copy of the current configuration.
func (g *Gateway) GetConfig() config.Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.config
}

// BatchTarget returns the configured batch/files target's virtual key, or ""
// when the batch surface is not configured. It is read under the same lock as
// the rest of the config so a reload is observed atomically.
func (g *Gateway) BatchTarget() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.config.BatchTarget
}

// ResponsesTarget returns the configured Responses id-subroute target's virtual
// key, or "" when those sub-routes are not configured. POST /v1/responses does
// not use it — that endpoint routes by model.
func (g *Gateway) ResponsesTarget() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.config.ResponsesTarget
}

// LoadPlugins initializes and registers plugins from the gateway configuration.
func (g *Gateway) LoadPlugins() error {
	g.mu.RLock()
	configs := append([]config.PluginConfig(nil), g.config.Plugins...)
	g.mu.RUnlock()
	plugins, err := g.buildPluginManager(configs)
	if err != nil {
		return err
	}

	g.mu.Lock()
	oldPlugins := g.plugins
	g.plugins = plugins
	g.mu.Unlock()
	if err := closePluginManager(oldPlugins); err != nil {
		return err
	}
	return nil
}

func (g *Gateway) buildPluginManager(configs []config.PluginConfig) (*plugin.Manager, error) {
	g.mu.RLock()
	sharedLogWriter := g.requestLogWriter
	g.mu.RUnlock()

	// Re-checked here, not only in ValidateConfig, because LoadPlugins can be
	// called with a plugin list that never went through it. The rule itself is
	// config's, so `ferrogw validate` rejects the same config this would.
	if err := config.ValidateMultiStagePlugins(configs); err != nil {
		return nil, err
	}

	plugins := plugin.NewManager(g.log)
	shared := make(map[string]plugin.Plugin, len(configs))
	for _, pc := range configs {
		if !pc.Enabled {
			continue
		}
		var p plugin.Plugin
		key, shareable := config.PluginSharingKey(pc)
		if shareable {
			p = shared[key]
		}
		reused := p != nil
		if !reused {
			factory, ok := plugin.GetFactory(pc.Name)
			if !ok {
				_ = plugins.Close()
				return nil, fmt.Errorf("unknown plugin: %s", pc.Name)
			}
			p = factory()
			// Hand the shared request-log store to plugins that record through it,
			// before Init so Init can direct persistence at it. Plugins never open
			// their own store from config, so no request-supplied value reaches the
			// filesystem.
			if sharedLogWriter != nil {
				if r, ok := p.(requestlog.WriterReceiver); ok {
					r.SetRequestLogWriter(sharedLogWriter)
				}
			}
			// Resolve ${VAR} references into the plugin's own config at construction.
			// The Config itself keeps the references, so the secret is never persisted
			// to the config store nor served by GET /admin/config.
			pluginCfg, err := envref.AnyMap(pc.Config)
			if err != nil {
				_ = plugins.Close()
				_ = p.Close()
				return nil, fmt.Errorf("plugin %s config: %w", pc.Name, err)
			}
			if err := p.Init(pluginCfg); err != nil {
				_ = plugins.Close()
				_ = p.Close()
				return nil, fmt.Errorf("plugin %s init failed: %w", pc.Name, err)
			}
		}
		stage := plugin.Stage(pc.Stage)
		if err := plugins.Register(stage, p); err != nil {
			_ = plugins.Close()
			// A reused instance is already registered at an earlier stage, so the
			// manager close above released it; only an instance this iteration
			// built is still unowned.
			if !reused {
				_ = p.Close()
			}
			return nil, fmt.Errorf("plugin %s register failed: %w", pc.Name, err)
		}
		if shareable {
			shared[key] = p
		}
	}
	return plugins, nil
}

func closePluginManager(plugins *plugin.Manager) error {
	if plugins == nil {
		return nil
	}
	return plugins.Close()
}

func acquirePluginManager(plugins *plugin.Manager) func() {
	if plugins == nil || !plugins.HasPlugins() {
		return func() {}
	}
	return plugins.Acquire()
}

// acquireMCPRegistry pins a registry snapshot for the life of one request, so a
// concurrent config reload retires the old registry only after this request has
// finished with it. Without it, a reload terminates the stdio subprocess
// underneath an in-flight tool call and the call fails with "transport closed".
func acquireMCPRegistry(reg *mcp.Registry) func() {
	if reg == nil {
		return func() {}
	}
	return reg.Acquire()
}

// ── Registry-consolidation helpers ──────────────────────────────────────────
// These methods make *Gateway satisfy providers.ProviderSource so that HTTP
// handlers that previously held a *providers.Registry can accept the gateway
// directly instead.

// AllModels returns the models this gateway will route: one entry per model ID,
// naming the target that serves it. If auto-discovery has run for a provider,
// discovered models take precedence over the provider's static model list.
//
// The targets list is an allowlist — routeTargets walks it and nothing else —
// so a provider registered because its credential happens to be present, but
// named by no target, serves nothing. Listing its models advertised an
// inventory every routed surface then refused: with a single `groq` target this
// returned 193 models, 179 of them owned by providers no target named, each one
// a 404 the moment a client asked for it.
//
// Two targets can serve one model ID — the catalog knows it for one of them and
// targets[].models declares it on the other, or two targets simply both serve
// it. The OpenAI /v1/models contract is keyed by `id`, so listing the ID twice
// is not "more information": a client that builds a map by ID silently keeps
// whichever entry came last, and a picker shows the name twice. One entry is
// published, and it is the FIRST configured target that serves the ID, so
// `owned_by` and the catalog metadata derived from it describe the target the
// request is actually offered to first.
//
// A model the routing strategy will refuse outright is not listed. That is one
// question with one authority — the strategy itself is asked, rather than a
// second copy of its rule kept here — and the case it exists for is
// `cost-optimized` with `unpriced_strategy: skip`, which serves only what the
// catalog prices: every model declared by targets[].models is unpriced by
// construction, so all of them were advertised and all of them 404'd.
//
// Circuit state is deliberately not consulted, unlike Readiness.Routable. A
// breaker is a transient statement about one target's health; this is the
// gateway's inventory. A model that vanished from /v1/models while a breaker
// was open would read to a client as "this gateway does not serve that model"
// and send it to change its request or its configuration over an outage that
// clears itself. /health and /readyz report circuit state; this reports what is
// configured.
func (g *Gateway) AllModels() []providers.ModelInfo {
	served := g.ServedProviders()

	// Built here rather than resolved per model: it is the same strategy the
	// next request will route through, and building it is what a first request
	// would do anyway. A config validated at load cannot fail to build (see
	// buildStrategy), and if one somehow does, the inventory is reported
	// unfiltered rather than reported empty — the routed surfaces answer the
	// same error either way, and an empty /v1/models would send an operator
	// looking for a missing model instead of a broken strategy.
	strategy, err := g.getStrategy()
	if err != nil {
		strategy = nil
	}

	snap := g.routing()
	listed := make([]providers.ModelInfo, 0, len(snap.providerNames))
	seen := make(map[string]struct{}, len(snap.providerNames))
	for _, name := range served {
		for _, m := range snap.modelsFor(name) {
			if _, dup := seen[m.ID]; dup {
				continue
			}
			seen[m.ID] = struct{}{}
			if strategy != nil && !g.routingServes(strategy, m.ID) {
				continue
			}
			listed = append(listed, m)
		}
	}
	return listed
}

// routingServes reports whether a request for this model would reach a target
// that serves it, which is what "this gateway serves it" means once a strategy
// can refuse a model outright and a routing mode can commit to one target.
//
// It asks the two authorities in the order routing asks them, rather than
// restating either. The strategy names an order — a strategy that refuses is
// the only thing that knows why, and a rule copied beside it drifts the moment
// either side changes. The pipeline then decides which of those names the walk
// may actually attempt (eligibleKeys) and whether one of them is a candidate for
// the model (candidateLocked, resolveTarget's own test).
//
// The second half is what the modes that commit to one target need. Under
// single, conditional and content-based the walk attempts the chosen target and
// nothing else, so a model owned only by some OTHER configured target is a 404
// however many targets the config lists — and all of them were advertised. It
// is the same defect as listing a provider no target names, one level in.
//
// Under content-based the answer is representative rather than exact: those
// rules match on prompt content, which a model listing does not have and cannot
// be given, so this reports what a request carrying no content gets — the
// no-match fallback target. A model only a matched rule would reach is
// therefore not listed. That is the safe direction, and the only one available:
// the endpoint's promise is that what it lists can be routed.
func (g *Gateway) routingServes(s strategies.Strategy, model string) bool {
	keys, err := s.SelectTargets(providers.Request{Model: model})
	if err != nil || len(keys) == 0 {
		return false
	}
	// gate is nil: /v1/models describes one inventory rather than one surface,
	// and chat is the surface every target can serve.
	//
	// ignoreCircuitState is the one way this deliberately differs from the walk.
	// The walk skips a target whose circuit is open; the listing must not, or the
	// inventory would shrink and grow with an outage — see AllModels and
	// healthyKeys. Everything else about candidacy is asked of the walk's own
	// code, so the two cannot drift.
	plan := g.planFor(model, keys)
	plan.ignoreCircuitState = true
	keys = g.eligibleKeys(plan, nil)

	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, key := range keys {
		if p, registered := g.providers[key]; registered && g.candidateLocked(key, p, model, nil) {
			return true
		}
	}
	return false
}

// ServedProviders returns the providers this gateway routes to: the registered
// provider each configured target names, in target order, de-duplicated.
//
// It is the single authority for "which providers does this gateway serve", and
// every surface that describes this gateway's inventory answers from it, so
// none of them can advertise a provider another refuses. /v1/capabilities
// answered from the REGISTERED set instead and reported five providers on a
// gateway with one target, four of which 404 on every routed surface.
//
// Both halves of the test are load-bearing. Registration follows the
// credentials in the environment, so a target whose key has not rolled out yet
// serves nothing; the targets list is the allowlist, so a provider with a
// credential and no target serves nothing either.
//
// It is read from the live config under the lock rather than from the routing
// snapshot, because the snapshot is republished only when the provider set, the
// catalog, or discovery changes — never on a config reload, which is exactly
// when this set changes.
func (g *Gateway) ServedProviders() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	served := make([]string, 0, len(g.config.Targets))
	for _, t := range g.config.Targets {
		if _, registered := g.providers[t.VirtualKey]; !registered {
			continue
		}
		if !slices.Contains(served, t.VirtualKey) {
			served = append(served, t.VirtualKey)
		}
	}
	return served
}

// IsTargetedProvider reports whether any configured target names this provider.
//
// Registration follows credentials in the environment; routing follows the
// names the config lists. The two sets are deliberately allowed to differ — a
// credential can be present for a provider no target uses — so "registered" is
// not an admission answer. Every surface that decides where a request body may
// go asks this instead, including the pass-through, which reaches providers by
// model ownership and by the X-Provider header.
func (g *Gateway) IsTargetedProvider(name string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, t := range g.config.Targets {
		if t.VirtualKey == name {
			return true
		}
	}
	return false
}

// ModelsFor reports the models one registered provider serves. It satisfies
// providers.ProviderSource so admin handlers and the CLI read the same composed
// list /v1/models publishes, instead of asking the provider for an inventory it
// no longer keeps.
func (g *Gateway) ModelsFor(name string) []providers.ModelInfo {
	return g.routing().modelsFor(name)
}

// modelsFor applies the advertised-model precedence (issue #146) — live
// discovery > catalog > the models this instance's provider config named — and
// then adds whatever targets[].models declared.
//
// The precedence and the addition are answering two different questions, which
// is why the operator's list is not simply a fourth tier. The three tiers are
// rival descriptions of one inventory, so the best-informed one wins outright:
// a live /models call describes the upstream better than a static catalog does,
// and shipping the union would re-advertise every model the catalog still lists
// and the upstream has retired. targets[].models is not a rival description. It
// is the operator saying "this one as well", precisely because no automatic
// source reported it — so it has to survive whichever tier won, or it would be
// swallowed by the first provider that has any catalog coverage at all and the
// declaration would route while staying invisible.
//
// The result is still a subset of what modelsForRoutingLocked admits — that is
// the union of all four — so the invariant this function has always kept holds:
// a model advertised here is always one the routing index routes, never the
// reverse.
func (s *routingSnapshot) modelsFor(name string) []providers.ModelInfo {
	p, ok := s.providers[name]
	if !ok {
		return nil
	}
	var inventory []providers.ModelInfo
	switch {
	case len(s.discovered[name]) > 0:
		inventory = s.discovered[name]
	case len(s.catalogModels[name]) > 0:
		inventory = core.ModelsFromList(name, s.catalogModels[name])
	default:
		inventory = core.ModelsFromList(name, configuredModelsOf(p))
	}

	declared := s.declaredModels[name]
	if len(declared) == 0 {
		return inventory
	}
	// Clone before appending: inventory may alias s.discovered's slice, which
	// every other reader of this snapshot shares.
	out := slices.Clone(inventory)
	for _, m := range core.ModelsFromList(name, declared) {
		if !slices.ContainsFunc(out, func(have providers.ModelInfo) bool { return have.ID == m.ID }) {
			out = append(out, m)
		}
	}
	return out
}

// GetProvider returns a registered provider by name.
func (g *Gateway) GetProvider(name string) (providers.Provider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p, ok := g.providers[name]
	return p, ok
}

// pricingProviderFor returns the canonical provider registered under key. An
// unknown key falls back to itself and remains unpriced by the model catalog.
func (g *Gateway) pricingProviderFor(key string) string {
	p, ok := g.GetProvider(key)
	if !ok || p == nil {
		return key
	}
	return providers.CanonicalName(p)
}

// Get satisfies providers.ProviderSource (alias for GetProvider).
func (g *Gateway) Get(name string) (providers.Provider, bool) {
	return g.GetProvider(name)
}

// ListProviders returns the names of all registered providers.
func (g *Gateway) ListProviders() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, len(g.providerNames))
	copy(names, g.providerNames)
	return names
}

// List satisfies providers.ProviderSource (alias for ListProviders).
func (g *Gateway) List() []string {
	return g.ListProviders()
}

// FindByModel returns the first REGISTERED provider that supports the given
// model. A configured alias is resolved first, the same way Route resolves it.
//
// It is a lookup, not an admission check, and the two answer different
// questions: registration follows the credentials in the environment, routing
// follows the targets list, and a provider can be registered without any target
// naming it. Handlers used to gate on this before calling Route and reported a
// miss with their own status, so the same unroutable model was a 400 on chat
// and a 404 on embeddings. Let Route decide — it consults the targets and
// reports through apierror.WriteRouteError.
func (g *Gateway) FindByModel(model string) (providers.Provider, bool) {
	return g.routing().findProvider(g.resolveModelAlias(model))
}

// FindStreamingByModel returns the first registered streaming-capable provider
// that supports the given model, resolving a configured alias first for the
// same reason as FindByModel.
func (g *Gateway) FindStreamingByModel(model string) (providers.StreamProvider, bool) {
	return g.routing().findStreamingProvider(g.resolveModelAlias(model))
}

// Close cleans up resources.
//
// Cancels the gateway shutdown context (which signals hook workers and the
// catalog refresh worker to exit) and waits up to 5s for workers to finish so
// in-flight hook dispatches are not abruptly killed. Returns nil even if the worker drain
// times out — Close must never block indefinitely (a panicking hook could
// otherwise wedge shutdown).
//
// For stdio MCP servers this terminates their subprocesses; HTTP MCP servers
// require no explicit teardown.
//
// Safe to call multiple times; subsequent calls are no-ops.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		g.shutdownCancel()
		// Snapshot and clear the MCP registry under the same lock that
		// ReloadConfig writes it through. Reading g.mcpRegistry after unlocking
		// raced with a concurrent reload, and the losing interleaving left a
		// live registry unclosed — every subprocess leaked for the life of the
		// process, since closeOnce can never fire again.
		g.mu.Lock()
		plugins := g.plugins
		g.plugins = plugin.NewManager(g.log)
		mcpRegistry := g.mcpRegistry
		g.mcpRegistry = nil
		g.mcpExecutor = nil
		g.closed = true
		g.mu.Unlock()
		if err := closePluginManager(plugins); err != nil {
			g.log.Warn("plugin close failed during gateway shutdown", "error", err)
		}
		// Retire MCP *inside* the bounded drain, not ahead of it. With no active
		// holders Close tears transports down inline, and one wedged stdio server
		// can spend seconds in the graceful/SIGTERM/SIGKILL ladder — ahead of the
		// budget that would consume the whole grace period before the timer even
		// starts, and an orchestrator would SIGKILL mid-cleanup.
		//
		// The wait also covers registries retired by earlier reloads, whose
		// teardown runs in its own goroutine. Without that a reload shortly
		// before shutdown could leave a termination ladder running past process
		// exit, orphaning exactly the subprocess the sweep exists to reap.
		done := make(chan struct{})
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelDrain()
		go func() {
			if mcpRegistry != nil {
				if err := mcpRegistry.Close(); err != nil {
					g.log.Warn("mcp registry close failed during gateway shutdown", "error", err)
				}
				// Close returns immediately while requests still hold the
				// registry, finishing teardown in a goroutine of its own. That
				// goroutine is not one of pendingMCPCloses, so without this wait
				// the drain below had nothing to wait on and the process could
				// exit with the stdio subprocess still running.
				if err := mcpRegistry.WaitClosed(drainCtx); err != nil {
					g.log.Warn("mcp registry teardown did not finish within the shutdown budget", "error", err)
				}
			}
			g.pendingMCPCloses.Wait()
			g.hooks.wait()
			g.catalogRefreshDone.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return nil
}
