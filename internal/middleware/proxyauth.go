package middleware

import (
	"net/http"
	"os"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/admin/handlers"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
)

// ProxyAuth returns a middleware that requires auth on proxy routes by default.
// Set ALLOW_UNAUTHENTICATED_PROXY=true to disable (local dev only).
//
// It accepts only long-lived API keys, not dashboard sessions — see
// ProxyAuthWithSessions for the session-aware variant production wiring uses.
func ProxyAuth(store repository.Store, masterKey string) func(http.Handler) http.Handler {
	return ProxyAuthWithSessions(store, nil, masterKey)
}

// ProxyAuthWithSessions is ProxyAuth extended to also accept a dashboard
// session bearer, exactly as AuthMiddlewareWithSessions extends AuthMiddleware.
//
// The dashboard Playground sends the operator's dashboard credential straight
// through to /v1/chat/completions, so /v1/* must accept a session the same
// way /admin/* does or the Playground breaks. This does not change which
// credentials may invoke inference: a session only ever carries the scopes of
// the credential it was minted from. sessions may be nil, in which case this
// behaves exactly like ProxyAuth.
func ProxyAuthWithSessions(store repository.Store, sessions repository.SessionStore, masterKey string) func(http.Handler) http.Handler {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_UNAUTHENTICATED_PROXY")), "true") {
		return func(next http.Handler) http.Handler { return next }
	}
	return handlers.AuthMiddlewareWithSessions(store, sessions, masterKey)
}
