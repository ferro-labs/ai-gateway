package admin

import "time"

// Store defines the interface for API key storage.
// The in-memory KeyStore implements this interface.
// Future implementations may use PostgreSQL, Redis, etc.
type Store interface {
	Create(name string, scopes []string, expiresAt *time.Time) (*APIKey, error)
	Get(id string) (*APIKey, bool)
	List() []*APIKey
	Revoke(id string) error
	Update(id string, name string, scopes []string) (*APIKey, error)
	SetExpiration(id string, expiresAt *time.Time) error
	Delete(id string) error
	ValidateKey(key string) (*APIKey, bool)
	RotateKey(id string) (*APIKey, error)
}
