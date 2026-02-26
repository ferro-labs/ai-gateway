package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const apiKeyContextKey contextKey = "api_key"

// API key permission scopes.
const (
	ScopeAdmin    = "admin"
	ScopeReadOnly = "read_only"
)

// APIKeyFromContext retrieves the authenticated API key from the request context.
func APIKeyFromContext(ctx context.Context) (*APIKey, bool) {
	key, ok := ctx.Value(apiKeyContextKey).(*APIKey)
	return key, ok
}

// AuthMiddleware returns a chi-compatible middleware that validates API keys
// and stores the authenticated key in the request context.
func AuthMiddleware(store Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
				return
			}

			key := strings.TrimPrefix(auth, "Bearer ")
			apiKey, ok := store.ValidateKey(key)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid or revoked API key")
				return
			}

			ctx := context.WithValue(r.Context(), apiKeyContextKey, apiKey)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireScope returns a middleware that checks whether the authenticated key
// has at least one of the required scopes.
func RequireScope(scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey, ok := APIKeyFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}

			for _, required := range scopes {
				for _, s := range apiKey.Scopes {
					if s == required {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			writeError(w, http.StatusForbidden, "insufficient permissions")
		})
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
