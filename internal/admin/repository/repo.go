// Package repository is the admin control-plane storage layer: the API-key,
// session, config, and audit stores (in-memory and SQL), their schema
// migrations, and config secret scrubbing. It depends only on the shared
// admin/model types, never on the HTTP handlers that consume it.
package repository

import (
	"context"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// Store defines the interface for API key storage.
// The in-memory KeyStore implements this interface.
// Future implementations may use PostgreSQL, Redis, etc.
//
// Every method accepts a context.Context as its first parameter so request
// cancellation and deadlines propagate down to the underlying storage layer.
type Store interface {
	// Create mints a key with the given name, scopes and optional expiry. An
	// empty scope list means the caller did not ask for a privilege, so
	// implementations grant the least one available rather than admin — see
	// defaultScopes, which every implementation resolves the list through.
	Create(ctx context.Context, name string, scopes []string, expiresAt *time.Time) (*model.APIKey, error)
	Get(ctx context.Context, id string) (*model.APIKey, bool)
	List(ctx context.Context) []*model.APIKey
	// IsEmpty reports whether the store holds no keys. It returns an error
	// rather than a bare bool so callers can distinguish "no keys" from a
	// store that could not answer.
	IsEmpty(ctx context.Context) (bool, error)
	// CountAdminKeys returns how many stored keys can currently authenticate an
	// admin request: active (not revoked) and carrying the admin scope. The
	// admin API uses it to refuse a mutation that would remove the last such
	// key. Implementations must take the count as one consistent read so a
	// concurrent delete cannot be observed half-applied.
	CountAdminKeys(ctx context.Context) (int, error)
	Revoke(ctx context.Context, id string) error
	Update(ctx context.Context, id string, name string, scopes []string) (*model.APIKey, error)
	SetExpiration(ctx context.Context, id string, expiresAt *time.Time) error
	Delete(ctx context.Context, id string) error
	ValidateKey(ctx context.Context, key string) (*model.APIKey, bool)
	RotateKey(ctx context.Context, id string) (*model.APIKey, error)
	// Ping reports whether the store is reachable. Readiness probes call it to
	// gate traffic; it must be cheap and return quickly.
	Ping(ctx context.Context) error
}
