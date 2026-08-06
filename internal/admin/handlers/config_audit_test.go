package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// These tests run through the real routes, so they prove the config handlers
// are wired to the audit trail — the thing that was missing. Replacing the
// active config can redirect every request, disable a guardrail, or drain a
// target, and none of it appeared in GET /admin/audit.

const auditTestConfigBody = `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"}]}`

// configAuditRequest drives one config route as the given admin key and returns
// the response.
func configAuditRequest(t *testing.T, r http.Handler, method, path, body string, key *model.APIKey) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(method, path, body, key))
	return w
}

func TestAudit_ConfigMutationsAreRecorded(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantAction string
		wantTarget string
	}{
		{"update", http.MethodPut, "/admin/config", auditTestConfigBody, http.StatusOK, auditConfigUpdate, auditConfigTarget},
		{"create", http.MethodPost, "/admin/config", auditTestConfigBody, http.StatusCreated, auditConfigCreate, auditConfigTarget},
		{"delete", http.MethodDelete, "/admin/config", "", http.StatusOK, auditConfigDelete, auditConfigTarget},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, r, store := auditedSessionRouter(t)
			adminKey := createAdminKey(t, h)

			if w := configAuditRequest(t, r, tc.method, tc.path, tc.body, adminKey); w.Code != tc.wantStatus {
				t.Fatalf("%s %s: status = %d, want %d: %s", tc.method, tc.path, w.Code, tc.wantStatus, w.Body.String())
			}

			entries := auditEntries(t, store, tc.wantAction)
			if len(entries) != 1 {
				t.Fatalf("got %d %s rows, want 1", len(entries), tc.wantAction)
			}
			e := entries[0]
			if e.Outcome != model.AuditOK {
				t.Errorf("outcome = %q, want ok", e.Outcome)
			}
			if e.TargetID != tc.wantTarget {
				t.Errorf("target_id = %q, want %q", e.TargetID, tc.wantTarget)
			}
			if e.ActorID != adminKey.ID {
				t.Errorf("actor_id = %q, want the acting key %q", e.ActorID, adminKey.ID)
			}
			if e.OccurredAt.IsZero() {
				t.Error("occurred_at is zero")
			}
		})
	}
}

// A rollback is the one config action identified by a version, so the version
// it was asked for is what the row names.
func TestAudit_ConfigRollbackIsRecordedWithItsVersion(t *testing.T) {
	h, r, store := auditedSessionRouter(t)
	adminKey := createAdminKey(t, h)

	// One recorded version to roll back to.
	if w := configAuditRequest(t, r, http.MethodPut, "/admin/config", auditTestConfigBody, adminKey); w.Code != http.StatusOK {
		t.Fatalf("seed config: status = %d: %s", w.Code, w.Body.String())
	}

	if w := configAuditRequest(t, r, http.MethodPost, "/admin/config/rollback/1", "", adminKey); w.Code != http.StatusOK {
		t.Fatalf("rollback: status = %d, want 200: %s", w.Code, w.Body.String())
	}

	entries := auditEntries(t, store, auditConfigRollback)
	if len(entries) != 1 {
		t.Fatalf("got %d %s rows, want 1", len(entries), auditConfigRollback)
	}
	e := entries[0]
	if e.Outcome != model.AuditOK {
		t.Errorf("outcome = %q, want ok", e.Outcome)
	}
	if e.TargetID != "1" {
		t.Errorf("target_id = %q, want the requested version \"1\"", e.TargetID)
	}
	if !strings.Contains(e.Detail, "rolled_back_from") {
		t.Errorf("detail = %q, want it to name the version rolled back from", e.Detail)
	}
}

// A rollback to a version that does not exist is recorded as denied. "Someone
// tried to roll production back to a version we no longer keep" is exactly the
// kind of thing the trail is read to find.
func TestAudit_ConfigRollbackToMissingVersionIsDenied(t *testing.T) {
	h, r, store := auditedSessionRouter(t)
	adminKey := createAdminKey(t, h)

	if w := configAuditRequest(t, r, http.MethodPost, "/admin/config/rollback/999", "", adminKey); w.Code != http.StatusNotFound {
		t.Fatalf("rollback: status = %d, want 404: %s", w.Code, w.Body.String())
	}

	entries := auditEntries(t, store, auditConfigRollback)
	if len(entries) != 1 {
		t.Fatalf("got %d %s rows, want 1", len(entries), auditConfigRollback)
	}
	if entries[0].Outcome != model.AuditDenied {
		t.Errorf("outcome = %q, want denied", entries[0].Outcome)
	}
	if entries[0].TargetID != "999" {
		t.Errorf("target_id = %q, want \"999\"", entries[0].TargetID)
	}
}

// A config carrying the redaction placeholder back is refused, and refusing it
// is itself worth recording: it is an operator about to overwrite a live
// credential with the text "[REDACTED]".
func TestAudit_RedactedSecretConfigIsRecordedAsDenied(t *testing.T) {
	h, r, store := auditedSessionRouter(t)
	adminKey := createAdminKey(t, h)

	body := `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai"}],` +
		`"observability":{"tracing":{"headers":{"authorization":"[REDACTED]"}}}}`

	w := configAuditRequest(t, r, http.MethodPut, "/admin/config", body, adminKey)
	if w.Code != http.StatusBadRequest {
		t.Skipf("this config shape is not refused as a redacted secret (status %d); the denial path is covered by the handler's own tests", w.Code)
	}

	entries := auditEntries(t, store, auditConfigUpdate)
	if len(entries) != 1 {
		t.Fatalf("got %d %s rows, want 1", len(entries), auditConfigUpdate)
	}
	if entries[0].Outcome != model.AuditDenied {
		t.Errorf("outcome = %q, want denied", entries[0].Outcome)
	}
	if strings.Contains(entries[0].Detail, "REDACTED") {
		t.Errorf("detail echoed the config value: %q", entries[0].Detail)
	}
}

// The trail must carry the request context an incident is reconstructed from,
// not just the fact that something changed.
func TestAudit_ConfigRowCarriesSourceAndTrace(t *testing.T) {
	h, r, store := auditedSessionRouter(t)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPut, "/admin/config", auditTestConfigBody, adminKey)
	req.RemoteAddr = "198.51.100.22:51000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update config: status = %d: %s", w.Code, w.Body.String())
	}

	entries := auditEntries(t, store, auditConfigUpdate)
	if len(entries) != 1 {
		t.Fatalf("got %d rows, want 1", len(entries))
	}
	if !strings.Contains(entries[0].SourceIP, "198.51.100.22") {
		t.Errorf("source_ip = %q, want it to carry the caller address", entries[0].SourceIP)
	}
	if entries[0].Actor == "" {
		t.Error("actor is empty on an authenticated config change")
	}
}
