package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// SessionIdentityPrefix marks the synthetic APIKey.ID a session is presented
// to the rest of the stack as (see AuthMiddlewareWithSessions), so it can
// never collide with a real key ID. Shared here rather than repeated as a
// string literal at every read/write site.
const SessionIdentityPrefix = "session:"

// Session lifetimes follow NIST SP 800-63B-4 AAL2: 24 hours absolute, one hour
// idle. ASVS 5.0 removed its hardcoded values and asks only that the numbers be
// documented with justification (7.1.1), so the source cited here is NIST.
const (
	DefaultSessionTTL     = 24 * time.Hour
	DefaultSessionIdleTTL = time.Hour
)

// sessionLastSeenResolution is how stale a stored last_seen_at may get before
// ValidateSession writes a fresh one.
//
// Refreshing on every authenticated request is what actually costs: the
// dashboard polls, so one operator with a tab open turns a read path into a
// steady write path — and on SQLite, where the pool is a single connection,
// those writes serialize against request traffic.
//
// A minute of slack against a one-hour idle bound is under two percent of it, so
// a session still expires when it should; and it collapses however many requests
// arrive in that minute into one write.
const sessionLastSeenResolution = time.Minute

// ErrSessionNotFound reports a session id that does not exist.
var ErrSessionNotFound = errors.New("session not found")

// cloneSession defensively copies a Session before it leaves the store, the
// same discipline cloneAPIKey applies to APIKey: callers must never be able to
// mutate a slice or time field and have it reach back into stored state.
func cloneSession(s *model.Session) *model.Session {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Scopes = append([]string(nil), s.Scopes...)
	if s.LastSeenAt != nil {
		lastSeen := *s.LastSeenAt
		cp.LastSeenAt = &lastSeen
	}
	return &cp
}

// SessionStore persists dashboard sessions. It is separate from Store because
// the two hold different kinds of credential with different lifetimes; sharing
// one table would rebuild the conflation this design removes.
//
// Every implementation must enforce both bounds at lookup time rather than
// relying on a sweeper: a row that has outlived either bound must never
// validate, even if no sweep has run.
type SessionStore interface {
	// CreateSession mints a session and returns it together with the plaintext
	// token, which is available only here and never recoverable afterwards.
	// credentialID is the ID of the APIKey the caller authenticated with; it is
	// stored on the session so a later request can re-check that credential is
	// still usable (see AuthMiddlewareWithSessions).
	CreateSession(ctx context.Context, subject, credentialID string, scopes []string, ttl time.Duration) (*model.Session, string, error)
	// ValidateSession resolves a plaintext token, refreshing last-seen. It
	// reports false for an unknown, expired, or idle-expired token without
	// distinguishing between them.
	ValidateSession(ctx context.Context, token string) (*model.Session, bool)
	ListSessions(ctx context.Context) ([]*model.Session, error)
	DeleteSession(ctx context.Context, id string) error
	// DeleteAllSessions removes every session and returns how many were
	// removed. It is the "sign everyone out" control.
	DeleteAllSessions(ctx context.Context) (int, error)
}

// hashSessionToken hashes a token for storage and lookup.
//
// Unsalted SHA-256 with no work factor is deliberate and matches hashKey: the
// token is 32 bytes of crypto/rand, so there is no low-entropy space for an
// attacker who obtains the table to grind, and lookup must stay O(1) on a path
// that runs for every authenticated request.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// generateSessionToken returns a new prefixed token with 256 bits of entropy,
// comfortably above the 128 bits ASVS 5.0 7.2.3 requires.
func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return model.SessionTokenPrefix + hex.EncodeToString(b), nil
}

func generateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isLive reports whether a session satisfies both the absolute and the idle
// bound. A session that has never been seen since creation is measured from
// CreatedAt.
//
// This is the definition every backend enforces at lookup, and the one the
// sweeps delete against. SQLSessionStore restates it as SQL (see
// sweepExpiredSessions); the two must agree, which TestSweepMatchesIsLive
// asserts on every dialect.
func isLive(s *model.Session, now time.Time) bool {
	if !now.Before(s.ExpiresAt) {
		return false
	}
	return idleSince(s).After(idleCutoff(now))
}

// idleSince is the instant a session's idle clock runs from: its last-seen time,
// or its creation if it has never been seen.
func idleSince(s *model.Session) time.Time {
	if s.LastSeenAt != nil {
		return *s.LastSeenAt
	}
	return s.CreatedAt
}

// idleCutoff returns the instant at or before which a session's idle clock makes
// it dead. Split out so the sweeps bind exactly the value isLive compares.
func idleCutoff(now time.Time) time.Time {
	return now.Add(-DefaultSessionIdleTTL)
}

// shouldRefreshLastSeen reports whether a validated session's last-seen is stale
// enough to be worth a write. See sessionLastSeenResolution.
func shouldRefreshLastSeen(s *model.Session, now time.Time) bool {
	return now.Sub(idleSince(s)) >= sessionLastSeenResolution
}

// MemorySessionStore is the SessionStore used when no database is configured.
// API_KEY_STORE_BACKEND defaults to memory, so this is the default deployment's
// implementation, not a test double.
//
// Sessions do not survive a restart here. That is correct: a restart signing
// every operator out is a safe outcome, not a lost feature.
type MemorySessionStore struct {
	mu     sync.Mutex
	byID   map[string]*model.Session
	byHash map[string]string // token hash -> session id
}

// NewSessionStore creates a new MemorySessionStore.
func NewSessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		byID:   make(map[string]*model.Session),
		byHash: make(map[string]string),
	}
}

// CreateSession mints a new session for subject and returns it with the
// plaintext token.
func (s *MemorySessionStore) CreateSession(_ context.Context, subject, credentialID string, scopes []string, ttl time.Duration) (*model.Session, string, error) {
	token, err := generateSessionToken()
	if err != nil {
		return nil, "", err
	}
	id, err := generateSessionID()
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()
	stored := &model.Session{
		ID:           id,
		CredentialID: credentialID,
		Subject:      subject,
		Scopes:       append([]string(nil), scopes...),
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	s.byID[id] = stored
	s.byHash[hashSessionToken(token)] = id

	return cloneSession(stored), token, nil
}

// sweep drops sessions that can no longer validate. The caller holds s.mu.
//
// It runs on create rather than on a ticker, here and in SQLSessionStore, for
// one reason: creation is the only thing that makes this store grow, so sweeping
// there ties the reclaim rate to the growth rate exactly. A ticker would need an
// interval guessed against a workload nobody measured, a goroutine and a
// shutdown path to own it, and on SQLite it would contend for the single
// connection the request path is using — to delete rows that, between sign-ins,
// nothing is producing.
func (s *MemorySessionStore) sweep(now time.Time) {
	for id, stored := range s.byID {
		if isLive(stored, now) {
			continue
		}
		delete(s.byID, id)
		for hash, mapped := range s.byHash {
			if mapped == id {
				delete(s.byHash, hash)
			}
		}
	}
}

// ValidateSession resolves token to a live session, refreshing last-seen.
func (s *MemorySessionStore) ValidateSession(_ context.Context, token string) (*model.Session, bool) {
	if token == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.byHash[hashSessionToken(token)]
	if !ok {
		return nil, false
	}
	stored, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	now := time.Now().UTC()
	if !isLive(stored, now) {
		return nil, false
	}
	if shouldRefreshLastSeen(stored, now) {
		stored.LastSeenAt = &now
	}

	return cloneSession(stored), true
}

// ListSessions returns every currently live session, newest first, with the id
// as the tiebreak.
//
// Map iteration is randomised, so without the sort the sessions page reshuffles
// its rows on every poll. The order matches SQLSessionStore.ListSessions'
// ORDER BY exactly, so the two backends serve the same list in the same order.
func (s *MemorySessionStore) ListSessions(_ context.Context) ([]*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	out := make([]*model.Session, 0, len(s.byID))
	for _, stored := range s.byID {
		if !isLive(stored, now) {
			continue
		}
		out = append(out, cloneSession(stored))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// DeleteSession removes the session with the given id.
func (s *MemorySessionStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.byID[id]; !ok {
		return ErrSessionNotFound
	}
	delete(s.byID, id)
	for hash, mapped := range s.byHash {
		if mapped == id {
			delete(s.byHash, hash)
		}
	}
	return nil
}

// DeleteAllSessions removes every session and reports how many were removed.
func (s *MemorySessionStore) DeleteAllSessions(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.byID)
	s.byID = make(map[string]*model.Session)
	s.byHash = make(map[string]string)
	return n, nil
}
