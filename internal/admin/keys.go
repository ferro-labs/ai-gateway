package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// APIKey represents an API key for authenticating requests to the gateway.
type APIKey struct {
	ID        string     `json:"id"`
	Key       string     `json:"key"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
	Active    bool       `json:"active"`
}

// KeyStore is an in-memory store for API keys.
type KeyStore struct {
	mu     sync.RWMutex
	byID   map[string]*APIKey
	byKey  map[string]string // key string -> ID
}

// NewKeyStore creates a new KeyStore.
func NewKeyStore() *KeyStore {
	return &KeyStore{
		byID:  make(map[string]*APIKey),
		byKey: make(map[string]string),
	}
}

// Create generates a new API key with the given name, scopes, and optional expiration.
func (s *KeyStore) Create(name string, scopes []string, expiresAt *time.Time) (*APIKey, error) {
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}
	key := "gw-" + hex.EncodeToString(keyBytes)

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}
	id := fmt.Sprintf("%x-%x-%x-%x-%x",
		idBytes[0:4], idBytes[4:6], idBytes[6:8], idBytes[8:10], idBytes[10:16])

	if len(scopes) == 0 {
		scopes = []string{ScopeAdmin}
	}

	apiKey := &APIKey{
		ID:        id,
		Key:       key,
		Name:      name,
		Scopes:    scopes,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
		Active:    true,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = apiKey
	s.byKey[key] = id
	return apiKey, nil
}

// Get retrieves an API key by ID.
func (s *KeyStore) Get(id string) (*APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	return k, ok
}

// List returns all keys with the Key field masked.
func (s *KeyStore) List() []*APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]*APIKey, 0, len(s.byID))
	for _, k := range s.byID {
		masked := *k
		if len(masked.Key) > 8 {
			masked.Key = masked.Key[:8] + "..."
		}
		keys = append(keys, &masked)
	}
	return keys
}

// Revoke marks an API key as revoked and inactive.
func (s *KeyStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("key not found: %s", id)
	}
	now := time.Now()
	k.RevokedAt = &now
	k.Active = false
	return nil
}

// Update updates the name and scopes of an API key.
func (s *KeyStore) Update(id string, name string, scopes []string) (*APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", id)
	}
	if name != "" {
		k.Name = name
	}
	if len(scopes) > 0 {
		k.Scopes = scopes
	}
	masked := *k
	if len(masked.Key) > 8 {
		masked.Key = masked.Key[:8] + "..."
	}
	return &masked, nil
}

// Delete removes an API key from the store.
func (s *KeyStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("key not found: %s", id)
	}
	delete(s.byKey, k.Key)
	delete(s.byID, id)
	return nil
}

// RotateKey generates a new key string for an existing API key.
func (s *KeyStore) RotateKey(id string) (*APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", id)
	}

	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}
	newKey := "gw-" + hex.EncodeToString(keyBytes)

	delete(s.byKey, k.Key)
	k.Key = newKey
	s.byKey[newKey] = id
	now := time.Now()
	k.RotatedAt = &now

	return k, nil
}

// ValidateKey looks up a key by its full string and returns it if active.
func (s *KeyStore) ValidateKey(key string) (*APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	return k, true
}
