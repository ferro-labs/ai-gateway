// Package authctx provides a lightweight context key for propagating an opaque
// API-key identifier across package boundaries without creating import cycles.
//
// The auth middleware (internal/admin) stores the key ID here after
// authentication; the gateway core reads it back to populate
// plugin.Context.Metadata["api_key"] so that per-key plugins (rate-limit,
// budget) can scope limits to the authenticated caller.
//
// Only the stable APIKey.ID — not the raw bearer secret — is stored here.
package authctx

import "context"

// contextKey is an unexported type used as a context key to avoid collisions
// with other packages that store values in context.
type contextKey struct{}

// WithKeyID returns a new context that carries the opaque API-key identifier id.
// id must not be the raw bearer secret; callers should pass a stable, non-secret
// identifier such as the database row ID of the authenticated key.
func WithKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// KeyID returns the opaque API-key identifier stored by WithKeyID, or ("", false)
// when no identifier is present in ctx.
func KeyID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(contextKey{}).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}
