package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// keyRecord pairs a stored key with the hash it is looked up by. The hash is a
// storage detail and never leaves the store.
type keyRecord struct {
	apiKey *model.APIKey
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
func hashKey(key string) string {
	// The keys are 32 bytes of crypto/rand output (see generateAPIKeyString),
	// not user-chosen passwords, so there is no low-entropy space to grind: a
	// slow password KDF would add a hash to every authenticated request while
	// defending nothing. One SHA-256 over a full-entropy value is the right
	// primitive.
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// DisplayKey renders the operator-visible form of a key, captured once at
// creation. Keeping both ends lets an operator match a key they hold against a
// stored record; the leading "fgw_" alone does not distinguish one key from
// another.
func DisplayKey(key string) string {
	if len(key) < keyDisplayHead+keyDisplayTail {
		return "..."
	}
	return key[:keyDisplayHead] + "..." + key[len(key)-keyDisplayTail:]
}

func cloneAPIKey(k *model.APIKey) *model.APIKey {
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

// defaultScopes resolves the scope list a new key is created with.
//
// An unspecified list grants the least privilege a key can carry, not the most:
// a caller that names no scope has not asked for admin, and a credential minted
// from an under-specified request must not come with the power to mint more.
// Granting admin here would also make the quietest possible mistake — an
// omitted field, a client that spells the field differently — the one that
// hands out full control of the control plane.
//
// Read-only rather than an error because the store is not the layer that
// validates a request: an error surfaces to the admin API as a 500 with no way
// for the caller to tell an under-specified request from a broken database, and
// a caller that wants admin can say so in one field. Both stores route through
// this so the two can never disagree on what an empty list means.
func defaultScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{model.ScopeReadOnly}
	}
	return scopes
}

// Create generates a new API key with the given name, scopes, and optional
// expiration. An empty scope list yields a read-only key (see defaultScopes).
// The returned key carries the full secret; the stored copy does not.
func (s *KeyStore) Create(_ context.Context, name string, scopes []string, expiresAt *time.Time) (*model.APIKey, error) {
	key, err := generateAPIKeyString()
	if err != nil {
		return nil, err
	}
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	scopes = defaultScopes(scopes)

	stored := &model.APIKey{
		ID:         id,
		Key:        DisplayKey(key),
		Name:       name,
		Scopes:     append([]string(nil), scopes...),
		CreatedAt:  time.Now().UTC(),
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
func (s *KeyStore) Get(_ context.Context, id string) (*model.APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return cloneAPIKey(rec.apiKey), true
}

// List returns all keys, newest first, with the id as the tiebreak.
//
// Map iteration is randomised, so without the sort two reads of an unchanged
// store return the same keys in a different order — which reorders the rows in
// the dashboard's key table under a poll that changed nothing. The order matches
// SQLStore.List's ORDER BY exactly (see keyListOrder), so switching backends
// does not switch what the Admin API serves.
func (s *KeyStore) List(_ context.Context) []*model.APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]*model.APIKey, 0, len(s.byID))
	for _, rec := range s.byID {
		keys = append(keys, cloneAPIKey(rec.apiKey))
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].CreatedAt.After(keys[j].CreatedAt)
		}
		return keys[i].ID > keys[j].ID
	})
	return keys
}

// IsEmpty reports whether the store holds no API keys.
func (s *KeyStore) IsEmpty(_ context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID) == 0, nil
}

// CountAdminKeys returns the number of stored keys that can authenticate an
// admin request. The whole scan runs under one read lock so the result is a
// consistent snapshot rather than a sum over a moving map.
func (s *KeyStore) CountAdminKeys(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, rec := range s.byID {
		if model.IsUsableAdmin(rec.apiKey) {
			count++
		}
	}
	return count, nil
}

// Revoke marks an API key as revoked and inactive.
func (s *KeyStore) Revoke(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", model.ErrKeyNotFound, id)
	}
	now := time.Now().UTC()
	rec.apiKey.RevokedAt = &now
	rec.apiKey.Active = false
	return nil
}

// Update updates the name and scopes of an API key.
func (s *KeyStore) Update(_ context.Context, id string, name string, scopes []string) (*model.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", model.ErrKeyNotFound, id)
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
		return fmt.Errorf("%w: %s", model.ErrKeyNotFound, id)
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
		return fmt.Errorf("%w: %s", model.ErrKeyNotFound, id)
	}
	delete(s.byHash, rec.hash)
	delete(s.byID, id)
	return nil
}

// RotateKey generates a new key string for an existing API key. The returned
// key carries the new secret; the stored copy does not.
func (s *KeyStore) RotateKey(_ context.Context, id string) (*model.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", model.ErrKeyNotFound, id)
	}

	newKey, err := generateAPIKeyString()
	if err != nil {
		return nil, err
	}

	delete(s.byHash, rec.hash)
	rec.hash = hashKey(newKey)
	s.byHash[rec.hash] = id
	rec.apiKey.Key = DisplayKey(newKey)
	now := time.Now().UTC()
	rec.apiKey.RotatedAt = &now

	rotated := cloneAPIKey(rec.apiKey)
	rotated.Key = newKey
	return rotated, nil
}

// ValidateKey looks up a key by its full string and returns it if active. The
// empty string is never a valid key: an "Authorization: Bearer " header with no
// value must not match a stored record, however that record came to exist.
func (s *KeyStore) ValidateKey(_ context.Context, key string) (*model.APIKey, bool) {
	if key == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byHash[hashKey(key)]
	if !ok {
		return nil, false
	}
	k := s.byID[id].apiKey
	if !model.KeyIsUsable(k) {
		return nil, false
	}
	now := time.Now().UTC()
	lastUsedAt := now
	k.LastUsedAt = &lastUsedAt
	k.UsageCount++
	return cloneAPIKey(k), true
}

// Ping reports whether the store is reachable. The in-memory KeyStore is always
// reachable, so it returns nil.
func (s *KeyStore) Ping(_ context.Context) error {
	return nil
}
