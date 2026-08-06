package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// recordAuditFrom drives recordAudit against a memory-backed Handlers and
// returns the single stored entry, so the tests assert what actually persists
// rather than a log line — the log is the fail-open fallback, not the record.
//
// A nil actor stands for a request with no authenticated caller, the case a
// denied sign-in presents.
func recordAuditFrom(t *testing.T, actor *model.APIKey, remoteAddr, action, target string, outcome model.AuditOutcome, attrs ...any) model.AuditEntry {
	t.Helper()

	ctx := t.Context()
	if actor != nil {
		ctx = model.ContextWithAPIKey(ctx, actor)
	}

	h := &Handlers{Audit: repository.NewMemoryAuditStore()}
	req := httptest.NewRequestWithContext(ctx, "POST", "/admin/keys", nil)
	req.RemoteAddr = remoteAddr
	h.recordAudit(req, action, target, outcome, attrs...)

	result, err := h.Audit.List(ctx, model.AuditQuery{})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("got %d stored audit entries, want 1", len(result.Data))
	}
	return result.Data[0]
}

// captureAuditLog swaps the shared logger for one writing JSON into a buffer,
// and returns a function that decodes the audit records from it. The store is
// the durable trail; this is for the tests that assert the log fallback.
func captureAuditLog(t *testing.T) func() []map[string]any {
	t.Helper()

	var buf bytes.Buffer
	original := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Level: "info", Output: &buf}))
	t.Cleanup(func() { logger.SetDefault(original) })

	return func() []map[string]any {
		var records []map[string]any
		for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("decode log line %q: %v", line, err)
			}
			if rec["msg"] == "admin audit" {
				records = append(records, rec)
			}
		}
		return records
	}
}

func TestRecordAudit_NamesTheActorAndTarget(t *testing.T) {
	actor := &model.APIKey{ID: "key-1", Name: "alice", Scopes: []string{model.ScopeAdmin}}
	entry := recordAuditFrom(t, actor, "203.0.113.4", "key.delete", "key-victim", model.AuditOK)

	if entry.Action != "key.delete" {
		t.Errorf("action = %q, want key.delete", entry.Action)
	}
	if entry.TargetID != "key-victim" {
		t.Errorf("target_id = %q, want key-victim", entry.TargetID)
	}
	if entry.Outcome != model.AuditOK {
		t.Errorf("outcome = %q, want ok", entry.Outcome)
	}
	if !strings.Contains(entry.Actor, "alice") || !strings.Contains(entry.Actor, "key-1") {
		t.Errorf("actor = %q, want it to name alice and key-1", entry.Actor)
	}
	// actor_id is the credential id alone, so one operator's history filters
	// without a substring match on the display name.
	if entry.ActorID != "key-1" {
		t.Errorf("actor_id = %q, want key-1", entry.ActorID)
	}
	// The source address is the point of the row on a denied sign-in; it must be
	// carried on every row, not only that one.
	if entry.SourceIP != "203.0.113.4" {
		t.Errorf("source_ip = %q, want 203.0.113.4", entry.SourceIP)
	}
}

// The stored row must never carry a bearer secret. APIKey.Key holds the raw
// credential, and a record that quoted it would write the very thing the store
// hashes into a trail read during an incident.
func TestRecordAudit_CarriesNoSecret(t *testing.T) {
	const secret = "fgw_super_secret_value"
	actor := &model.APIKey{ID: "key-1", Name: "alice", Key: secret, Scopes: []string{model.ScopeAdmin}}
	entry := recordAuditFrom(t, actor, "203.0.113.4", "key.rotate", "key-1", model.AuditOK)

	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("re-encode entry: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("audit entry contains the bearer secret: %s", raw)
	}
}

// A denied sign-in has no authenticated caller, so actor must fall to
// unrecorded and actor_id to empty — while source_ip still carries the address
// the attempt came from, which is the whole value of the row.
func TestRecordAudit_MarksAnUnauthenticatedCaller(t *testing.T) {
	entry := recordAuditFrom(t, nil, "198.51.100.9", "session.create", "*", model.AuditDenied, "reason", "invalid_api_key")

	if entry.Actor != model.ActorUnrecorded {
		t.Errorf("actor = %q, want %q", entry.Actor, model.ActorUnrecorded)
	}
	if entry.ActorID != "" {
		t.Errorf("actor_id = %q, want empty", entry.ActorID)
	}
	if entry.Outcome != model.AuditDenied {
		t.Errorf("outcome = %q, want denied", entry.Outcome)
	}
	if entry.SourceIP != "198.51.100.9" {
		t.Errorf("source_ip = %q, want 198.51.100.9", entry.SourceIP)
	}
}

// The attrs become the JSON detail on the stored row, so a "key.update" that
// silently changed a key's scopes can be told from one that only renamed it.
func TestRecordAudit_RendersAttrsAsDetail(t *testing.T) {
	actor := &model.APIKey{ID: "key-1", Name: "alice", Scopes: []string{model.ScopeAdmin}}
	entry := recordAuditFrom(t, actor, "203.0.113.4", "key.update", "key-9", model.AuditOK, "name", "prod", "scopes", []string{"admin"})

	var detail map[string]any
	if err := json.Unmarshal([]byte(entry.Detail), &detail); err != nil {
		t.Fatalf("detail is not JSON: %q: %v", entry.Detail, err)
	}
	if detail["name"] != "prod" {
		t.Errorf("detail.name = %v, want prod", detail["name"])
	}
	if _, ok := detail["scopes"]; !ok {
		t.Errorf("detail missing scopes: %v", detail)
	}
}

// A nil store must not panic and must still leave the log line: the trail is
// best-effort, and the log is the record when no store is configured.
func TestRecordAudit_NilStoreStillLogs(t *testing.T) {
	records := captureAuditLog(t)

	h := &Handlers{} // Audit is nil
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/admin/keys", nil)
	req.RemoteAddr = "203.0.113.4"
	h.recordAudit(req, "key.delete", "key-victim", model.AuditOK)

	got := records()
	if len(got) != 1 {
		t.Fatalf("got %d log records, want 1", len(got))
	}
	if got[0]["action"] != "key.delete" || got[0]["outcome"] != "ok" {
		t.Errorf("log record = %v, want key.delete/ok", got[0])
	}
}

// A failing store must not fail the audited action: recordAudit returns
// normally and the error is logged, because failing key management over a
// missing audit row is the worse of the two outcomes. See recordAudit's doc.
func TestRecordAudit_FailingStoreDoesNotBreakTheAction(t *testing.T) {
	var buf bytes.Buffer
	original := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Level: "info", Output: &buf}))
	t.Cleanup(func() { logger.SetDefault(original) })

	h := &Handlers{Audit: failingAuditStore{}}
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/admin/keys", nil)
	req.RemoteAddr = "203.0.113.4"
	// A panic or re-raised error here is the failure this guards against:
	// recordAudit must swallow the store error and return normally.
	h.recordAudit(req, "key.delete", "key-victim", model.AuditOK)

	if !strings.Contains(buf.String(), "audit append failed") {
		t.Errorf("expected an 'audit append failed' error line when the store rejects the append; got:\n%s", buf.String())
	}
}

// failingAuditStore rejects every append, standing in for a database that is
// down at the moment a mutation needs auditing.
type failingAuditStore struct{}

func (failingAuditStore) Append(context.Context, model.AuditEntry) error {
	return errors.New("audit backend unavailable")
}
func (failingAuditStore) List(context.Context, model.AuditQuery) (model.AuditResult, error) {
	return model.AuditResult{}, nil
}
func (failingAuditStore) Ping(context.Context) error { return nil }
func (failingAuditStore) Close() error               { return nil }

// The synthetic credentials use the same string for name and ID, so the
// name-and-ID form rendered "master-key (master-key)" — which reads as two
// distinct facts that happen to coincide rather than one identity.
func TestActorFromContext_DoesNotRepeatASyntheticIdentity(t *testing.T) {
	ctx := model.ContextWithAPIKey(t.Context(), &model.APIKey{ID: "master-key", Name: "master-key", Scopes: []string{model.ScopeAdmin}})
	if got := model.ActorFromContext(ctx); got != "master-key" {
		t.Errorf("actor = %q, want %q", got, "master-key")
	}
}

// A context with an APIKey carrying neither identifier must still not produce
// an empty actor, or the record becomes indistinguishable from a row written
// before actors were tracked.
func TestActorFromContext_NeverReturnsEmpty(t *testing.T) {
	ctx := model.ContextWithAPIKey(t.Context(), &model.APIKey{})
	if got := model.ActorFromContext(ctx); got != model.ActorUnrecorded {
		t.Errorf("actor = %q, want %q", got, model.ActorUnrecorded)
	}
}
