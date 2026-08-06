package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

type auditPayload struct {
	Data    []model.AuditEntry `json:"data"`
	Summary struct {
		TotalEntries    int `json:"total_entries"`
		ReturnedEntries int `json:"returned_entries"`
	} `json:"summary"`
	Filters map[string]any `json:"filters"`
}

// seedAuditTrail wires an audited router and appends three rows that differ in
// every filterable dimension, so a filter that is silently ignored shows up as
// the wrong total rather than as an accidentally-correct one.
func seedAuditTrail(t *testing.T) (*Handlers, http.Handler, time.Time) {
	t.Helper()
	h, r, store := auditedSessionRouter(t)
	base := time.Now().UTC().Add(-time.Hour)
	seed := []model.AuditEntry{
		{OccurredAt: base, Action: "key.create", ActorID: "op-1", TargetID: "key-1", Outcome: model.AuditOK},
		{OccurredAt: base.Add(time.Minute), Action: "key.delete", ActorID: "op-2", TargetID: "key-1", Outcome: model.AuditDenied},
		{OccurredAt: base.Add(2 * time.Minute), Action: "key.create", ActorID: "op-2", TargetID: "key-2", Outcome: model.AuditError},
	}
	for _, entry := range seed {
		if err := store.Append(t.Context(), entry); err != nil {
			t.Fatalf("seed audit entry: %v", err)
		}
	}
	return h, r, base
}

func getAudit(t *testing.T, r http.Handler, query string, key *model.APIKey) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(http.MethodGet, "/admin/audit"+query, "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuditRead_Filters(t *testing.T) {
	h, r, base := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"unfiltered", "", 3},
		{"action", "?action=key.create", 2},
		{"actor_id", "?actor_id=op-2", 2},
		{"outcome ok", "?outcome=ok", 1},
		{"outcome denied", "?outcome=denied", 1},
		{"outcome error", "?outcome=error", 1},
		{"since", "?since=" + base.Add(90*time.Second).Format(time.RFC3339), 1},
		{"combined", "?action=key.create&actor_id=op-2", 1},
		{"no match", "?action=key.rotate", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := getAudit(t, r, tc.query, adminKey)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			var payload auditPayload
			decodeJSON(t, w.Body, &payload)
			if payload.Summary.TotalEntries != tc.want {
				t.Errorf("total_entries = %d, want %d", payload.Summary.TotalEntries, tc.want)
			}
			if payload.Summary.ReturnedEntries != len(payload.Data) {
				t.Errorf("returned_entries = %d, but data has %d rows",
					payload.Summary.ReturnedEntries, len(payload.Data))
			}
		})
	}
}

// Paging must narrow the page without narrowing the count, or a caller cannot
// tell how much trail is left to read.
func TestAuditRead_LimitAndOffsetPageWithoutHidingTheTotal(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	w := getAudit(t, r, "?limit=1&offset=1", adminKey)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var payload auditPayload
	decodeJSON(t, w.Body, &payload)
	if payload.Summary.TotalEntries != 3 {
		t.Errorf("total_entries = %d, want 3 — the total must ignore limit/offset", payload.Summary.TotalEntries)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("data has %d rows, want 1", len(payload.Data))
	}
	// Newest first, so offset 1 is the middle row.
	if payload.Data[0].Action != "key.delete" {
		t.Errorf("action = %q, want key.delete", payload.Data[0].Action)
	}
}

// The echoed filters are what the handler actually applied. Absent ones are
// omitted rather than echoed as "", which would read as a filter that matched
// everything.
func TestAuditRead_EchoesOnlyAppliedFilters(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	w := getAudit(t, r, "?action=key.create", adminKey)
	var payload auditPayload
	decodeJSON(t, w.Body, &payload)

	if payload.Filters["action"] != "key.create" {
		t.Errorf("filters.action = %v, want key.create", payload.Filters["action"])
	}
	for _, absent := range []string{"actor_id", "outcome", "since"} {
		if _, ok := payload.Filters[absent]; ok {
			t.Errorf("filters echoed %q, which the caller never sent", absent)
		}
	}
	for _, always := range []string{"limit", "offset"} {
		if _, ok := payload.Filters[always]; !ok {
			t.Errorf("filters omitted %q, which always has an applied value", always)
		}
	}
}

func TestAuditRead_RejectsUnknownOutcome(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	// "OK" and "denied " are here because the store matches the outcome column
	// exactly: a case- or whitespace-variant that reached it would return an
	// empty page reading as "nothing was denied".
	for _, bad := range []string{"maybe", "OK", "success", "denied "} {
		t.Run(bad, func(t *testing.T) {
			w := getAudit(t, r, "?outcome="+url.QueryEscape(bad), adminKey)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if got := errorCode(t, w.Body.Bytes()); got != "invalid_request" {
				t.Errorf("code = %q, want invalid_request", got)
			}
		})
	}
}

func TestAuditRead_RejectsMalformedPaging(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	for _, query := range []string{"?limit=0", "?limit=abc", "?offset=-1", "?since=yesterday"} {
		t.Run(query, func(t *testing.T) {
			if w := getAudit(t, r, query, adminKey); w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}

// A gateway with no audit store configured reports a deployment choice, not a
// fault, exactly as listLogs and listSessions do for theirs.
func TestAuditRead_NotImplementedWithoutStore(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	w := getAudit(t, r, "", adminKey)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", w.Code, w.Body.String())
	}
	if got := errorCode(t, w.Body.Bytes()); got != "not_implemented" {
		t.Errorf("code = %q, want not_implemented", got)
	}
}

func TestAuditRead_IsNotCacheable(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	w := getAudit(t, r, "", adminKey)
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// The trail is read by more people than hold credentials, so the response must
// carry only the fields AuditEntry declares — a handler that grew an extra
// field would disclose it to every one of them.
func TestAuditRead_ExposesNoFieldsBeyondTheStoredEntry(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	w := getAudit(t, r, "", adminKey)
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	decodeJSON(t, w.Body, &payload)
	if len(payload.Data) == 0 {
		t.Fatal("no rows returned")
	}

	allowed := map[string]bool{
		"occurred_at": true, "action": true, "actor": true, "actor_id": true,
		"target_id": true, "outcome": true, "detail": true, "source_ip": true, "trace_id": true,
	}
	for _, row := range payload.Data {
		for field := range row {
			if !allowed[field] {
				t.Errorf("response row carries unexpected field %q: %+v", field, row)
			}
		}
	}
}

// A read-only credential may read the trail; the route sits in the read group.
func TestAuditRead_ReadOnlyScopeIsSufficient(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	readKey := createReadOnlyKey(t, h)

	if w := getAudit(t, r, "", readKey); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestAuditRead_RequiresAuthentication(t *testing.T) {
	_, r, _ := seedAuditTrail(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/audit", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", w.Code, w.Body.String())
	}
}

// Guards the contract the dashboard binds to: the two summary keys and the
// filters object, not just the data array.
func TestAuditRead_ResponseShape(t *testing.T) {
	h, r, _ := seedAuditTrail(t)
	adminKey := createAdminKey(t, h)

	w := getAudit(t, r, "", adminKey)
	var raw map[string]json.RawMessage
	decodeJSON(t, w.Body, &raw)
	for _, key := range []string{"data", "summary", "filters"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("response is missing top-level key %q", key)
		}
	}
}
