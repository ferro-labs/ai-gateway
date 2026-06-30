// Package middleware provides HTTP middleware for the gateway server.
package middleware

import (
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/logging"
)

// CORS returns middleware that sets CORS headers for the given allowed origins.
//
// When no origins are configured the middleware is a no-op: it emits no
// Access-Control-Allow-Origin header and passes every request (including
// OPTIONS preflights) straight through to the next handler. Cross-origin
// requests are therefore blocked by the browser. Set CORS_ORIGINS to an
// explicit comma-separated allowlist to enable cross-origin access.
func CORS(allowedOrigins ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, value := range allowedOrigins {
		origin := strings.TrimSpace(value)
		if origin == "" {
			continue
		}
		allowed[origin] = struct{}{}
	}

	if len(allowed) == 0 {
		logging.Logger.Warn("CORS_ORIGINS is not configured — all cross-origin requests will be blocked. Set CORS_ORIGINS to allow specific origins (e.g. your dashboard).")
		// Return a pass-through middleware: no CORS headers are emitted.
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r)
			})
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestOrigin := r.Header.Get("Origin")
			if _, ok := allowed[requestOrigin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", requestOrigin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Provider")
				w.Header().Set("Access-Control-Max-Age", "86400")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
