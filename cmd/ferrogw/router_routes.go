package main

import (
	"encoding/json"
	"expvar"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/providers"
	webassets "github.com/ferro-labs/ai-gateway/web"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func mountOperationalRoutes(r chi.Router, gw *aigateway.Gateway) {
	r.Get("/health", healthHandler(gw))
	r.Handle("/metrics", promhttp.Handler())
	r.Handle("/debug/vars", expvar.Handler())
	mountPprofRoutes(r)
}

type pageData struct {
	ActivePage string
	PageTitle  string
	Version    string
}

func renderPage(w http.ResponseWriter, page, title string) {
	data := pageData{ActivePage: page, PageTitle: title, Version: version.Short()}
	if err := renderWebTemplate(w, page, data); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to render dashboard", "server_error", "internal_error")
	}
}

func mountDashboardRoutes(r chi.Router) {
	r.Get("/dashboard", func(w http.ResponseWriter, _ *http.Request) {
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
		tmpl, err := template.ParseFS(webassets.Assets, "templates/login.html")
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to render login page", "server_error", "internal_error")
			return
		}
		_ = tmpl.Execute(w, nil)
	})

	// Serve static assets from embedded filesystem.
	staticFS, _ := fs.Sub(webassets.Assets, "static")
	r.Handle("/dashboard/static/*", http.StripPrefix("/dashboard/static/", http.FileServer(http.FS(staticFS))))

	r.Get("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		serveLogo(w)
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

// optionalProxyAuth returns a middleware that requires auth on proxy routes
// only when a master key is configured. When no master key is set, requests
// pass through without authentication (backward compatible).
func optionalProxyAuth(store admin.Store, masterKey string) func(http.Handler) http.Handler {
	if masterKey == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return admin.AuthMiddleware(store, masterKey)
}

func mountOpenAIRoutes(r chi.Router, gw *aigateway.Gateway, registry *providers.Registry, store admin.Store, masterKey string) {
	proxyAuth := optionalProxyAuth(store, masterKey)

	r.Group(func(r chi.Router) {
		r.Use(proxyAuth)
		r.Get("/v1/models", modelsHandler(gw))
		r.Post("/v1/chat/completions", chatCompletionsHandler(gw))

		// Legacy text completions.
		r.Post("/v1/completions", completionsHandler(registry))

		// Embeddings endpoint.
		r.Post("/v1/embeddings", embeddingsHandler(gw))

		// Image generation endpoint.
		r.Post("/v1/images/generations", imagesHandler(gw))

		// Proxy pass-through for unhandled /v1/* endpoints.
		r.HandleFunc("/v1/*", proxyHandler(registry))
	})
}

func modelsHandler(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
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
	}
}

func healthHandler(gw *aigateway.Gateway) http.HandlerFunc {
	type providerHealth struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Models int    `json:"models"`
	}

	return func(w http.ResponseWriter, _ *http.Request) {
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
	}
}

func chatCompletionsHandler(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
			writeSSE(r.Context(), w, ch)
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

		if resp.OverheadMs > 0 {
			w.Header().Set("X-Gateway-Overhead-Ms", fmt.Sprintf("%.3f", resp.OverheadMs))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
