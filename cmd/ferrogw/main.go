package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
	webassets "github.com/ferro-labs/ai-gateway/web"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	// Register built-in plugins so they can be loaded from config.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/budget"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

var webTemplates = template.Must(template.ParseFS(webassets.Assets, "*.html"))

const (
	backendMemory      = "memory"
	backendSQLite      = "sqlite"
	backendPostgres    = "postgres"
	backendPostgresSQL = "postgresql"
)

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
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

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
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		stop()
		logging.Logger.Error("server error", "error", err)
		os.Exit(1) //nolint:gocritic
	}
	logging.Logger.Info("server stopped")
}

func createKeyStoreFromEnv() (admin.Store, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("API_KEY_STORE_BACKEND")))
	if backend == "" {
		backend = backendMemory
	}

	storeDSN := strings.TrimSpace(os.Getenv("API_KEY_STORE_DSN"))

	switch backend {
	case backendMemory, "in-memory", "inmemory":
		return admin.NewKeyStore(), backendMemory, nil
	case backendSQLite:
		store, err := admin.NewSQLiteStore(storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, backendSQLite, nil
	case backendPostgres, backendPostgresSQL:
		store, err := admin.NewPostgresStore(storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, backendPostgres, nil
	default:
		return nil, "", fmt.Errorf("unsupported API key store backend %q", backend)
	}
}

func createRequestLogReaderFromEnv() (requestlog.Reader, requestlog.Maintainer, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_BACKEND")))
	if backend == "" {
		return nil, nil, "disabled", nil
	}

	dsn := strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_DSN"))

	switch backend {
	case backendSQLite:
		reader, err := requestlog.NewSQLiteWriter(dsn)
		if err != nil {
			return nil, nil, "", err
		}
		return reader, reader, backendSQLite, nil
	case backendPostgres, backendPostgresSQL:
		reader, err := requestlog.NewPostgresWriter(dsn)
		if err != nil {
			return nil, nil, "", err
		}
		return reader, reader, backendPostgres, nil
	default:
		return nil, nil, "", fmt.Errorf("unsupported request log store backend %q", backend)
	}
}

func createConfigManagerFromEnv(gw *aigateway.Gateway) (admin.ConfigManager, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("CONFIG_STORE_BACKEND")))
	if backend == "" {
		backend = backendMemory
	}

	dsn := strings.TrimSpace(os.Getenv("CONFIG_STORE_DSN"))

	switch backend {
	case backendMemory, "in-memory", "inmemory":
		manager, err := admin.NewGatewayConfigManager(gw, nil)
		if err != nil {
			return nil, "", err
		}
		return manager, backendMemory, nil
	case backendSQLite:
		store, err := admin.NewSQLiteConfigStore(dsn)
		if err != nil {
			return nil, "", err
		}
		manager, err := admin.NewGatewayConfigManager(gw, store)
		if err != nil {
			return nil, "", err
		}
		return manager, backendSQLite, nil
	case backendPostgres, backendPostgresSQL:
		store, err := admin.NewPostgresConfigStore(dsn)
		if err != nil {
			return nil, "", err
		}
		manager, err := admin.NewGatewayConfigManager(gw, store)
		if err != nil {
			return nil, "", err
		}
		return manager, backendPostgres, nil
	default:
		return nil, "", fmt.Errorf("unsupported config store backend %q", backend)
	}
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

// newRouter builds the HTTP router.
func newRouter(
	registry *providers.Registry,
	keyStore admin.Store,
	corsOrigins []string,
	gw *aigateway.Gateway,
	cfgManager admin.ConfigManager,
	rlStore *ratelimit.Store,
	logReader requestlog.Reader,
	logMaintainer requestlog.Maintainer,
) http.Handler {
	if gw == nil {
		defaultTargets := make([]aigateway.Target, 0, len(registry.List()))
		for _, name := range registry.List() {
			defaultTargets = append(defaultTargets, aigateway.Target{VirtualKey: name})
		}
		cfg := aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
			Targets:  defaultTargets,
		}
		created, err := aigateway.New(cfg)
		if err == nil {
			for _, name := range registry.List() {
				if p, ok := registry.Get(name); ok {
					created.RegisterProvider(p)
				}
			}
			gw = created
		}
	}

	r := chi.NewRouter()

	// Core middleware stack.
	r.Use(logging.Middleware) // inject trace ID + X-Request-ID header
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(corsMiddleware(corsOrigins...))

	// Optional per-IP rate limiting middleware.
	if rlStore != nil {
		r.Use(rateLimitMiddleware(rlStore))
	}

	// Health check — deep: lists registered providers and their model counts.
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		type providerHealth struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Models int    `json:"models"`
		}
		var providerStatuses []providerHealth
		for _, name := range gw.ListProviders() {
			p, ok := gw.GetProvider(name)
			if !ok {
				continue
			}
			providerStatuses = append(providerStatuses, providerHealth{
				Name:   name,
				Status: "available",
				Models: len(p.Models()),
			})
		}
		if providerStatuses == nil {
			providerStatuses = []providerHealth{}
		}
		status := "ok"
		if len(providerStatuses) == 0 {
			status = "no_providers"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    status,
			"providers": providerStatuses,
		})
	})

	// Prometheus metrics endpoint.
	r.Handle("/metrics", promhttp.Handler())
	mountPprofRoutes(r)

	// Minimal built-in admin dashboard UI.
	r.Get("/dashboard", func(w http.ResponseWriter, _ *http.Request) {
		if err := renderWebTemplate(w, "dashboard.html", nil); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to render dashboard", "server_error", "internal_error")
			return
		}
	})
	r.Get("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		data, err := fs.ReadFile(webassets.Assets, "logo.png")
		if err != nil {
			writeOpenAIError(w, http.StatusNotFound, "logo not found", "not_found_error", "resource_not_found")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})

	r.Get("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		catalog := gw.Catalog()
		raw := gw.AllModels()
		enriched := make([]EnrichedModelInfo, 0, len(raw))
		for _, m := range raw {
			enriched = append(enriched, enrichFromCatalog(catalog, m.OwnedBy, m.ID))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data":   enriched,
		})
	})

	// Admin routes — pass the gateway as the ProviderSource.
	adminHandlers := &admin.Handlers{
		Keys:      keyStore,
		Providers: gw,
		Configs:   cfgManager,
		Logs:      logReader,
		LogAdmin:  logMaintainer,
	}
	r.Route("/admin", func(r chi.Router) {
		r.Use(admin.AuthMiddleware(keyStore))
		r.Mount("/", adminHandlers.Routes())
	})

	r.Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeChatCompletionRequest(r.Body)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request")
			return
		}
		if err := req.Validate(); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request")
			return
		}

		// --- Streaming path ---
		if req.Stream {
			if _, ok := gw.FindByModel(req.Model); !ok {
				writeOpenAIError(w, http.StatusBadRequest, "no provider supports model: "+req.Model, "invalid_request_error", "model_not_found")
				return
			}
			if _, ok := gw.FindStreamingByModel(req.Model); !ok {
				writeOpenAIError(w, http.StatusBadRequest, "provider does not support streaming", "invalid_request_error", "streaming_not_supported")
				return
			}

			ch, err := gw.RouteStream(r.Context(), req)
			if err != nil {
				status, errType, code := routeErrorDetails(err)
				writeOpenAIError(w, status, err.Error(), errType, code)
				return
			}
			writeSSE(w, ch)
			return
		}

		// --- Non-streaming path ---
		if _, ok := gw.FindByModel(req.Model); !ok {
			writeOpenAIError(w, http.StatusBadRequest, "no provider supports model: "+req.Model, "invalid_request_error", "model_not_found")
			return
		}

		resp, err := gw.Route(r.Context(), req)
		if err != nil {
			status, errType, code := routeErrorDetails(err)
			writeOpenAIError(w, status, err.Error(), errType, code)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Legacy text completions.
	r.Post("/v1/completions", completionsHandler(registry))

	// Embeddings endpoint.
	r.Post("/v1/embeddings", embeddingsHandler(gw))

	// Image generation endpoint.
	r.Post("/v1/images/generations", imagesHandler(gw))

	// Proxy pass-through for unhandled /v1/* endpoints.
	r.HandleFunc("/v1/*", proxyHandler(registry))

	return r
}

type routeChatCompletionRequest struct {
	Model               string                    `json:"model"`
	Messages            []routeChatMessage        `json:"messages"`
	Temperature         *float64                  `json:"temperature,omitempty"`
	TopP                *float64                  `json:"top_p,omitempty"`
	N                   *int                      `json:"n,omitempty"`
	Seed                *int64                    `json:"seed,omitempty"`
	MaxTokens           *int                      `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                      `json:"max_completion_tokens,omitempty"`
	PresencePenalty     *float64                  `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64                  `json:"frequency_penalty,omitempty"`
	Stop                []string                  `json:"stop,omitempty"`
	Tools               []providers.Tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage           `json:"tool_choice,omitempty"`
	ResponseFormat      *providers.ResponseFormat `json:"response_format,omitempty"`
	LogProbs            bool                      `json:"logprobs,omitempty"`
	TopLogProbs         *int                      `json:"top_logprobs,omitempty"`
	Stream              bool                      `json:"stream,omitempty"`
	User                string                    `json:"user,omitempty"`
	LogitBias           map[string]float64        `json:"logit_bias,omitempty"`
}

type routeChatMessage struct {
	Role       string               `json:"role"`
	Content    json.RawMessage      `json:"content"`
	Name       string               `json:"name,omitempty"`
	ToolCalls  []providers.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

func decodeChatCompletionRequest(r io.Reader) (providers.Request, error) {
	var wire routeChatCompletionRequest
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return providers.Request{}, err
	}

	messages := make([]providers.Message, len(wire.Messages))
	for i, msg := range wire.Messages {
		decoded, err := msg.toProviderMessage()
		if err != nil {
			return providers.Request{}, fmt.Errorf("messages[%d]: %w", i, err)
		}
		messages[i] = decoded
	}

	var toolChoice interface{}
	if len(wire.ToolChoice) > 0 && !rawJSONNull(wire.ToolChoice) {
		if err := json.Unmarshal(wire.ToolChoice, &toolChoice); err != nil {
			return providers.Request{}, fmt.Errorf("tool_choice: %w", err)
		}
	}

	return providers.Request{
		Model:               wire.Model,
		Messages:            messages,
		Temperature:         wire.Temperature,
		TopP:                wire.TopP,
		N:                   wire.N,
		Seed:                wire.Seed,
		MaxTokens:           wire.MaxTokens,
		MaxCompletionTokens: wire.MaxCompletionTokens,
		PresencePenalty:     wire.PresencePenalty,
		FrequencyPenalty:    wire.FrequencyPenalty,
		Stop:                wire.Stop,
		Tools:               wire.Tools,
		ToolChoice:          toolChoice,
		ResponseFormat:      wire.ResponseFormat,
		LogProbs:            wire.LogProbs,
		TopLogProbs:         wire.TopLogProbs,
		Stream:              wire.Stream,
		User:                wire.User,
		LogitBias:           wire.LogitBias,
	}, nil
}

func (m routeChatMessage) toProviderMessage() (providers.Message, error) {
	msg := providers.Message{
		Role:       m.Role,
		Name:       m.Name,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}
	if len(m.Content) == 0 || rawJSONNull(m.Content) {
		return msg, nil
	}

	if m.Content[0] == '"' {
		if err := json.Unmarshal(m.Content, &msg.Content); err != nil {
			return providers.Message{}, err
		}
		return msg, nil
	}

	var parts []providers.ContentPart
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return providers.Message{}, err
	}
	msg.ContentParts = parts
	for _, part := range parts {
		if part.Type == providers.ContentTypeText {
			msg.Content += part.Text
		}
	}
	return msg, nil
}

func rawJSONNull(raw []byte) bool {
	return len(raw) == 4 && raw[0] == 'n' && raw[1] == 'u' && raw[2] == 'l' && raw[3] == 'l'
}

func renderWebTemplate(w http.ResponseWriter, templateName string, data interface{}) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return webTemplates.ExecuteTemplate(w, templateName, data)
}

func mountPprofRoutes(r chi.Router) {
	if !pprofEnabled() {
		return
	}

	r.Route("/debug/pprof", func(r chi.Router) {
		r.Get("/", httppprof.Index)
		r.Get("/cmdline", httppprof.Cmdline)
		r.Get("/profile", httppprof.Profile)
		r.Post("/symbol", httppprof.Symbol)
		r.Get("/symbol", httppprof.Symbol)
		r.Get("/trace", httppprof.Trace)
		r.Get("/allocs", httppprof.Handler("allocs").ServeHTTP)
		r.Get("/block", httppprof.Handler("block").ServeHTTP)
		r.Get("/goroutine", httppprof.Handler("goroutine").ServeHTTP)
		r.Get("/heap", httppprof.Handler("heap").ServeHTTP)
		r.Get("/mutex", httppprof.Handler("mutex").ServeHTTP)
		r.Get("/threadcreate", httppprof.Handler("threadcreate").ServeHTTP)
	})
}

func pprofEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("ENABLE_PPROF")))
	return v == "1" || v == "true" || v == "yes"
}

// rateLimitMiddleware rejects requests that exceed the per-IP token-bucket limit.
func rateLimitMiddleware(store *ratelimit.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := r.RemoteAddr
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.SplitN(xff, ",", 2)
				ip = strings.TrimSpace(parts[0])
			}
			if !store.Allow(ip) {
				metrics.RateLimitRejections.WithLabelValues("ip").Inc()
				writeOpenAIError(w, http.StatusTooManyRequests,
					"rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeOpenAIError writes a unified OpenAI-compatible JSON error response.
func writeOpenAIError(w http.ResponseWriter, status int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
}

func routeErrorDetails(err error) (status int, errType, code string) {
	status = http.StatusInternalServerError
	errType = "server_error"
	code = "routing_error"

	var rejection *plugin.RejectionError
	if errors.As(err, &rejection) {
		switch rejection.Stage {
		case plugin.StageBeforeRequest:
			// Rate-limit and budget plugins signal throttling — return 429.
			if rejection.PluginType == plugin.TypeRateLimit {
				return http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded"
			}
			return http.StatusBadRequest, "invalid_request_error", "request_rejected"
		case plugin.StageAfterRequest:
			return http.StatusBadGateway, "upstream_error", "response_rejected"
		default:
			return http.StatusInternalServerError, "server_error", "request_rejected"
		}
	}

	return status, errType, code
}

// writeSSE streams SSE chunks from ch to the response writer.
func writeSSE(w http.ResponseWriter, ch <-chan providers.StreamChunk) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 4096)
	enc := json.NewEncoder(bw)
	now := time.Now().Unix()
	for chunk := range ch {
		if chunk.Error != nil {
			_ = writeSSEEvent(bw, enc, map[string]any{
				"error": map[string]string{
					"message": chunk.Error.Error(),
					"type":    "stream_error",
					"code":    "stream_error",
				},
			})
			_ = bw.Flush()
			flushSSE(flusher)
			return
		}
		if chunk.Object == "" {
			chunk.Object = "chat.completion.chunk"
		}
		if chunk.Created == 0 {
			chunk.Created = now
		}
		_ = writeSSEEvent(bw, enc, chunk)
		_ = bw.Flush()
		flushSSE(flusher)
	}
	_, _ = bw.WriteString("data: [DONE]\n\n")
	_ = bw.Flush()
	flushSSE(flusher)
}

func writeSSEEvent(bw *bufio.Writer, enc *json.Encoder, payload any) error {
	if _, err := bw.WriteString("data: "); err != nil {
		return err
	}
	if err := enc.Encode(payload); err != nil {
		return err
	}
	if err := bw.WriteByte('\n'); err != nil {
		return err
	}
	return nil
}

func flushSSE(flusher http.Flusher) {
	if flusher != nil {
		flusher.Flush()
	}
}
