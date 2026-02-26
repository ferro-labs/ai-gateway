package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	// Register built-in plugins so they can be loaded from config.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

func main() {
	// Load and validate config if GATEWAY_CONFIG is set.
	var cfg *aigateway.Config
	if cfgPath := os.Getenv("GATEWAY_CONFIG"); cfgPath != "" {
		loaded, err := aigateway.LoadConfig(cfgPath)
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
		if err := aigateway.ValidateConfig(*loaded); err != nil {
			log.Fatalf("Invalid config: %v", err)
		}
		cfg = loaded
		log.Printf("Config loaded: strategy=%s, targets=%d", cfg.Strategy.Mode, len(cfg.Targets))
	}

	// Auto-register providers based on environment variables.
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
	}
	for _, pe := range autoProviders {
		if key := os.Getenv(pe.envKey); key != "" {
			p, err := pe.create(key, "")
			if err != nil {
				log.Fatalf("%s provider: %v", pe.name, err)
			}
			registry.Register(p)
			log.Printf("Provider registered: %s", pe.name)
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
				log.Fatalf("Azure OpenAI provider: %v", err)
			}
			registry.Register(p)
			log.Println("Provider registered: azure-openai")
		} else {
			log.Println("Warning: AZURE_OPENAI_API_KEY set but AZURE_OPENAI_ENDPOINT and AZURE_OPENAI_DEPLOYMENT are required")
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
			log.Fatalf("Ollama provider: %v", err)
		}
		registry.Register(p)
		log.Printf("Provider registered: ollama (models: %s)", strings.Join(p.SupportedModels(), ", "))
	}

	if len(registry.List()) == 0 {
		log.Fatal("No providers configured. Set at least one provider API key (e.g., OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY) or OLLAMA_HOST for local models")
	}

	if cfg == nil {
		defaultTargets := make([]aigateway.Target, 0, len(registry.List()))
		for _, name := range registry.List() {
			defaultTargets = append(defaultTargets, aigateway.Target{VirtualKey: name})
		}
		cfg = &aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
			Targets:  defaultTargets,
		}
		log.Printf("No GATEWAY_CONFIG set; using default strategy=%s with %d target(s)", cfg.Strategy.Mode, len(cfg.Targets))
	}

	// Build and wire the Gateway.
	var gw *aigateway.Gateway
	var err error
	gw, err = aigateway.New(*cfg)
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}
	// Register all env-var providers on the Gateway so strategies can route to them.
	for _, name := range registry.List() {
		if p, ok := registry.Get(name); ok {
			gw.RegisterProvider(p)
		}
	}
	if len(cfg.Plugins) > 0 {
		if err := gw.LoadPlugins(); err != nil {
			log.Fatalf("Failed to load plugins: %v", err)
		}
		log.Printf("Gateway ready: %d plugin(s) loaded", len(cfg.Plugins))
	}

	keyStore := admin.NewKeyStore()

	var corsOrigins []string
	if origins := os.Getenv("CORS_ORIGINS"); origins != "" {
		corsOrigins = strings.Split(origins, ",")
	}

	r := newRouter(registry, keyStore, corsOrigins, gw)

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
		log.Println("Shutting down gracefully…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("Shutdown error: %v", err)
		}
	}()

	log.Printf("FerroGateway %s listening on %s (%d provider(s))", version.Short(), addr, len(registry.List()))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		stop()
		log.Fatalf("Server error: %v", err) //nolint:gocritic
	}
	log.Println("Server stopped.")
}

// newRouter builds the HTTP router.
func newRouter(registry *providers.Registry, keyStore admin.Store, corsOrigins []string, gw *aigateway.Gateway) http.Handler {
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
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(corsMiddleware(corsOrigins...))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	r.Get("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data":   registry.AllModels(),
		})
	})

	adminHandlers := &admin.Handlers{
		Keys:     keyStore,
		Registry: registry,
	}
	r.Route("/admin", func(r chi.Router) {
		r.Use(admin.AuthMiddleware(keyStore))
		r.Mount("/", adminHandlers.Routes())
	})

	r.Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req providers.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		if err := req.Validate(); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}

		// --- Streaming path ---
		if req.Stream {
			if !hasModelProvider(registry, req.Model) {
				writeOpenAIError(w, http.StatusBadRequest, "no provider supports model: "+req.Model, "invalid_request_error")
				return
			}
			if !hasStreamingProviderForModel(registry, req.Model) {
				writeOpenAIError(w, http.StatusBadRequest, "provider does not support streaming", "invalid_request_error")
				return
			}

			ch, err := gw.RouteStream(r.Context(), req)
			if err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
				return
			}
			writeSSE(w, ch)
			return
		}

		// --- Non-streaming path ---
		if !hasModelProvider(registry, req.Model) {
			writeOpenAIError(w, http.StatusBadRequest, "no provider supports model: "+req.Model, "invalid_request_error")
			return
		}

		resp, err := gw.Route(r.Context(), req)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Legacy text completions (e.g. gpt-3.5-turbo-instruct, deepseek-chat).
	// Proxies natively to providers that support it, or shims via chat for others.
	r.Post("/v1/completions", completionsHandler(registry))

	// Proxy pass-through: forward any unhandled /v1/* request to the upstream
	// provider.  This covers files, batches, fine-tuning, audio, images/edits,
	// responses API, realtime, etc. without needing a dedicated handler.
	// Must be registered LAST so explicit routes take precedence.
	r.HandleFunc("/v1/*", proxyHandler(registry))

	return r
}

// writeOpenAIError writes an OpenAI-compatible JSON error response.
func writeOpenAIError(w http.ResponseWriter, status int, message, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errType,
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
			errData := fmt.Sprintf(`{"error":{"message":"%s","type":"stream_error"}}`, chunk.Error.Error())
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

func hasModelProvider(registry *providers.Registry, model string) bool {
	_, ok := registry.FindByModel(model)
	return ok
}

func hasStreamingProviderForModel(registry *providers.Registry, model string) bool {
	for _, name := range registry.List() {
		p, ok := registry.Get(name)
		if !ok || !p.SupportsModel(model) {
			continue
		}
		if _, ok := p.(providers.StreamProvider); ok {
			return true
		}
	}
	return false
}
