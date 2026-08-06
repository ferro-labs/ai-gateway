// Package middleware provides HTTP middleware for the gateway server.
package middleware

import (
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// CORS returns middleware that sets CORS headers for the given allowed origins.
//
// A cross-origin request is denied by withholding Access-Control-Allow-Origin,
// never by refusing the preflight. Those are not the same denial. The header is
// what a browser actually enforces; the status is not, and a preflight answered
// with an error teaches the caller the wrong thing about why it failed.
//
// So an OPTIONS request carrying an Origin — the shape only a preflight has — is
// answered here, before authentication and before the per-route method guard,
// whether or not the origin is allowed. An allowed origin gets 204 with the
// Access-Control-* headers; any other origin gets the same 204 with none of
// them, and the browser blocks the real request that would have followed.
//
// It is answered here because neither layer below can answer it correctly. A
// preflight carries no credentials — browsers do not send Authorization on one —
// so authentication rejects it as 401, and the route's method guard, which
// serves POST, rejects it as 405 with "Allow: POST". Both describe the wrong
// thing: the resource does support OPTIONS, and demonstrably answers it 204 the
// moment the caller's origin is listed. Whether a preflight succeeds turns on
// the request's Origin, which is not a property of the resource and so is not
// something Allow can express.
//
// An OPTIONS request with no Origin is not a preflight and is left alone: that
// one really is a question about the resource's methods, and the route answers
// it.
//
// When no origins are configured no cross-origin request is ever allowed. That
// is the correct state for the default deployment, where the dashboard is served
// from this same origin. Set CORS_ORIGINS to an explicit comma-separated
// allowlist only for browser applications of your own that call this gateway
// from another origin.
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
		// Info, not Warn: the dashboard is served from this origin, so the
		// default deployment needs no allowlist and nothing here is wrong. A
		// warning on every correct start only teaches operators to skip them.
		logger.Default().Info("CORS_ORIGINS is not set; no cross-origin allowlist is active. The dashboard is served from this origin and needs none — set CORS_ORIGINS only for browser applications of your own calling this gateway from another origin.")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestOrigin := r.Header.Get("Origin")
			// The response depends on Origin whether or not this one is
			// allowed, so a shared cache has to key on it either way.
			w.Header().Set("Vary", "Origin")

			// An empty Origin never matches: the allowlist is built above with
			// blank entries skipped.
			if _, ok := allowed[requestOrigin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", requestOrigin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Provider")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}

			if r.Method == http.MethodOptions && requestOrigin != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
