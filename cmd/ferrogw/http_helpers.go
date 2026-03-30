package main

import (
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/plugin"
	webassets "github.com/ferro-labs/ai-gateway/web"
	"github.com/go-chi/chi/v5"
)

func renderWebTemplate(w http.ResponseWriter, pageName string, data interface{}) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl, err := template.ParseFS(webassets.Assets, "templates/layout.html", "templates/pages/"+pageName+".html")
	if err != nil {
		return err
	}
	return tmpl.ExecuteTemplate(w, "layout.html", data)
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

func serveLogo(w http.ResponseWriter) {
	data, err := fs.ReadFile(webassets.Assets, "logo.png")
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "logo not found", "not_found_error", "resource_not_found")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}
