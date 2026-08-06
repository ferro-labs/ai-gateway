package model

import (
	"context"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
)

type contextKey string

const apiKeyContextKey contextKey = "api_key"

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

// ContextWithAPIKey returns a new context that carries key so that
// APIKeyFromContext can retrieve it, and also populates the authctx key-ID slot
// so that gateway.go can read the opaque identifier without importing this
// package. This is provided for use in tests and integration harnesses that
// need to simulate an authenticated request without going through the HTTP auth
// middleware.
func ContextWithAPIKey(ctx context.Context, key *APIKey) context.Context {
	return StoreKeyInContext(ctx, key, "")
}

// StoreKeyInContext stores key in ctx under both the admin-package context key
// (for APIKeyFromContext) and the authctx key (for gateway-level per-key
// plugins, i.e. budget/ratelimit bucketing). Using a private helper ensures
// both slots are always written together and avoids drift between the two.
//
// bucketID is the authctx value. Pass "" to bucket under key.ID itself, which
// is correct whenever the authenticated identity IS the credential (a direct
// API key, or the master key). A session is the one case where the two must
// differ: APIKeyFromContext must keep seeing the session's own ID
// (deleteSession depends on the "session:" prefix to recover it), but the
// bucket must follow the credential the session was minted from, or minting a
// session would reset that credential's budget/rate-limit bucket.
func StoreKeyInContext(ctx context.Context, key *APIKey, bucketID string) context.Context {
	if key == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, apiKeyContextKey, key)
	if bucketID == "" {
		bucketID = key.ID
	}
	if bucketID != "" {
		ctx = authctx.WithKeyID(ctx, bucketID)
	}
	return ctx
}
