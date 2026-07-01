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
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/internal/mcp"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Gateway is the main entry point for routing LLM requests.
type Gateway struct {
	mu                 sync.RWMutex
	config             Config
	catalog            models.Catalog
	providers          map[string]providers.Provider
	providerNames      []string
	strategy           strategies.Strategy
	streamingContent   []streamingContentCondition
	plugins            *plugin.Manager
	closeOnce          sync.Once
	hooks              []EventHookFunc
	hookSnapshot       atomic.Value
	hookDispatchQ      chan hookDispatch
	hookWorkersDone    sync.WaitGroup
	catalogRefreshDone sync.WaitGroup
	// shutdownCtx is a lifecycle context, not a request context. Storing it on the
	// struct is the intended idiom here: it is created once in New, parents the
	// gateway's background workers (hook dispatch, catalog refresh, MCP init), and
	// is cancelled by Close() to signal shutdown. It is never a per-request context.
	shutdownCtx      context.Context
	shutdownCancel   context.CancelFunc
	circuitBreakers  map[string]*circuitbreaker.CircuitBreaker
	discoveredModels map[string][]providers.ModelInfo
	latencyTracker   *latency.Tracker
	modelIndex       modelLookupIndex

	// obs is the observability provider used to emit per-request spans.
	// Defaults to observability.NoOp() when SetObservability has not
	// been called, which guarantees zero allocations on the hot path
	// (issue #49 acceptance criterion).
	obs observability.Provider

	// obsEventsActive is true when the installed Provider implements
	// observability.EventRecordingProvider and RecordingEnabled() returned
	// true at the time SetObservability was called.  It is read on the
	// hot path without holding the gateway mutex — it is set once before
	// traffic starts, so no additional synchronisation is required.
	obsEventsActive bool

	// MCP fields — nil when no MCPServers are configured.
	mcpRegistry *mcp.Registry
	mcpExecutor *mcp.Executor
	mcpInitDone chan struct{} // closed when background MCP init goroutine completes
}

const (
	hookDispatchQueueSize  = 256
	catalogRefreshInterval = 24 * time.Hour
)

// New creates a new Gateway instance with the given configuration.
// It validates cfg with ValidateConfig before initialising any resources,
// returning an error immediately if the config is invalid. This matches the
// fail-fast behaviour already present in ReloadConfig and the CLI.
func New(cfg Config) (*Gateway, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	streamingContent, err := compileStreamingContentConditions(cfg.Strategy.Mode, cfg.Strategy.ContentConditions)
	if err != nil {
		return nil, err
	}

	catalogResult, err := models.LoadWithInfo()
	recordCatalogLoad(catalogResult.Source, err)
	catalog := catalogResult.Catalog
	if err != nil {
		// Non-fatal: operate without model metadata (no enrichment / cost reporting).
		slog.Error("model catalog unavailable; continuing without catalog metadata", "url", catalogResult.URLForLog(), "error", err)
		catalog = models.Catalog{}
	}

	gw := &Gateway{
		config:           cfg,
		catalog:          catalog,
		providers:        make(map[string]providers.Provider),
		streamingContent: streamingContent,
		plugins:          plugin.NewManager(),
		circuitBreakers:  make(map[string]*circuitbreaker.CircuitBreaker),
		discoveredModels: make(map[string][]providers.ModelInfo),
		latencyTracker:   latency.New(0), // default window size (100 samples)
		modelIndex: modelLookupIndex{
			exactProviders:       make(map[string][]string),
			exactStreamProviders: make(map[string][]string),
			exactEmbedProviders:  make(map[string][]string),
			exactImageProviders:  make(map[string][]string),
		},
		hookDispatchQ: make(chan hookDispatch, hookDispatchQueueSize),
		obs:           observability.NoOp(),
	}
	gw.shutdownCtx, gw.shutdownCancel = context.WithCancel(context.Background()) //nolint:gosec // canceled by Gateway.Close()
	gw.hookSnapshot.Store([]EventHookFunc{})
	gw.startHookWorkers()
	gw.startCatalogRefresh()

	// Wire MCP from config. In New the gateway is not yet published, so no lock
	// is held here; the field writes are safe.
	gw.wireMCPLocked(cfg, "mcp: server initialization failed")

	gw.mu.Lock()
	gw.ensureCircuitBreakersLocked()
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
	// hot path can skip Event construction when nothing is listening.
	g.obsEventsActive = false
	if er, ok := p.(observability.EventRecordingProvider); ok {
		g.obsEventsActive = er.RecordingEnabled()
	}
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
				g.refreshCatalog()
			}
		}
	}()
}

func (g *Gateway) refreshCatalog() {
	result, err := models.LoadWithInfo()
	recordCatalogLoad(result.Source, err)
	if err != nil {
		slog.Error("model catalog refresh failed", "url", result.URLForLog(), "error", err)
		return
	}
	if result.Source != models.LoadSourceRemote {
		slog.Warn("model catalog refresh skipped; keeping current catalog", "url", result.URLForLog(), "source", result.Source)
		return
	}

	g.mu.Lock()
	g.catalog = result.Catalog
	// The exact-match routing index is derived from the catalog (issue #146),
	// so it must be rebuilt whenever the catalog is replaced — otherwise the
	// 24h refresh would leave routing frozen at the startup catalog while
	// /v1/models reflects the new one.
	g.rebuildModelIndexesLocked()
	if g.config.Strategy.Mode == ModeCostOptimized {
		g.strategy = nil
	}
	g.mu.Unlock()

	slog.Info("model catalog refreshed", "url", result.URLForLog(), "models", len(result.Catalog))
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

// RegisterProvider registers a provider with the gateway.
func (g *Gateway) RegisterProvider(p providers.Provider) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.providers[p.Name()]; !exists {
		g.providerNames = append(g.providerNames, p.Name())
	}
	g.providers[p.Name()] = p
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
func (g *Gateway) ReloadConfig(ctx context.Context, cfg Config) error {
	_ = ctx
	if err := ValidateConfig(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	streamingContent, err := compileStreamingContentConditions(cfg.Strategy.Mode, cfg.Strategy.ContentConditions)
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	plugins, err := buildPluginManager(cfg.Plugins)
	if err != nil {
		return err
	}
	pluginsInstalled := false
	defer func() {
		if pluginsInstalled {
			return
		}
		if closeErr := closePluginManager(plugins); closeErr != nil {
			slog.Warn("plugin close failed after config reload error", "error", closeErr)
		}
	}()
	g.mu.Lock()
	oldPlugins := g.plugins
	g.config = cfg
	g.streamingContent = streamingContent
	g.plugins = plugins
	pluginsInstalled = true
	g.strategy = nil // force rebuild on next request
	g.circuitBreakers = make(map[string]*circuitbreaker.CircuitBreaker)
	g.ensureCircuitBreakersLocked()

	// Re-register MCP servers from the new config (clears MCP state when none).
	g.wireMCPLocked(cfg, "mcp: server initialization failed after reload")

	g.mu.Unlock()
	if err := closePluginManager(oldPlugins); err != nil {
		slog.Warn("plugin close failed during config reload", "error", err)
	}
	return nil
}

// GetConfig returns a copy of the current configuration.
func (g *Gateway) GetConfig() Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.config
}

// LoadPlugins initializes and registers plugins from the gateway configuration.
func (g *Gateway) LoadPlugins() error {
	g.mu.RLock()
	configs := append([]PluginConfig(nil), g.config.Plugins...)
	g.mu.RUnlock()
	plugins, err := buildPluginManager(configs)
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

func buildPluginManager(configs []PluginConfig) (*plugin.Manager, error) {
	plugins := plugin.NewManager()
	for _, pc := range configs {
		if !pc.Enabled {
			continue
		}
		factory, ok := plugin.GetFactory(pc.Name)
		if !ok {
			_ = plugins.Close()
			return nil, fmt.Errorf("unknown plugin: %s", pc.Name)
		}
		p := factory()
		if err := p.Init(pc.Config); err != nil {
			_ = plugins.Close()
			_ = p.Close()
			return nil, fmt.Errorf("plugin %s init failed: %w", pc.Name, err)
		}
		stage := plugin.Stage(pc.Stage)
		if err := plugins.Register(stage, p); err != nil {
			_ = plugins.Close()
			_ = p.Close()
			return nil, fmt.Errorf("plugin %s register failed: %w", pc.Name, err)
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

// ── Registry-consolidation helpers ──────────────────────────────────────────
// These methods make *Gateway satisfy providers.ProviderSource so that HTTP
// handlers that previously held a *providers.Registry can accept the gateway
// directly instead.

// AllModels returns ModelInfo from all registered providers.
// If auto-discovery has run for a provider, discovered models take precedence
// over the provider's static model list.
func (g *Gateway) AllModels() []providers.ModelInfo {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var models []providers.ModelInfo
	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok {
			continue
		}
		// Precedence (issue #146): live discovery > catalog > hardcoded fallback.
		if discovered, ok := g.discoveredModels[name]; ok && len(discovered) > 0 {
			models = append(models, discovered...)
		} else if catModels := g.catalog.ModelsForProvider(name); len(catModels) > 0 {
			models = append(models, core.ModelsFromList(name, catModels)...)
		} else {
			models = append(models, p.Models()...)
		}
	}
	return models
}

// GetProvider returns a registered provider by name.
func (g *Gateway) GetProvider(name string) (providers.Provider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p, ok := g.providers[name]
	return p, ok
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

// FindByModel returns the first registered provider that supports the given model.
func (g *Gateway) FindByModel(model string) (providers.Provider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.findProviderByModelLocked(model)
}

// FindStreamingByModel returns the first registered streaming-capable provider
// that supports the given model.
func (g *Gateway) FindStreamingByModel(model string) (providers.StreamProvider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.findStreamingProviderByModelLocked(model)
}

// Close cleans up resources.
//
// Cancels the gateway shutdown context (which signals hook workers and the
// catalog refresh worker to exit) and waits up to 5s for workers to finish so
// in-flight hook dispatches are not abruptly killed. Returns nil even if the worker drain
// times out — Close must never block indefinitely (a panicking hook could
// otherwise wedge shutdown).
//
// Safe to call multiple times; subsequent calls are no-ops.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		g.shutdownCancel()
		g.mu.Lock()
		plugins := g.plugins
		g.plugins = plugin.NewManager()
		g.mu.Unlock()
		if err := closePluginManager(plugins); err != nil {
			slog.Warn("plugin close failed during gateway shutdown", "error", err)
		}
		done := make(chan struct{})
		go func() {
			g.hookWorkersDone.Wait()
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
