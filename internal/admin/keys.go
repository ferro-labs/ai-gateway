package admin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrKeyNotFound is returned by Store implementations when an operation targets
// an API key ID that does not exist. Handlers use errors.Is to distinguish a
// genuine not-found (HTTP 404) from an internal or transient store failure
// (HTTP 500), so a database outage is never reported to callers as a 404.
var ErrKeyNotFound = errors.New("key not found")

// APIKey represents an API key for authenticating requests to the gateway.
type APIKey struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RotatedAt  *time.Time `json:"rotated_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	UsageCount int64      `json:"usage_count"`
	Active     bool       `json:"active"`
}

// KeyStore is an in-memory store for API keys.
type KeyStore struct {
	mu    sync.RWMutex
	byID  map[string]*APIKey
	byKey map[string]string // key string -> ID
}

// NewKeyStore creates a new KeyStore.
func NewKeyStore() *KeyStore {
	return &KeyStore{
		byID:  make(map[string]*APIKey),
		byKey: make(map[string]string),
	}
}

const keyMaskPrefixLen = 8

// maskKey truncates key to a short prefix followed by an ellipsis when it is
// longer than keyMaskPrefixLen so full secret values never appear in admin API
// responses. A non-empty key at or below the prefix length is fully masked to
// avoid leaking short secrets verbatim; the empty string is returned unchanged.
func maskKey(key string) string {
	if len(key) > keyMaskPrefixLen {
		return key[:keyMaskPrefixLen] + "..."
	}
	if key == "" {
		return ""
	}
	return "..."
}

func cloneAPIKey(k *APIKey) *APIKey {
	if k == nil {
		return nil
	}

	cp := *k
	cp.Scopes = append([]string(nil), k.Scopes...)
	cp.RevokedAt = cloneTime(k.RevokedAt)
	cp.ExpiresAt = cloneTime(k.ExpiresAt)
	cp.RotatedAt = cloneTime(k.RotatedAt)
	cp.LastUsedAt = cloneTime(k.LastUsedAt)
	return &cp
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

// Create generates a new API key with the given name, scopes, and optional expiration.
func (s *KeyStore) Create(_ context.Context, name string, scopes []string, expiresAt *time.Time) (*APIKey, error) {
	key, err := generateAPIKeyString()
	if err != nil {
		return nil, err
	}
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	if len(scopes) == 0 {
		scopes = []string{ScopeAdmin}
	}

	apiKey := &APIKey{
		ID:         id,
		Key:        key,
		Name:       name,
		Scopes:     append([]string(nil), scopes...),
		CreatedAt:  time.Now(),
		ExpiresAt:  cloneTime(expiresAt),
		UsageCount: 0,
		Active:     true,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = apiKey
	s.byKey[key] = id
	return cloneAPIKey(apiKey), nil
}

// Get retrieves an API key by ID.
func (s *KeyStore) Get(_ context.Context, id string) (*APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return cloneAPIKey(k), true
}

// List returns all keys with the Key field masked.
func (s *KeyStore) List(_ context.Context) []*APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]*APIKey, 0, len(s.byID))
	for _, k := range s.byID {
		masked := cloneAPIKey(k)
		masked.Key = maskKey(masked.Key)
		keys = append(keys, masked)
	}
	return keys
}

// Revoke marks an API key as revoked and inactive.
func (s *KeyStore) Revoke(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	now := time.Now()
	k.RevokedAt = &now
	k.Active = false
	return nil
}

// Update updates the name and scopes of an API key.
func (s *KeyStore) Update(_ context.Context, id string, name string, scopes []string) (*APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	if name != "" {
		k.Name = name
	}
	if len(scopes) > 0 {
		k.Scopes = append([]string(nil), scopes...)
	}
	masked := cloneAPIKey(k)
	masked.Key = maskKey(masked.Key)
	return masked, nil
}

// SetExpiration updates the expiration time for an API key.
func (s *KeyStore) SetExpiration(_ context.Context, id string, expiresAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	if expiresAt == nil {
		k.ExpiresAt = nil
		return nil
	}

	normalized := expiresAt.UTC()
	t := normalized
	k.ExpiresAt = &t
	return nil
}

// Delete removes an API key from the store.
func (s *KeyStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	delete(s.byKey, k.Key)
	delete(s.byID, id)
	return nil
}

// RotateKey generates a new key string for an existing API key.
func (s *KeyStore) RotateKey(_ context.Context, id string) (*APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}

	newKey, err := generateAPIKeyString()
	if err != nil {
		return nil, err
	}

	delete(s.byKey, k.Key)
	k.Key = newKey
	s.byKey[newKey] = id
	now := time.Now()
	k.RotatedAt = &now

	return cloneAPIKey(k), nil
}

// ValidateKey looks up a key by its full string and returns it if active.
func (s *KeyStore) ValidateKey(_ context.Context, key string) (*APIKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byKey[key]
	if !ok {
		return nil, false
	}
	k := s.byID[id]
	if !k.Active || k.RevokedAt != nil {
		return nil, false
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return nil, false
	}
	now := time.Now().UTC()
	lastUsedAt := now
	k.LastUsedAt = &lastUsedAt
	k.UsageCount++
	return cloneAPIKey(k), true
}
