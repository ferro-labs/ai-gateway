package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/go-chi/chi/v5"
)

// These tests exercise the audit trail through the real HTTP handlers, so they
// prove the wiring — that a route actually calls recordAudit — not just that
// recordAudit works in isolation (actor_audit_test.go covers that).
//
// The audit store is attached after construction: the router closes over the
// same *Handlers, and the handlers read h.Audit at request time, so setting it
// here reaches the live route without a bespoke router builder.

func auditedSessionRouter(t *testing.T) (*Handlers, chi.Router, *repository.MemoryAuditStore) {
	t.Helper()
	h, r := setupTestRouterWithSessions()
	store := repository.NewMemoryAuditStore()
	h.Audit = store
	return h, r, store
}

func auditEntries(t *testing.T, store *repository.MemoryAuditStore, action string) []model.AuditEntry {
	t.Helper()
	result, err := store.List(t.Context(), model.AuditQuery{Action: action})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	return result.Data
}

// A successful sign-in is recorded, and it names the credential that signed in.
// The endpoint authenticates the bearer itself, ahead of the auth middleware,
// so the actor is not in the request context — a naive ActorFromContext would
// record every successful sign-in as unattributed.
func TestAudit_SessionCreateSuccessNamesTheCredential(t *testing.T) {
	h, r, store := auditedSessionRouter(t)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/session", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("exchange: status = %d, want 201: %s", w.Code, w.Body.String())
	}

	entries := auditEntries(t, store, "session.create")
	if len(entries) != 1 {
		t.Fatalf("got %d session.create rows, want 1", len(entries))
	}
	e := entries[0]
	if e.Outcome != model.AuditOK {
		t.Errorf("outcome = %q, want ok", e.Outcome)
	}
	if e.ActorID != adminKey.ID {
		t.Errorf("actor_id = %q, want the signing key %q", e.ActorID, adminKey.ID)
	}
	if e.Actor == model.ActorUnrecorded {
		t.Error("a successful sign-in was recorded as unattributed; the accepted credential must be named")
	}
}

// A rejected credential is the highest-value row: it is what a brute-force
// attempt looks like. It must be recorded as denied, carry the source address,
// and never echo the presented bearer.
func TestAudit_SessionCreateDeniedRecordsSourceNotSecret(t *testing.T) {
	_, r, store := auditedSessionRouter(t)

	//nolint:gosec // G101: a credential-shaped fixture, not a real credential — the test needs a string that would be a secret if it leaked.
	const secret = "fgw_a_guessed_secret_value"
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/admin/session", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.RemoteAddr = "198.51.100.7:44100"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("denied exchange: status = %d, want 401", w.Code)
	}

	entries := auditEntries(t, store, "session.create")
	if len(entries) != 1 {
		t.Fatalf("got %d session.create rows, want 1", len(entries))
	}
	e := entries[0]
	if e.Outcome != model.AuditDenied {
		t.Errorf("outcome = %q, want denied", e.Outcome)
	}
	if e.Actor != model.ActorUnrecorded {
		t.Errorf("actor = %q, want %q — a rejected credential has no known actor", e.Actor, model.ActorUnrecorded)
	}
	if !strings.Contains(e.SourceIP, "198.51.100.7") {
		t.Errorf("source_ip = %q, want it to carry the caller address", e.SourceIP)
	}
	if strings.Contains(e.SourceIP+e.Actor+e.ActorID+e.TargetID+e.Detail, secret) {
		t.Errorf("the denied row echoed the presented bearer: %+v", e)
	}
}

// Creating a key through the real route records a key.create row, proving the
// key handler is wired to the trail and not only the standalone recordAudit.
func TestAudit_KeyCreateIsRecorded(t *testing.T) {
	h, r, store := auditedSessionRouter(t)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/keys", `{"name":"deploy-bot"}`, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: status = %d, want 201: %s", w.Code, w.Body.String())
	}

	entries := auditEntries(t, store, "key.create")
	if len(entries) != 1 {
		t.Fatalf("got %d key.create rows, want 1", len(entries))
	}
	if entries[0].Outcome != model.AuditOK {
		t.Errorf("outcome = %q, want ok", entries[0].Outcome)
	}
	// The acting admin key is named, not the key that was just created.
	if entries[0].ActorID != adminKey.ID {
		t.Errorf("actor_id = %q, want the acting key %q", entries[0].ActorID, adminKey.ID)
	}
}
