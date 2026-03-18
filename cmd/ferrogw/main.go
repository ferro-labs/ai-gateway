package main

import (
	"context"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/providers"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
	webassets "github.com/ferro-labs/ai-gateway/web"

	// Register built-in plugins so they can be loaded from config.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/budget"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

var webTemplates = template.Must(template.ParseFS(webassets.Assets, "*.html"))

func main() {
	// Initialise structured logging before anything else.
	logging.Setup(os.Getenv("LOG_LEVEL"), os.Getenv("LOG_FORMAT"))

	cfg := loadConfig()
	registry := registerProviders()

	if len(registry.List()) == 0 {
		logging.Logger.Error("no providers configured; set at least one provider API key (e.g. OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY) or OLLAMA_HOST for local models")
		os.Exit(1)
	}

	gw := buildGateway(cfg, registry)
	cfgManager, configStoreBackend, err := createConfigManagerFromEnv(gw)
	if err != nil {
		logging.Logger.Error("failed to initialize config store", "error", err)
		os.Exit(1)
	}

	keyStore, keyStoreBackend, err := createKeyStoreFromEnv()
	if err != nil {
		logging.Logger.Error("failed to initialize API key store", "error", err)
		os.Exit(1)
	}
	logBootstrapConfigurationWarnings(keyStore)

	var corsOrigins []string
	if origins := os.Getenv("CORS_ORIGINS"); origins != "" {
		corsOrigins = strings.Split(origins, ",")
	}

	rlStore := newRateLimitStore()
	logReader, logMaintainer, logReaderBackend, err := createRequestLogReaderFromEnv()
	if err != nil {
		logging.Logger.Error("failed to initialize request log reader", "error", err)
		os.Exit(1)
	}

	r := newRouter(registry, keyStore, corsOrigins, gw, cfgManager, rlStore, logReader, logMaintainer)

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	srv := newHTTPServer(addr, r)

	// Graceful shutdown on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		logging.Logger.Info("shutting down gracefully")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logging.Logger.Error("shutdown error", "error", err)
		}
	}()

	logging.Logger.Info("ferrogw started",
		"version", version.Short(),
		"addr", addr,
		"providers", len(registry.List()),
		"config_store", configStoreBackend,
		"api_key_store", keyStoreBackend,
		"request_log_store", logReaderBackend,
	)
	serveErr := srv.ListenAndServe()
	if serveErr != nil && serveErr != http.ErrServerClosed {
		stop()
		logging.Logger.Error("server error", "error", serveErr)
	}

	if err := closeResources(
		namedResource{name: "gateway", value: gw},
		namedResource{name: "config manager", value: cfgManager},
		namedResource{name: "api key store", value: keyStore},
		namedResource{name: "request log store", value: logReader},
	); err != nil {
		logging.Logger.Error("shutdown cleanup error", "error", err)
	}

	if serveErr != nil && serveErr != http.ErrServerClosed {
		os.Exit(1) //nolint:gocritic
	}
	logging.Logger.Info("server stopped")
}

func logBootstrapConfigurationWarnings(keyStore admin.Store) {
	bootstrapAdminConfigured := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_KEY")) != ""
	bootstrapReadOnlyConfigured := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_READ_ONLY_KEY")) != ""
	if !bootstrapAdminConfigured && !bootstrapReadOnlyConfigured {
		return
	}

	bootstrapEnabled := true
	if raw := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_ENABLED")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			logging.Logger.Warn("invalid ADMIN_BOOTSTRAP_ENABLED value; expected true/false, defaulting to enabled", "value", raw)
		} else {
			bootstrapEnabled = parsed
		}
	}

	if !bootstrapEnabled {
		logging.Logger.Info("bootstrap keys configured but disabled by ADMIN_BOOTSTRAP_ENABLED=false")
		return
	}

	existingKeys := len(keyStore.List())
	if existingKeys > 0 {
		logging.Logger.Warn("bootstrap keys configured but ignored because API key store is not empty",
			"existing_keys", existingKeys,
			"bootstrap_admin_configured", bootstrapAdminConfigured,
			"bootstrap_read_only_configured", bootstrapReadOnlyConfigured,
		)
		return
	}

	logging.Logger.Warn("bootstrap keys enabled for first-run setup; create persistent API keys and then unset bootstrap env vars",
		"bootstrap_admin_configured", bootstrapAdminConfigured,
		"bootstrap_read_only_configured", bootstrapReadOnlyConfigured,
	)
}

// loadConfig loads and validates the gateway config from GATEWAY_CONFIG env var.
// Returns nil if GATEWAY_CONFIG is not set (caller uses default config).
func loadConfig() *aigateway.Config {
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

// registerProviders auto-registers all providers found via environment variables.
func registerProviders() *providers.Registry {
	registry := providers.NewRegistry()

	// Register all providers whose required environment variables are set.
	// Each ProviderEntry in providers.AllProviders() declares its own env var
	// mappings and Build function — no special-casing per provider needed here,
	// except for AWS Bedrock which uses a multi-key "configured?" gate.
	for _, entry := range providers.AllProviders() {
		if entry.ID == providers.NameBedrock {
			continue // handled below with its dual-key detection
		}

		cfg := providers.ProviderConfigFromEnv(entry)
		if cfg == nil {
			continue // required env var unset — provider not configured, skip silently
		}

		p, err := entry.Build(cfg)
		if err != nil {
			logging.Logger.Error("provider init failed", "provider", entry.ID, "error", err)
			os.Exit(1)
		}
		registry.Register(p)
		logging.Logger.Info("provider registered", "provider", entry.ID)
	}

	// AWS Bedrock: register if AWS_REGION or AWS_ACCESS_KEY_ID is set.
	// Accepts instance-role auth (no key vars) as long as AWS_REGION is set,
	// or explicit static credentials when AWS_ACCESS_KEY_ID is present.
	if region := os.Getenv("AWS_REGION"); region != "" || os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		p, err := bedrockpkg.NewWithOptions(bedrockpkg.Options{
			Region:          os.Getenv("AWS_REGION"),
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

	return registry
}

// buildGateway constructs the Gateway, wires providers, and loads plugins.
// If cfg is nil a default fallback config is created from the registry.
func buildGateway(cfg *aigateway.Config, registry *providers.Registry) *aigateway.Gateway {
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

// newRateLimitStore builds a per-IP token-bucket store from env vars.
// Returns nil if RATE_LIMIT_RPS is not set or is not a positive number.
func newRateLimitStore() *ratelimit.Store {
	rpsStr := os.Getenv("RATE_LIMIT_RPS")
	if rpsStr == "" {
		return nil
	}
	rps, err := strconv.ParseFloat(rpsStr, 64)
	if err != nil || rps <= 0 {
		return nil
	}
	var burst float64
	if burstStr := os.Getenv("RATE_LIMIT_BURST"); burstStr != "" {
		if v, err := strconv.ParseFloat(burstStr, 64); err == nil {
			burst = v
		}
	}
	store := ratelimit.NewStore(rps, burst)
	logging.Logger.Info("rate limiting enabled", "rps", rps, "burst", burst)
	return store
}
