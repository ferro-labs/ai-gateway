package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

func TestMemorySessionStoreLifecycle(t *testing.T) {
	ctx := t.Context()
	s := NewSessionStore()

	sess, token, err := s.CreateSession(ctx, "master-key", "master-key", []string{model.ScopeAdmin}, DefaultSessionTTL)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !strings.HasPrefix(token, model.SessionTokenPrefix) {
		t.Fatalf("token %q lacks prefix %q", token, model.SessionTokenPrefix)
	}
	if sess.Subject != "master-key" {
		t.Fatalf("Subject = %q, want master-key", sess.Subject)
	}

	got, ok := s.ValidateSession(ctx, token)
	if !ok {
		t.Fatal("ValidateSession: fresh token rejected")
	}
	if got.ID != sess.ID {
		t.Fatalf("ID = %q, want %q", got.ID, sess.ID)
	}

	// Logout must actually invalidate — ASVS 5.0 7.4.1.
	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, ok := s.ValidateSession(ctx, token); ok {
		t.Fatal("ValidateSession: deleted session still validates")
	}
}

func TestMemorySessionStoreRejectsExpired(t *testing.T) {
	ctx := t.Context()
	s := NewSessionStore()

	_, token, err := s.CreateSession(ctx, "master-key", "master-key", []string{model.ScopeAdmin}, -time.Second)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, ok := s.ValidateSession(ctx, token); ok {
		t.Fatal("an already-expired session validated")
	}
}

func TestMemorySessionStoreRejectsIdle(t *testing.T) {
	ctx := t.Context()
	s := NewSessionStore()

	sess, token, err := s.CreateSession(ctx, "master-key", "master-key", []string{model.ScopeAdmin}, DefaultSessionTTL)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Backdate last-seen beyond the idle bound.
	s.mu.Lock()
	stored := s.byID[sess.ID]
	old := time.Now().Add(-2 * DefaultSessionIdleTTL)
	stored.LastSeenAt = &old
	s.mu.Unlock()

	if _, ok := s.ValidateSession(ctx, token); ok {
		t.Fatal("an idle-expired session validated")
	}
}

func TestMemorySessionStoreDeleteAll(t *testing.T) {
	ctx := t.Context()
	s := NewSessionStore()

	_, a, _ := s.CreateSession(ctx, "one", "one", []string{model.ScopeAdmin}, DefaultSessionTTL)
	_, b, _ := s.CreateSession(ctx, "two", "two", []string{model.ScopeAdmin}, DefaultSessionTTL)

	n, err := s.DeleteAllSessions(ctx)
	if err != nil {
		t.Fatalf("DeleteAllSessions: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	if _, ok := s.ValidateSession(ctx, a); ok {
		t.Fatal("session a survived DeleteAllSessions")
	}
	if _, ok := s.ValidateSession(ctx, b); ok {
		t.Fatal("session b survived DeleteAllSessions")
	}
}

func TestSessionTokensAreUnique(t *testing.T) {
	ctx := t.Context()
	s := NewSessionStore()
	seen := make(map[string]bool, 100)
	for range 100 {
		_, token, err := s.CreateSession(ctx, "x", "x", []string{model.ScopeAdmin}, DefaultSessionTTL)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if seen[token] {
			t.Fatal("duplicate session token generated")
		}
		seen[token] = true
	}
}
