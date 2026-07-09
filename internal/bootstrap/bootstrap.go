package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/providers"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
)

// CheckProductionSafety returns an error if ALLOW_UNAUTHENTICATED_PROXY=true is
// combined with GATEWAY_ENV=production.  Allowing unauthenticated proxy access
// in a production environment silently removes all /v1/* data-plane auth, so
// the gateway refuses to start rather than serve in an insecure state.
//
// Outside of production (GATEWAY_ENV unset or any value other than
// "production"), the flag is honoured and a warning is emitted at startup
// (existing dev-mode behaviour is preserved).
func CheckProductionSafety() error {
	allowUnauth := strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_UNAUTHENTICATED_PROXY")), "true")
	isProduction := strings.EqualFold(strings.TrimSpace(os.Getenv("GATEWAY_ENV")), "production")

	if allowUnauth && isProduction {
		return fmt.Errorf(
			"ALLOW_UNAUTHENTICATED_PROXY=true is not allowed when GATEWAY_ENV=production: " +
				"all /v1/* data-plane endpoints would be unauthenticated; " +
				"unset ALLOW_UNAUTHENTICATED_PROXY or change GATEWAY_ENV to a non-production value",
		)
	}
	return nil
}

// defaultListenAddr is the address the HTTP server listens on when PORT is unset.
const defaultListenAddr = ":8080"

// shutdownGracePeriod bounds how long graceful shutdown waits for in-flight
// HTTP connections to drain before returning.
const shutdownGracePeriod = 15 * time.Second

// Serve runs the full gateway server startup sequence and blocks until the
// server shuts down.  It exits the process on fatal errors.
func Serve() {
	logging.Setup(os.Getenv("LOG_LEVEL"), os.Getenv("LOG_FORMAT"))

	if err := CheckProductionSafety(); err != nil {
		logging.Logger.Error("startup blocked: unsafe configuration", "error", err)
		os.Exit(1)
	}

	gw, srv, cfgManager, keyStore, logReader, otelShutdown := buildServer()
	listenErr := runUntilShutdown(gw, srv)
	gracefulShutdown(srv, gw, cfgManager, keyStore, logReader, otelShutdown, listenErr)
}

// buildServer runs the startup sequence: it loads configuration, registers
// providers, initializes observability and the persistence stores, constructs
// the router and HTTP server, prints the startup banner, and logs readiness.
// It exits the process on any fatal initialization error and returns the
// long-lived components needed to run and later shut down the server.
func buildServer() (
	*aigateway.Gateway,
	*http.Server,
	admin.ConfigManager,
	admin.Store,
	requestlog.Reader,
	gwotel.ShutdownFunc,
) {
	cfg := LoadConfig()
	registry := RegisterProviders()
	masterKey := ResolveMasterKey()

	if strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_UNAUTHENTICATED_PROXY")), "true") {
		logging.Logger.Warn("ALLOW_UNAUTHENTICATED_PROXY is set -- proxy routes are unauthenticated (dev mode only; not for production)")
	}

	if len(registry.List()) == 0 {
		logging.Logger.Warn("no providers configured; set provider API keys (e.g. OPENAI_API_KEY) or OLLAMA_HOST, or add them later via the admin API")
	}

	gw := BuildGateway(cfg, registry)

	// Initialise OpenTelemetry. Init returns a NoOp provider (and a
	// no-op shutdown) when neither an OTLP endpoint nor any enabled
	// exporter is configured, so this is free for users who don't opt in.
	var obsCfg aigateway.ObservabilityConfig
	if cfg != nil {
		obsCfg = cfg.Observability
	}
	obsProvider, otelShutdown, err := gwotel.Init(context.Background(), otelConfigFromGateway(obsCfg))
	if err != nil {
		logging.Logger.Error("failed to initialize observability", "error", err)
		os.Exit(1)
	}
	gw.SetObservability(obsProvider)

	// No lifecycle context exists yet at this point in startup (signal.NotifyContext
	// is only set up later, in runUntilShutdown), matching the gwotel.Init call
	// above — these are one-time store-initialization calls, not per-request work.
	cfgManager, configStoreBackend, err := CreateConfigManagerFromEnv(context.Background(), gw)
	if err != nil {
		logging.Logger.Error("failed to initialize config store", "error", err)
		os.Exit(1)
	}

	keyStore, keyStoreBackend, err := CreateKeyStoreFromEnv(context.Background())
	if err != nil {
		logging.Logger.Error("failed to initialize API key store", "error", err)
		os.Exit(1)
	}
	LogDeprecatedBootstrapKeys()

	var corsOrigins []string
	if origins := os.Getenv("CORS_ORIGINS"); origins != "" {
		corsOrigins = strings.Split(origins, ",")
	}

	// TRUSTED_PROXIES is a comma-separated list of CIDR blocks whose
	// X-Forwarded-For / X-Real-IP headers are trusted for client-IP resolution.
	// An empty or unset value falls back to the loopback default
	// (127.0.0.0/8, ::1/128), which is appropriate for a local reverse proxy.
	trustedProxies, err := httpserver.ParseTrustedProxyCIDRs(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		logging.Logger.Error("invalid TRUSTED_PROXIES", "error", err)
		os.Exit(1)
	}

	rlStore := NewRateLimitStore()
	logReader, logMaintainer, logReaderBackend, err := CreateRequestLogReaderFromEnv(context.Background())
	if err != nil {
		logging.Logger.Error("failed to initialize request log reader", "error", err)
		os.Exit(1)
	}

	r := httpserver.NewRouter(registry, keyStore, corsOrigins, gw, cfgManager, rlStore, logReader, logMaintainer, masterKey, trustedProxies)

	addr := defaultListenAddr
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	srv := httpserver.NewServer(addr, r)

	PrintStartupBanner(addr, registry, cfg, masterKey, keyStoreBackend, configStoreBackend)
	logging.Logger.Info("ferrogw started",
		"version", version.Short(),
		"addr", addr,
		"providers", len(registry.List()),
		"config_store", configStoreBackend,
		"api_key_store", keyStoreBackend,
		"request_log_store", logReaderBackend,
	)

	return gw, srv, cfgManager, keyStore, logReader, otelShutdown
}

// runUntilShutdown starts the HTTP server and optional live model discovery,
// then blocks until an OS signal or a fatal listen error. It returns the listen
// error observed, if any.
func runUntilShutdown(gw *aigateway.Gateway, srv *http.Server) error {
	// Run the server in a goroutine so the main goroutine can block on signal
	// or a fatal listen error.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	// Block until OS signal or a fatal server error.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// Opt-in live model discovery: refreshes provider model lists in the
	// background and stops when the lifecycle ctx is cancelled on shutdown.
	if interval, ok := discoveryIntervalFromEnv(); ok {
		if err := gw.StartDiscovery(ctx, interval); err != nil {
			logging.Logger.Warn("model discovery not started", "error", err)
		} else {
			logging.Logger.Info("model discovery enabled", "interval", interval.String())
		}
	}

	var listenErr error
	select {
	case <-ctx.Done():
		logging.Logger.Info("shutdown signal received")
	case listenErr = <-serveErr:
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			logging.Logger.Error("server error", "error", listenErr)
		}
	}
	stop() // release signal resources; called explicitly so os.Exit below doesn't bypass it
	return listenErr
}

// gracefulShutdown drains the HTTP server, closes the persistence stores, and
// flushes OTel exporters in that order, then exits the process if the server
// terminated on a fatal listen error.
func gracefulShutdown(
	srv *http.Server,
	gw *aigateway.Gateway,
	cfgManager admin.ConfigManager,
	keyStore admin.Store,
	logReader requestlog.Reader,
	otelShutdown gwotel.ShutdownFunc,
	listenErr error,
) {
	// Shutdown drains active connections before returning — CloseResources must
	// come after so in-flight requests can still reach the stores.
	logging.Logger.Info("shutting down gracefully")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logging.Logger.Error("shutdown error", "error", err)
	}
	cancel()

	if err := httpserver.CloseResources(
		httpserver.NamedResource{Name: "gateway", Value: gw},
		httpserver.NamedResource{Name: "config manager", Value: cfgManager},
		httpserver.NamedResource{Name: "api key store", Value: keyStore},
		httpserver.NamedResource{Name: "request log store", Value: logReader},
	); err != nil {
		logging.Logger.Error("shutdown cleanup error", "error", err)
	}

	// Drain OTel exporters last so spans emitted during the rest of the
	// shutdown sequence still reach the collector.
	// The ShutdownFunc returned by gwotel.Init applies its own internal
	// deadline derived from cfg.ShutdownGrace, so we pass context.Background()
	// here rather than duplicating the duration parse.
	if err := otelShutdown(context.Background()); err != nil {
		logging.Logger.Error("otel shutdown error", "error", err)
	}

	logging.Logger.Info("server stopped")

	if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
		os.Exit(1)
	}
}

// discoveryIntervalFromEnv reads the FERRO_MODEL_DISCOVERY_INTERVAL env var and
// returns the opt-in refresh interval for live model discovery. It is pure: it
// performs no logging. Returns (0, false) when the var is unset/empty, fails to
// parse, or resolves to a duration below the 1-minute minimum (which guards
// against a hot-loop of provider API calls); otherwise (interval, true).
func discoveryIntervalFromEnv() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv("FERRO_MODEL_DISCOVERY_INTERVAL"))
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Minute {
		return 0, false
	}
	return d, true
}

// ResolveMasterKey returns the master key from the MASTER_KEY env var.
func ResolveMasterKey() string {
	return strings.TrimSpace(os.Getenv("MASTER_KEY"))
}

// LogDeprecatedBootstrapKeys warns about deprecated env vars.
func LogDeprecatedBootstrapKeys() {
	if strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_KEY")) != "" {
		logging.Logger.Warn("ADMIN_BOOTSTRAP_KEY is deprecated -- use MASTER_KEY instead")
	}
	if strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_READ_ONLY_KEY")) != "" {
		logging.Logger.Warn("ADMIN_BOOTSTRAP_READ_ONLY_KEY is deprecated -- use MASTER_KEY instead")
	}
}

// LoadConfig loads and validates the gateway config from GATEWAY_CONFIG env var.
// Returns nil if GATEWAY_CONFIG is not set (caller uses default config).
func LoadConfig() *aigateway.Config {
	cfgPath := os.Getenv("GATEWAY_CONFIG")
	if cfgPath == "" {
		return nil
	}
	loaded, err := aigateway.LoadConfig(cfgPath)
	if err != nil {
		logging.Logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	if err := aigateway.ValidateConfig(*loaded); err != nil {
		logging.Logger.Error("invalid config", "error", err)
		os.Exit(1)
	}
	logging.Logger.Info("config loaded",
		"strategy", loaded.Strategy.Mode,
		"targets", len(loaded.Targets),
	)
	return loaded
}

// RegisterProviders auto-registers all providers found via environment variables.
func RegisterProviders() *providers.Registry {
	registry := providers.NewRegistry()
	registerProviderEntries(registry, providers.AllProviders())
	registerBedrockProvider(registry)
	return registry
}

func registerProviderEntries(registry *providers.Registry, entries []providers.ProviderEntry) {
	for _, entry := range entries {
		if entry.ID == providers.NameBedrock {
			continue // handled below with its dual-key detection
		}

		cfg := providers.ProviderConfigFromEnv(entry)
		if cfg == nil {
			continue // required env var unset — provider not configured, skip silently
		}

		p, err := entry.Build(cfg)
		if err != nil {
			logging.Logger.Warn("provider init skipped", "provider", entry.ID, "error", err)
			continue
		}
		registry.Register(p)
		logging.Logger.Info("provider registered", "provider", entry.ID)
	}
}

func registerBedrockProvider(registry *providers.Registry) {
	// AWS Bedrock: register if AWS_REGION, AWS_ACCESS_KEY_ID, or
	// AWS_BEARER_TOKEN_BEDROCK is set.
	if region := os.Getenv("AWS_REGION"); region != "" ||
		os.Getenv("AWS_ACCESS_KEY_ID") != "" ||
		os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
		p, err := bedrockpkg.NewWithOptions(bedrockpkg.Options{
			Region:          os.Getenv("AWS_REGION"),
			BearerToken:     os.Getenv("AWS_BEARER_TOKEN_BEDROCK"),
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		})
		if err != nil {
			logging.Logger.Error("provider init failed", "provider", providers.NameBedrock, "error", err)
		} else {
			registry.Register(p)
			logging.Logger.Info("provider registered", "provider", providers.NameBedrock, "region", p.Region())
		}
	}
}

// BuildGateway constructs the Gateway, wires providers, and loads plugins.
// If cfg is nil a default fallback config is created from the registry.
func BuildGateway(cfg *aigateway.Config, registry *providers.Registry) *aigateway.Gateway {
	if cfg == nil {
		defaultTargets := make([]aigateway.Target, 0, len(registry.List()))
		for _, name := range registry.List() {
			defaultTargets = append(defaultTargets, aigateway.Target{VirtualKey: name})
		}
		cfg = &aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
			Targets:  defaultTargets,
		}
		logging.Logger.Info("using default config",
			"strategy", cfg.Strategy.Mode,
			"targets", len(cfg.Targets),
		)
	}

	gw, err := aigateway.New(*cfg)
	if err != nil {
		logging.Logger.Error("failed to create gateway", "error", err)
		os.Exit(1)
	}
	for _, name := range registry.List() {
		if p, ok := registry.Get(name); ok {
			gw.RegisterProvider(p)
		}
	}
	if len(cfg.Plugins) > 0 {
		if err := gw.LoadPlugins(); err != nil {
			logging.Logger.Error("failed to load plugins", "error", err)
			os.Exit(1)
		}
		logging.Logger.Info("plugins loaded", "count", len(cfg.Plugins))
	}
	return gw
}

const (
	// defaultRateLimitRPS and defaultRateLimitBurst are applied when
	// RATE_LIMIT_RPS/RATE_LIMIT_BURST are unset, so the per-IP limiter is
	// enabled out of the box rather than opt-in.
	defaultRateLimitRPS   = 20.0
	defaultRateLimitBurst = 40.0
)

// defaultRateLimitMaxKeys caps the number of per-IP limiters tracked at
// once, preventing unbounded memory growth from a flood of distinct client
// IPs. A var (not const) so tests can shrink it to exercise the eviction cap
// cheaply; production always uses this value.
var defaultRateLimitMaxKeys = 100_000

// NewRateLimitStore builds a per-IP token-bucket store from env vars. Rate
// limiting is enabled by default at defaultRateLimitRPS/defaultRateLimitBurst,
// capped at defaultRateLimitMaxKeys tracked IPs. Set RATE_LIMIT_RPS=0 to opt
// out entirely; a positive RATE_LIMIT_RPS overrides the default rate. Setting
// RATE_LIMIT_RPS alone always resets the burst to defaultRateLimitBurst too —
// set RATE_LIMIT_BURST explicitly if a custom rate needs a matching burst.
//
// The store keys on the resolved client IP (see RealIPMiddleware), which only
// trusts X-Forwarded-For/X-Real-IP from TRUSTED_PROXIES (default: loopback).
// Behind a reverse proxy/ingress/LB outside that range, every request
// resolves to the proxy's own IP, collapsing this into one shared bucket for
// all clients — set TRUSTED_PROXIES to the proxy's real CIDR for this to
// rate-limit per client rather than globally.
func NewRateLimitStore() *ratelimit.Store {
	rps := defaultRateLimitRPS
	if rpsStr := os.Getenv("RATE_LIMIT_RPS"); rpsStr != "" {
		v, err := strconv.ParseFloat(rpsStr, 64)
		switch {
		case err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0:
			logging.Logger.Warn("ignoring invalid RATE_LIMIT_RPS; using default", "value", rpsStr, "default", defaultRateLimitRPS)
		case v == 0:
			logging.Logger.Info("rate limiting disabled via RATE_LIMIT_RPS=0")
			return nil
		default:
			rps = v
		}
	}

	burst := defaultRateLimitBurst
	if burstStr := os.Getenv("RATE_LIMIT_BURST"); burstStr != "" {
		if v, err := strconv.ParseFloat(burstStr, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 {
			burst = v
		} else {
			logging.Logger.Warn("ignoring invalid RATE_LIMIT_BURST; using default", "value", burstStr, "default", defaultRateLimitBurst)
		}
	}

	store := ratelimit.NewStoreWithMax(rps, burst, defaultRateLimitMaxKeys)
	logging.Logger.Info("rate limiting enabled", "rps", rps, "burst", burst, "max_keys", defaultRateLimitMaxKeys)
	return store
}

// PrintStartupBanner prints a branded, informative banner to stderr on server start.
func PrintStartupBanner(addr string, registry *providers.Registry, cfg *aigateway.Config, masterKey, keyStoreBackend, configStoreBackend string) {
	const (
		orange = "\033[38;5;208m"
		bold   = "\033[1m"
		white  = "\033[97m"
		dim    = "\033[2m"
		green  = "\033[92m"
		yellow = "\033[93m"
		reset  = "\033[0m"
	)

	strategy := "fallback"
	pluginCount := 0
	if cfg != nil {
		strategy = string(cfg.Strategy.Mode)
		pluginCount = len(cfg.Plugins)
	}

	providerCount := len(registry.List())

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  %sFERRO LABS  AI GATEWAY%s  %s%s%s\n",
		bold+white, reset, dim, version.Short(), reset)
	fmt.Fprintf(os.Stderr, "  %s->%s  http://localhost%s\n",
		orange, reset, addr)
	fmt.Fprintf(os.Stderr, "  %s->%s  http://localhost%s/dashboard\n",
		dim, reset, addr)
	fmt.Fprintf(os.Stderr, "\n")

	// Provider status.
	fmt.Fprintf(os.Stderr, "  Providers\n")
	topProviders := []struct {
		id     string
		envVar string
	}{
		{"openai", "OPENAI_API_KEY"},
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
		{"groq", "GROQ_API_KEY"},
		{"mistral", "MISTRAL_API_KEY"},
	}
	for _, tp := range topProviders {
		if _, ok := registry.Get(tp.id); ok {
			fmt.Fprintf(os.Stderr, "    %s[OK]%s %s\n", green, reset, tp.id)
		} else {
			fmt.Fprintf(os.Stderr, "    %s[-]%s  %s (%s not set)\n", dim, reset, tp.id, tp.envVar)
		}
	}
	topSet := map[string]bool{"openai": true, "anthropic": true, "gemini": true, "groq": true, "mistral": true}
	for _, name := range registry.List() {
		if !topSet[name] {
			fmt.Fprintf(os.Stderr, "    %s[OK]%s %s\n", green, reset, name)
		}
	}
	fmt.Fprintf(os.Stderr, "    %s%d providers | %s | %d plugins%s\n",
		dim, providerCount, strategy, pluginCount, reset)
	fmt.Fprintf(os.Stderr, "\n")

	// Auth status.
	fmt.Fprintf(os.Stderr, "  Auth\n")
	if masterKey != "" {
		fmt.Fprintf(os.Stderr, "    Master key: %sconfigured%s\n", green, reset)
	} else {
		fmt.Fprintf(os.Stderr, "    %s[!] No MASTER_KEY set -- run 'ferrogw init' to generate one%s\n", yellow, reset)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Store warnings.
	hasWarnings := false
	if keyStoreBackend == BackendMemory {
		if !hasWarnings {
			fmt.Fprintf(os.Stderr, "  Warnings\n")
			hasWarnings = true
		}
		fmt.Fprintf(os.Stderr, "    %s[!] API key store: in-memory (keys lost on restart)%s\n", yellow, reset)
	}
	if configStoreBackend == BackendMemory {
		if !hasWarnings {
			fmt.Fprintf(os.Stderr, "  Warnings\n")
			hasWarnings = true
		}
		fmt.Fprintf(os.Stderr, "    %s[!] Config store: in-memory (config lost on restart)%s\n", yellow, reset)
	}
	if hasWarnings {
		fmt.Fprintf(os.Stderr, "    %sSet API_KEY_STORE_BACKEND=sqlite for persistence%s\n", dim, reset)
		fmt.Fprintf(os.Stderr, "\n")
	}
}
