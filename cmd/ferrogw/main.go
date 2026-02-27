package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	// Register built-in plugins so they can be loaded from config.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
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

	keyStore, keyStoreBackend, err := createKeyStoreFromEnv()
	if err != nil {
		logging.Logger.Error("failed to initialize API key store", "error", err)
		os.Exit(1)
	}

	var corsOrigins []string
	if origins := os.Getenv("CORS_ORIGINS"); origins != "" {
		corsOrigins = strings.Split(origins, ",")
	}

	rlStore := newRateLimitStore()
	logReader, logReaderBackend, err := createRequestLogReaderFromEnv()
	if err != nil {
		logging.Logger.Error("failed to initialize request log reader", "error", err)
		os.Exit(1)
	}

	r := newRouter(registry, keyStore, corsOrigins, gw, rlStore, logReader)

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
		backend = "memory"
	}

	storeDSN := strings.TrimSpace(os.Getenv("API_KEY_STORE_DSN"))

	switch backend {
	case "memory", "in-memory", "inmemory":
		return admin.NewKeyStore(), "memory", nil
	case "sqlite":
		store, err := admin.NewSQLiteStore(storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, "sqlite", nil
	case "postgres", "postgresql":
		store, err := admin.NewPostgresStore(storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, "postgres", nil
	default:
		return nil, "", fmt.Errorf("unsupported API key store backend %q", backend)
	}
}

func createRequestLogReaderFromEnv() (requestlog.Reader, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_BACKEND")))
	if backend == "" {
		return nil, "disabled", nil
	}

	dsn := strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_DSN"))

	switch backend {
	case "sqlite":
		reader, err := requestlog.NewSQLiteWriter(dsn)
		if err != nil {
			return nil, "", err
		}
		return reader, "sqlite", nil
	case "postgres", "postgresql":
		reader, err := requestlog.NewPostgresWriter(dsn)
		if err != nil {
			return nil, "", err
		}
		return reader, "postgres", nil
	default:
		return nil, "", fmt.Errorf("unsupported request log store backend %q", backend)
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

	type providerEntry struct {
		envKey string
		name   string
		create func(key, baseURL string) (providers.Provider, error)
	}
	autoProviders := []providerEntry{
		{"OPENAI_API_KEY", "openai", func(k, b string) (providers.Provider, error) { return providers.NewOpenAI(k, b) }},
		{"ANTHROPIC_API_KEY", "anthropic", func(k, b string) (providers.Provider, error) { return providers.NewAnthropic(k, b) }},
		{"GROQ_API_KEY", "groq", func(k, b string) (providers.Provider, error) { return providers.NewGroq(k, b) }},
		{"TOGETHER_API_KEY", "together", func(k, b string) (providers.Provider, error) { return providers.NewTogether(k, b) }},
		{"GEMINI_API_KEY", "gemini", func(k, b string) (providers.Provider, error) { return providers.NewGemini(k, b) }},
		{"MISTRAL_API_KEY", "mistral", func(k, b string) (providers.Provider, error) { return providers.NewMistral(k, b) }},
		{"COHERE_API_KEY", "cohere", func(k, b string) (providers.Provider, error) { return providers.NewCohere(k, b) }},
		{"DEEPSEEK_API_KEY", "deepseek", func(k, b string) (providers.Provider, error) { return providers.NewDeepSeek(k, b) }},
		{"PERPLEXITY_API_KEY", "perplexity", func(k, b string) (providers.Provider, error) { return providers.NewPerplexity(k, b) }},
		{"FIREWORKS_API_KEY", "fireworks", func(k, b string) (providers.Provider, error) { return providers.NewFireworks(k, b) }},
		{"AI21_API_KEY", "ai21", func(k, b string) (providers.Provider, error) { return providers.NewAI21(k, b) }},
	}
	for _, pe := range autoProviders {
		if key := os.Getenv(pe.envKey); key != "" {
			p, err := pe.create(key, "")
			if err != nil {
				logging.Logger.Error("provider init failed", "provider", pe.name, "error", err)
				os.Exit(1)
			}
			registry.Register(p)
			logging.Logger.Info("provider registered", "provider", pe.name)
		}
	}

	// Azure OpenAI requires additional config.
	if key := os.Getenv("AZURE_OPENAI_API_KEY"); key != "" {
		baseURL := os.Getenv("AZURE_OPENAI_ENDPOINT")
		deployment := os.Getenv("AZURE_OPENAI_DEPLOYMENT")
		apiVersion := os.Getenv("AZURE_OPENAI_API_VERSION")
		if baseURL != "" && deployment != "" {
			p, err := providers.NewAzureOpenAI(key, baseURL, deployment, apiVersion)
			if err != nil {
				logging.Logger.Error("provider init failed", "provider", "azure-openai", "error", err)
				os.Exit(1)
			}
			registry.Register(p)
			logging.Logger.Info("provider registered", "provider", "azure-openai")
		} else {
			logging.Logger.Warn("AZURE_OPENAI_API_KEY set but AZURE_OPENAI_ENDPOINT and AZURE_OPENAI_DEPLOYMENT are required")
		}
	}

	// Ollama is local and needs no API key.
	if ollamaURL := os.Getenv("OLLAMA_HOST"); ollamaURL != "" {
		var models []string
		if m := os.Getenv("OLLAMA_MODELS"); m != "" {
			models = strings.Split(m, ",")
		}
		p, err := providers.NewOllama(ollamaURL, models)
		if err != nil {
			logging.Logger.Error("provider init failed", "provider", "ollama", "error", err)
			os.Exit(1)
		}
		registry.Register(p)
		logging.Logger.Info("provider registered", "provider", "ollama", "models", p.SupportedModels())
	}

	// Replicate requires an API token and optional model lists.
	if token := os.Getenv("REPLICATE_API_TOKEN"); token != "" {
		var textModels, imageModels []string
		if m := os.Getenv("REPLICATE_TEXT_MODELS"); m != "" {
			textModels = strings.Split(m, ",")
		}
		if m := os.Getenv("REPLICATE_IMAGE_MODELS"); m != "" {
			imageModels = strings.Split(m, ",")
		}
		p, err := providers.NewReplicate(token, "", textModels, imageModels)
		if err != nil {
			logging.Logger.Error("provider init failed", "provider", "replicate", "error", err)
			os.Exit(1)
		}
		registry.Register(p)
		logging.Logger.Info("provider registered", "provider", "replicate")
	}

	// AWS Bedrock uses the AWS credential chain (env vars, ~/.aws/credentials, IAM roles).
	if region := os.Getenv("AWS_REGION"); region != "" || os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		bedrockRegion := os.Getenv("AWS_REGION")
		p, err := providers.NewBedrock(bedrockRegion)
		if err != nil {
			logging.Logger.Error("provider init failed", "provider", "bedrock", "error", err)
		} else {
			registry.Register(p)
			logging.Logger.Info("provider registered", "provider", "bedrock", "region", bedrockRegion)
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
	rlStore *ratelimit.Store,
	logReader requestlog.Reader,
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

	r.Get("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data":   gw.AllModels(),
		})
	})

	// Admin routes — pass the gateway as the ProviderSource.
	adminHandlers := &admin.Handlers{
		Keys:      keyStore,
		Providers: gw,
		Configs:   gw,
		Logs:      logReader,
	}
	r.Route("/admin", func(r chi.Router) {
		r.Use(admin.AuthMiddleware(keyStore))
		r.Mount("/", adminHandlers.Routes())
	})

	r.Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req providers.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			if !hasStreamingProviderForModel(gw, req.Model) {
				writeOpenAIError(w, http.StatusBadRequest, "provider does not support streaming", "invalid_request_error", "streaming_not_supported")
				return
			}

			ch, err := gw.RouteStream(r.Context(), req)
			if err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "routing_error")
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
			writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "routing_error")
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

// writeSSE streams SSE chunks from ch to the response writer.
func writeSSE(w http.ResponseWriter, ch <-chan providers.StreamChunk) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	now := time.Now().Unix()
	for chunk := range ch {
		if chunk.Error != nil {
			errData := fmt.Sprintf(`{"error":{"message":%q,"type":"stream_error","code":"stream_error"}}`, chunk.Error.Error())
			_, _ = fmt.Fprintf(w, "data: %s\n\n", errData)
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if chunk.Object == "" {
			chunk.Object = "chat.completion.chunk"
		}
		if chunk.Created == 0 {
			chunk.Created = now
		}
		data, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func hasStreamingProviderForModel(src providers.ProviderSource, model string) bool {
	for _, name := range src.List() {
		p, ok := src.Get(name)
		if !ok || !p.SupportsModel(model) {
			continue
		}
		if _, ok := p.(providers.StreamProvider); ok {
			return true
		}
	}
	return false
}
