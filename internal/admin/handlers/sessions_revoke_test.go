package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// sessionIDFor resolves a minted token back to its session id, which is what
// the revoke route addresses. The token itself never appears in a URL.
func sessionIDFor(t *testing.T, h *Handlers, token string) string {
	t.Helper()
	sess, ok := h.Sessions.ValidateSession(t.Context(), token)
	if !ok {
		t.Fatal("validate session: token was rejected")
	}
	return sess.ID
}

func deleteSessionByID(t *testing.T, r http.Handler, id string, key *model.APIKey) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(http.MethodDelete, "/admin/sessions/"+id, "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRevokeOneSessionLeavesTheOthers is the reason this route exists: before
// it, revoking one operator meant DELETE /admin/sessions, which signs out
// everyone including the operator doing the revoking.
func TestRevokeOneSessionLeavesTheOthers(t *testing.T) {
	h, r, audit := auditedSessionRouter(t)
	adminKey := createAdminKey(t, h)

	revoked := exchangeSession(t, r, adminKey.Key)
	survivor := exchangeSession(t, r, adminKey.Key)
	revokedID := sessionIDFor(t, h, revoked)

	if w := deleteSessionByID(t, r, revokedID, adminKey); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: status = %d, want 204: %s", w.Code, w.Body.String())
	}

	assertSessionStatus(t, r, revoked, http.StatusUnauthorized)
	assertSessionStatus(t, r, survivor, http.StatusOK)

	entries := auditEntries(t, audit, "session.revoke")
	if len(entries) != 1 {
		t.Fatalf("got %d session.revoke rows, want 1", len(entries))
	}
	if entries[0].TargetID != revokedID {
		t.Errorf("target_id = %q, want the revoked session id %q", entries[0].TargetID, revokedID)
	}
	if entries[0].ActorID != adminKey.ID {
		t.Errorf("actor_id = %q, want the acting key %q", entries[0].ActorID, adminKey.ID)
	}
	if entries[0].Outcome != model.AuditOK {
		t.Errorf("outcome = %q, want ok", entries[0].Outcome)
	}
}

// An id that names no session is a success: the caller asked for it to stop
// working and it already does not work. A 404 would also tell an attacker which
// session ids are live.
func TestRevokeSessionUnknownIDIsSuccess(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	adminKey := createAdminKey(t, h)

	for _, id := range []string{"never-existed", "00000000000000000000000000000000"} {
		t.Run(id, func(t *testing.T) {
			if w := deleteSessionByID(t, r, id, adminKey); w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
			}
		})
	}
}

// Revoking the same session twice is the same success, so a retried request
// after a dropped response cannot fail.
func TestRevokeSessionIsIdempotent(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	adminKey := createAdminKey(t, h)

	token := exchangeSession(t, r, adminKey.Key)
	id := sessionIDFor(t, h, token)

	for attempt := 1; attempt <= 2; attempt++ {
		if w := deleteSessionByID(t, r, id, adminKey); w.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: status = %d, want 204: %s", attempt, w.Code, w.Body.String())
		}
	}
	assertSessionStatus(t, r, token, http.StatusUnauthorized)
}

// The sign-one-out and sign-everyone-out routes must stay distinct: a caller
// asking to revoke one session must never reach deleteAllSessions.
func TestRevokeSessionDoesNotShadowRevokeAll(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	adminKey := createAdminKey(t, h)

	first := exchangeSession(t, r, adminKey.Key)
	second := exchangeSession(t, r, adminKey.Key)

	if w := deleteSessionByID(t, r, sessionIDFor(t, h, first), adminKey); w.Code != http.StatusNoContent {
		t.Fatalf("revoke one: status = %d, want 204", w.Code)
	}
	assertSessionStatus(t, r, second, http.StatusOK)

	req := authedRequest(http.MethodDelete, "/admin/sessions", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke all: status = %d, want 200: %s", w.Code, w.Body.String())
	}
	assertSessionStatus(t, r, second, http.StatusUnauthorized)
}

// A read-only credential must not be able to sign another operator out — the
// route sits in the admin-scope write group.
func TestRevokeSessionRequiresAdminScope(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	adminKey := createAdminKey(t, h)
	readKey := createReadOnlyKey(t, h)

	token := exchangeSession(t, r, adminKey.Key)
	id := sessionIDFor(t, h, token)

	if w := deleteSessionByID(t, r, id, readKey); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
	assertSessionStatus(t, r, token, http.StatusOK)
}

// Sessions disabled reports a deployment choice, matching its sibling routes.
func TestRevokeSessionNotImplementedWithoutSessions(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	w := deleteSessionByID(t, r, "any-id", adminKey)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", w.Code, w.Body.String())
	}
	if got := errorCode(t, w.Body.Bytes()); got != "not_implemented" {
		t.Errorf("code = %q, want not_implemented", got)
	}
}
