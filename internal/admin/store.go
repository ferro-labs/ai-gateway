package admin

import (
	"context"
	"time"
)

// Store defines the interface for API key storage.
// The in-memory KeyStore implements this interface.
// Future implementations may use PostgreSQL, Redis, etc.
//
// Every method accepts a context.Context as its first parameter so request
// cancellation and deadlines propagate down to the underlying storage layer.
type Store interface {
	Create(ctx context.Context, name string, scopes []string, expiresAt *time.Time) (*APIKey, error)
	Get(ctx context.Context, id string) (*APIKey, bool)
	List(ctx context.Context) []*APIKey
	// IsEmpty reports whether the store holds no keys. It returns an error
	// rather than a bare bool so callers can distinguish "no keys" from a
	// store that could not answer.
	IsEmpty(ctx context.Context) (bool, error)
	Revoke(ctx context.Context, id string) error
	Update(ctx context.Context, id string, name string, scopes []string) (*APIKey, error)
	SetExpiration(ctx context.Context, id string, expiresAt *time.Time) error
	Delete(ctx context.Context, id string) error
	ValidateKey(ctx context.Context, key string) (*APIKey, bool)
	RotateKey(ctx context.Context, id string) (*APIKey, error)
}
