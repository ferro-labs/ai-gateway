package main

import (
	"net/http"
	"strings"
)

// corsMiddleware returns middleware that sets CORS headers.
// If no origins are provided, it defaults to "*".
func corsMiddleware(allowedOrigins ...string) func(http.Handler) http.Handler {
	allowAny := len(allowedOrigins) == 0
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, value := range allowedOrigins {
		origin := strings.TrimSpace(value)
		if origin == "" {
			continue
		}
		allowed[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if allowAny {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				requestOrigin := r.Header.Get("Origin")
				if _, ok := allowed[requestOrigin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", requestOrigin)
					w.Header().Set("Vary", "Origin")
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Provider")
			w.Header().Set("Access-Control-Max-Age", "86400")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
