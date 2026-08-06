package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
)

// A refusal is a fact the trail exists to record. Until these rows were written,
// recordAudit was wired only to the success path of a key mutation: "somebody
// tried to delete the last admin key" left no record anywhere, and the trail
// answered it with a silence indistinguishable from nobody having tried.
//
// The assertions run through the real routes, so they prove the wiring rather
// than that recordAudit works in isolation.
func TestAudit_KeyMutationRefusalsAreRecorded(t *testing.T) {
	cases := []struct {
		name       string
		arrange    func(t *testing.T, h *Handlers) (caller *model.APIKey, targetID string)
		method     string
		path       func(id string) string
		body       string
		wantAction string
		wantReason string
	}{
		{
			name: "self delete",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantAction: "key.delete",
			wantReason: codeSelfMutation,
		},
		{
			name: "self revoke",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPost,
			path:       func(id string) string { return "/admin/keys/" + id + "/revoke" },
			wantAction: "key.revoke",
			wantReason: codeSelfMutation,
		},
		{
			name: "self de-scope",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"scopes":["read_only"]}`,
			wantAction: "key.update",
			wantReason: codeSelfMutation,
		},
		{
			name: "last admin delete",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantAction: "key.delete",
			wantReason: codeLastAdminKey,
		},
		{
			name: "last admin revoke",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodPost,
			path:       func(id string) string { return "/admin/keys/" + id + "/revoke" },
			wantAction: "key.revoke",
			wantReason: codeLastAdminKey,
		},
		{
			name: "last admin de-scope",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"scopes":["read_only"]}`,
			wantAction: "key.update",
			wantReason: codeLastAdminKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, router := newGuardRouter(t, repository.NewKeyStore())
			store := repository.NewMemoryAuditStore()
			h.Audit = store
			caller, targetID := tc.arrange(t, h)

			req := authedRequest(tc.method, tc.path(targetID), tc.body, caller)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("got status %d, want 409: %s", w.Code, w.Body.String())
			}

			entries := auditEntries(t, store, tc.wantAction)
			if len(entries) != 1 {
				t.Fatalf("got %d %s rows, want 1 — a refusal must leave a record", len(entries), tc.wantAction)
			}
			e := entries[0]
			if e.Outcome != model.AuditDenied {
				t.Errorf("outcome = %q, want denied", e.Outcome)
			}
			if e.TargetID != targetID {
				t.Errorf("target_id = %q, want the refused key %q", e.TargetID, targetID)
			}
			if !strings.Contains(e.Detail, tc.wantReason) {
				t.Errorf("detail = %q, want it to name the refusal %q", e.Detail, tc.wantReason)
			}
			if strings.Contains(e.Detail, caller.Key) {
				t.Errorf("the denied row echoed the acting credential: %q", e.Detail)
			}
		})
	}
}

// A signed-in operator signing out is the other half of session.create, which is
// already audited. Without this row the trail shows sessions opening and never
// closing, so "was that operator still signed in when the config changed" has no
// answer.
func TestAudit_SessionLogoutIsRecorded(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	store := repository.NewMemoryAuditStore()
	h.Audit = store
	adminKey := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPost, "/admin/session", "", adminKey))
	if w.Code != http.StatusCreated {
		t.Fatalf("exchange: status = %d, want 201: %s", w.Code, w.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	outW := httptest.NewRecorder()
	r.ServeHTTP(outW, tokenRequest(http.MethodDelete, "/admin/session", "", body.Token))
	if outW.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204: %s", outW.Code, outW.Body.String())
	}

	entries := auditEntries(t, store, "session.logout")
	if len(entries) != 1 {
		t.Fatalf("got %d session.logout rows, want 1", len(entries))
	}
	e := entries[0]
	if e.Outcome != model.AuditOK {
		t.Errorf("outcome = %q, want ok", e.Outcome)
	}
	if e.ActorID != adminKey.ID {
		t.Errorf("actor_id = %q, want the credential behind the session %q", e.ActorID, adminKey.ID)
	}
	// The target is the opaque session id. The token never reaches the handler,
	// and must never reach the trail.
	if e.TargetID == "" || strings.Contains(e.TargetID, repository.SessionIdentityPrefix) {
		t.Errorf("target_id = %q, want the bare session id", e.TargetID)
	}
	if strings.Contains(e.TargetID+e.Detail, body.Token) {
		t.Errorf("the logout row echoed the session token: %+v", e)
	}
}
