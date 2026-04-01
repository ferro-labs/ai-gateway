package main

import (
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

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
	masterKey string,
) http.Handler {
	gw = ensureRouterGateway(gw, registry)

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

	mountOperationalRoutes(r, gw, keyStore, masterKey)
	mountDashboardRoutes(r)
	mountAdminRoutes(r, gw, keyStore, cfgManager, logReader, logMaintainer, masterKey)
	mountOpenAIRoutes(r, gw, registry, keyStore, masterKey)

	return r
}

func ensureRouterGateway(gw *aigateway.Gateway, registry *providers.Registry) *aigateway.Gateway {
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
