package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
)

type contextKey string

const apiKeyContextKey contextKey = "api_key"

// API key permission scopes.
const (
	ScopeAdmin    = "admin"
	ScopeReadOnly = "read_only"
)

// APIKeyFromContext retrieves the authenticated API key from the request context.
// It reports ok=false when no key is present or a typed-nil *APIKey was stored,
// so callers can safely dereference the returned key whenever ok is true.
func APIKeyFromContext(ctx context.Context) (*APIKey, bool) {
	key, ok := ctx.Value(apiKeyContextKey).(*APIKey)
	if !ok || key == nil {
		return nil, false
	}
	return key, true
}

// KeyIDFromContext returns the opaque identifier of the authenticated API key
// stored in ctx, or ("", false) when no key is present. The returned ID is
// derived from APIKey.ID — it never contains the raw bearer secret and is safe
// to use as a rate-limit or budget bucket key.
func KeyIDFromContext(ctx context.Context) (string, bool) {
	key, ok := APIKeyFromContext(ctx)
	if !ok || key == nil || key.ID == "" {
		return "", false
	}
	return key.ID, true
}

// ContextWithAPIKey returns a new context that carries key so that
// APIKeyFromContext and KeyIDFromContext can retrieve it, and also populates the
// authctx key-ID slot so that gateway.go can read the opaque identifier without
// importing this package. This is provided for use in tests and integration
// harnesses that need to simulate an authenticated request without going through
// the HTTP auth middleware.
func ContextWithAPIKey(ctx context.Context, key *APIKey) context.Context {
	return storeKeyInContext(ctx, key)
}

// AuthMiddleware returns a chi-compatible middleware that validates API keys
// and stores the authenticated key in the request context.
// If masterKey is non-empty, it is checked first and grants full admin scope.
func AuthMiddleware(store Store, masterKey string) func(http.Handler) http.Handler {
	bootstrapAdminKey := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_KEY"))
	bootstrapReadOnlyKey := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_READ_ONLY_KEY"))
	bootstrapEnabled := true
	if raw := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_ENABLED")); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			bootstrapEnabled = parsed
		}
	}

	bootstrapAdminAPIKey := &APIKey{
		ID:     "bootstrap-admin",
		Name:   "bootstrap-admin",
		Scopes: []string{ScopeAdmin},
		Active: true,
	}
	bootstrapReadOnlyAPIKey := &APIKey{
		ID:     "bootstrap-read-only",
		Name:   "bootstrap-read-only",
		Scopes: []string{ScopeReadOnly},
		Active: true,
	}
	masterAPIKey := &APIKey{
		ID:     "master-key",
		Name:   "master-key",
		Scopes: []string{ScopeAdmin},
		Active: true,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "missing or invalid authorization header", "authentication_error", "missing_api_key")
				return
			}

			key := strings.TrimPrefix(auth, "Bearer ")

			// 1. Master key check (always active if set).
			if masterKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(masterKey)) == 1 {
				next.ServeHTTP(w, r.WithContext(storeKeyInContext(r.Context(), masterAPIKey)))
				return
			}

			// 2. Bootstrap key check (only when store is empty and no master key is configured).
			if masterKey == "" && bootstrapEnabled && len(store.List(r.Context())) == 0 {
				if bootstrapAdminKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(bootstrapAdminKey)) == 1 {
					next.ServeHTTP(w, r.WithContext(storeKeyInContext(r.Context(), bootstrapAdminAPIKey)))
					return
				}

				if bootstrapReadOnlyKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(bootstrapReadOnlyKey)) == 1 {
					next.ServeHTTP(w, r.WithContext(storeKeyInContext(r.Context(), bootstrapReadOnlyAPIKey)))
					return
				}
			}

			// 3. Key store lookup.
			apiKey, ok := store.ValidateKey(r.Context(), key)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid or revoked API key", "authentication_error", "invalid_api_key")
				return
			}

			next.ServeHTTP(w, r.WithContext(storeKeyInContext(r.Context(), apiKey)))
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
				writeError(w, http.StatusUnauthorized, "authentication required", "authentication_error", "authentication_required")
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

			writeError(w, http.StatusForbidden, "insufficient permissions", "permission_error", "insufficient_scope")
		})
	}
}

// writeError writes a unified OpenAI-compatible JSON error response:
//
//	{"error":{"message":"...","type":"...","code":"..."}}
//
// errType and code may be empty; defaults are derived from the HTTP status.
func writeError(w http.ResponseWriter, status int, message, errType, code string) {
	if errType == "" {
		errType = defaultErrType(status)
	}
	if code == "" {
		code = errType
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
}

// storeKeyInContext stores key in ctx under both the admin-package context key
// (for APIKeyFromContext) and the authctx key (for gateway-level per-key
// plugins). Using a private helper ensures both slots are always written
// together and avoids drift between the two stores.
func storeKeyInContext(ctx context.Context, key *APIKey) context.Context {
	if key == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, apiKeyContextKey, key)
	if key.ID != "" {
		ctx = authctx.WithKeyID(ctx, key.ID)
	}
	return ctx
}

func defaultErrType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status >= 400 && status < 500:
		return "invalid_request_error"
	default:
		return "server_error"
	}
}
