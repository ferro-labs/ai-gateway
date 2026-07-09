package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
//
// Key holds the display form (see displayKey) on every value a Store reads
// back. The full secret appears only on the values returned by Create and
// RotateKey, which build it in memory — it is never stored and cannot be
// recovered afterwards.
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

// keyRecord pairs a stored key with the hash it is looked up by. The hash is a
// storage detail and never leaves the store.
type keyRecord struct {
	apiKey *APIKey
	hash   string
}

// KeyStore is an in-memory store for API keys.
type KeyStore struct {
	mu     sync.RWMutex
	byID   map[string]*keyRecord
	byHash map[string]string // sha256 hex -> ID
}

// NewKeyStore creates a new KeyStore.
func NewKeyStore() *KeyStore {
	return &KeyStore{
		byID:   make(map[string]*keyRecord),
		byHash: make(map[string]string),
	}
}

const (
	keyDisplayHead = 8
	keyDisplayTail = 4
)

// hashKey derives the value stored and looked up in place of the secret.
// Generated keys are 32 bytes of CSPRNG output, so one SHA-256 pass is
// sufficient; a password KDF would add cost to every authenticated request
// without adding entropy to defend.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// displayKey renders the operator-visible form of a key, captured once at
// creation. Keeping both ends lets an operator match a key they hold against a
// stored record; the leading "fgw_" alone does not distinguish one key from
// another.
func displayKey(key string) string {
	if len(key) < keyDisplayHead+keyDisplayTail {
		return "..."
	}
	return key[:keyDisplayHead] + "..." + key[len(key)-keyDisplayTail:]
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

// Create generates a new API key with the given name, scopes, and optional
// expiration. The returned key carries the full secret; the stored copy does
// not.
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

	stored := &APIKey{
		ID:         id,
		Key:        displayKey(key),
		Name:       name,
		Scopes:     append([]string(nil), scopes...),
		CreatedAt:  time.Now(),
		ExpiresAt:  cloneTime(expiresAt),
		UsageCount: 0,
		Active:     true,
	}
	hash := hashKey(key)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = &keyRecord{apiKey: stored, hash: hash}
	s.byHash[hash] = id

	created := cloneAPIKey(stored)
	created.Key = key
	return created, nil
}

// Get retrieves an API key by ID.
func (s *KeyStore) Get(_ context.Context, id string) (*APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return cloneAPIKey(rec.apiKey), true
}

// List returns all keys.
func (s *KeyStore) List(_ context.Context) []*APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]*APIKey, 0, len(s.byID))
	for _, rec := range s.byID {
		keys = append(keys, cloneAPIKey(rec.apiKey))
	}
	return keys
}

// IsEmpty reports whether the store holds no API keys.
func (s *KeyStore) IsEmpty(_ context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID) == 0, nil
}

// Revoke marks an API key as revoked and inactive.
func (s *KeyStore) Revoke(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	now := time.Now()
	rec.apiKey.RevokedAt = &now
	rec.apiKey.Active = false
	return nil
}

// Update updates the name and scopes of an API key.
func (s *KeyStore) Update(_ context.Context, id string, name string, scopes []string) (*APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	if name != "" {
		rec.apiKey.Name = name
	}
	if len(scopes) > 0 {
		rec.apiKey.Scopes = append([]string(nil), scopes...)
	}
	return cloneAPIKey(rec.apiKey), nil
}

// SetExpiration updates the expiration time for an API key.
func (s *KeyStore) SetExpiration(_ context.Context, id string, expiresAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	if expiresAt == nil {
		rec.apiKey.ExpiresAt = nil
		return nil
	}

	normalized := expiresAt.UTC()
	t := normalized
	rec.apiKey.ExpiresAt = &t
	return nil
}

// Delete removes an API key from the store.
func (s *KeyStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	delete(s.byHash, rec.hash)
	delete(s.byID, id)
	return nil
}

// RotateKey generates a new key string for an existing API key. The returned
// key carries the new secret; the stored copy does not.
func (s *KeyStore) RotateKey(_ context.Context, id string) (*APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}

	newKey, err := generateAPIKeyString()
	if err != nil {
		return nil, err
	}

	delete(s.byHash, rec.hash)
	rec.hash = hashKey(newKey)
	s.byHash[rec.hash] = id
	rec.apiKey.Key = displayKey(newKey)
	now := time.Now()
	rec.apiKey.RotatedAt = &now

	rotated := cloneAPIKey(rec.apiKey)
	rotated.Key = newKey
	return rotated, nil
}

// ValidateKey looks up a key by its full string and returns it if active.
func (s *KeyStore) ValidateKey(_ context.Context, key string) (*APIKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byHash[hashKey(key)]
	if !ok {
		return nil, false
	}
	k := s.byID[id].apiKey
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
