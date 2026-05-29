package httpserver

import (
	"expvar"
	"html/template"
	"io/fs"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/dashboard"
	"github.com/ferro-labs/ai-gateway/internal/handler"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/middleware"
	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/internal/proxy"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/providers"
	webassets "github.com/ferro-labs/ai-gateway/web"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var loginTemplate = template.Must(template.ParseFS(webassets.Assets, "templates/login.html"))

// NewRouter builds the HTTP router for the gateway.
func NewRouter(
	registry *providers.Registry,
	keyStore admin.Store,
	corsOrigins []string,
	gw *aigateway.Gateway,
	cfgManager admin.ConfigManager,
	rlStore *ratelimit.Store,
	logReader requestlog.Reader,
	logMaintainer requestlog.Maintainer,
	masterKey string,
) http.Handler {
	gw = ensureGateway(gw, registry)

	r := chi.NewRouter()

	// Core middleware stack.
	// OTel middleware MUST come before logging.Middleware so any inbound
	// W3C traceparent is extracted into the request context, then the
	// logging layer reuses that trace ID for X-Request-ID. When no OTel
	// provider is configured this middleware is a cheap no-op (the
	// global propagator is the default no-op propagator).
	r.Use(gwotel.Middleware)
	r.Use(logging.Middleware) // inject trace ID + X-Request-ID header
	r.Use(chimw.Recoverer)
	r.Use(chimw.RealIP)
	r.Use(middleware.CORS(corsOrigins...))

	// Optional per-IP rate limiting middleware.
	if rlStore != nil {
		r.Use(middleware.RateLimit(rlStore))
	}

	mountOperationalRoutes(r, gw, keyStore, masterKey)
	mountDashboardRoutes(r)
	mountAdminRoutes(r, gw, keyStore, cfgManager, logReader, logMaintainer, masterKey)
	mountOpenAIRoutes(r, gw, registry, keyStore, masterKey)

	return r
}

// ensureGateway returns gw if non-nil; otherwise builds a default fallback
// gateway from the registry.
func ensureGateway(gw *aigateway.Gateway, registry *providers.Registry) *aigateway.Gateway {
	if gw != nil {
		return gw
	}

	defaultTargets := make([]aigateway.Target, 0, len(registry.List()))
	for _, name := range registry.List() {
		defaultTargets = append(defaultTargets, aigateway.Target{VirtualKey: name})
	}
	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  defaultTargets,
	}
	created, err := aigateway.New(cfg)
	if err != nil {
		return nil
	}
	for _, name := range registry.List() {
		if p, ok := registry.Get(name); ok {
			created.RegisterProvider(p)
		}
	}
	return created
}

func mountOperationalRoutes(r chi.Router, gw *aigateway.Gateway, store admin.Store, masterKey string) {
	r.Get("/health", handler.Health(gw))
	obsAuth := admin.AuthMiddleware(store, masterKey)
	r.Group(func(r chi.Router) {
		r.Use(obsAuth)
		r.Handle("/metrics", promhttp.Handler())
		r.Handle("/debug/vars", expvar.Handler())
		dashboard.MountPprofRoutes(r)
	})
}

type pageData struct {
	ActivePage string
	PageTitle  string
	Version    string
}

func renderPage(w http.ResponseWriter, page, title string) {
	data := pageData{ActivePage: page, PageTitle: title, Version: version.Short()}
	if err := dashboard.RenderWebTemplate(w, page, data); err != nil {
		apierror.WriteOpenAI(w, http.StatusInternalServerError, "failed to render dashboard", "server_error", "internal_error")
	}
}

func mountDashboardRoutes(r chi.Router) {
	r.Get("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/getting-started", http.StatusFound)
	})
	r.Get("/dashboard/getting-started", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "getting-started", "Getting Started")
	})
	r.Get("/dashboard/overview", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "overview", "Overview")
	})
	r.Get("/dashboard/keys", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "keys", "API Keys")
	})
	r.Get("/dashboard/logs", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "logs", "Request Logs")
	})
	r.Get("/dashboard/providers", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "providers", "Providers")
	})
	r.Get("/dashboard/config", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "config", "Config")
	})
	r.Get("/dashboard/analytics", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "analytics", "Analytics")
	})
	r.Get("/dashboard/playground", func(w http.ResponseWriter, _ *http.Request) {
		renderPage(w, "playground", "Playground")
	})

	r.Get("/dashboard/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginTemplate.Execute(w, nil)
	})

	// Serve static assets from embedded filesystem.
	staticFS, _ := fs.Sub(webassets.Assets, "static")
	r.Handle("/dashboard/static/*", http.StripPrefix("/dashboard/static/", http.FileServer(http.FS(staticFS))))

	r.Get("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		dashboard.ServeLogo(w)
	})
}

func mountAdminRoutes(
	r chi.Router,
	gw *aigateway.Gateway,
	keyStore admin.Store,
	cfgManager admin.ConfigManager,
	logReader requestlog.Reader,
	logMaintainer requestlog.Maintainer,
	masterKey string,
) {
	adminHandlers := &admin.Handlers{
		Keys:      keyStore,
		Providers: gw,
		Configs:   cfgManager,
		Logs:      logReader,
		LogAdmin:  logMaintainer,
	}
	r.Route("/admin", func(r chi.Router) {
		r.Use(admin.AuthMiddleware(keyStore, masterKey))
		r.Mount("/", adminHandlers.Routes())
	})
}

func mountOpenAIRoutes(r chi.Router, gw *aigateway.Gateway, registry *providers.Registry, store admin.Store, masterKey string) {
	auth := middleware.ProxyAuth(store, masterKey)

	r.Group(func(r chi.Router) {
		r.Use(auth)
		r.Get("/v1/models", handler.Models(gw))
		r.Post("/v1/chat/completions", handler.ChatCompletions(gw))

		// Legacy text completions.
		r.Post("/v1/completions", handler.Completions(registry))

		// Embeddings endpoint.
		r.Post("/v1/embeddings", handler.Embeddings(gw))

		// Image generation endpoint.
		r.Post("/v1/images/generations", handler.Images(gw))

		// Proxy pass-through for unhandled /v1/* endpoints.
		r.HandleFunc("/v1/*", proxy.Handler(registry))
	})
}
