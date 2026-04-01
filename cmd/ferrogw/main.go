package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/cli"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/providers"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
	"github.com/spf13/cobra"

	// Register built-in plugins so they can be loaded from config.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/budget"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

var rootCmd = &cobra.Command{
	Use:   "ferrogw",
	Short: "Ferro Labs AI Gateway",
	Long:  "High-performance AI gateway with smart routing, plugins, and admin dashboard.",
	// Default: start the server (backward compatible).
	Run: func(_ *cobra.Command, _ []string) {
		runServe()
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the gateway server",
	Run: func(_ *cobra.Command, _ []string) {
		runServe()
	},
}

func main() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(cli.InitCmd)
	rootCmd.AddCommand(cli.ValidateCmd)
	rootCmd.AddCommand(cli.PluginsCmd)
	rootCmd.AddCommand(cli.DoctorCmd)
	rootCmd.AddCommand(cli.StatusCmd)
	rootCmd.AddCommand(cli.VersionCmd)
	rootCmd.AddCommand(cli.AdminCmd)

	// Persistent flags for CLI commands.
	rootCmd.PersistentFlags().String("gateway-url", "",
		"Gateway base URL (env: FERROGW_URL, default: http://localhost:8080)")
	rootCmd.PersistentFlags().String("api-key", "",
		"Admin API key (env: FERROGW_API_KEY)")
	rootCmd.PersistentFlags().String("format", "table",
		"Output format: table, json, or yaml")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// resolveMasterKey returns the master key from the MASTER_KEY env var.
func resolveMasterKey() string {
	return strings.TrimSpace(os.Getenv("MASTER_KEY"))
}

func runServe() {
	logging.Setup(os.Getenv("LOG_LEVEL"), os.Getenv("LOG_FORMAT"))

	cfg := loadConfig()
	registry := registerProviders()
	masterKey := resolveMasterKey()

	if strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_UNAUTHENTICATED_PROXY")), "true") {
		logging.Logger.Warn("ALLOW_UNAUTHENTICATED_PROXY is set -- proxy routes are unauthenticated (not recommended for production)")
	}

	if len(registry.List()) == 0 {
		logging.Logger.Warn("no providers configured; set provider API keys (e.g. OPENAI_API_KEY) or OLLAMA_HOST, or add them later via the admin API")
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
	logDeprecatedBootstrapKeys()

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

	r := newRouter(registry, keyStore, corsOrigins, gw, cfgManager, rlStore, logReader, logMaintainer, masterKey)

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

	printStartupBanner(addr, registry, cfg, masterKey, keyStoreBackend, configStoreBackend)

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

func logDeprecatedBootstrapKeys() {
	if strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_KEY")) != "" {
		logging.Logger.Warn("ADMIN_BOOTSTRAP_KEY is deprecated -- use MASTER_KEY instead")
	}
	if strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_READ_ONLY_KEY")) != "" {
		logging.Logger.Warn("ADMIN_BOOTSTRAP_READ_ONLY_KEY is deprecated -- use MASTER_KEY instead")
	}
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

// printStartupBanner prints a branded, informative banner to stderr on server start.
func printStartupBanner(addr string, registry *providers.Registry, cfg *aigateway.Config, masterKey, keyStoreBackend, configStoreBackend string) {
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
	if keyStoreBackend == backendMemory {
		if !hasWarnings {
			fmt.Fprintf(os.Stderr, "  Warnings\n")
			hasWarnings = true
		}
		fmt.Fprintf(os.Stderr, "    %s[!] API key store: in-memory (keys lost on restart)%s\n", yellow, reset)
	}
	if configStoreBackend == backendMemory {
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
