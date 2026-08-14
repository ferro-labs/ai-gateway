package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// servicesStub serves the sessions/audit/plugins surface and records the last
// path and query it was asked for.
func servicesStub(t *testing.T, path *string, query *url.Values) *httptest.Server {
	t.Helper()
	record := func(r *http.Request) {
		*path = r.URL.Path
		q := r.URL.Query()
		*query = q
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/sessions", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, 200, `{"data":[
			{"id":"s1","credential_id":"k1","subject":"ci","scopes":["read_only"],
			 "created_at":"2026-08-08T09:00:00Z","last_seen_at":"2026-08-08T09:30:00Z",
			 "expires_at":"2026-08-09T09:00:00Z"},
			{"id":"s2","credential_id":"k2","subject":"ops","scopes":["admin"],
			 "created_at":"2026-08-08T08:00:00Z","expires_at":"2026-08-09T08:00:00Z"}]}`)
	})
	mux.HandleFunc("GET /admin/audit", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, 200, `{"data":[
			{"occurred_at":"2026-08-08T09:00:00Z","action":"key.create","actor":"ops","actor_id":"k2",
			 "target_id":"k9","outcome":"ok","detail":"scopes=read_only","source_ip":"10.0.0.1","trace_id":"t1"},
			{"occurred_at":"2026-08-08T09:01:00Z","action":"session.create","actor":"unknown",
			 "outcome":"denied"}],
			"summary":{"total_entries":2,"returned_entries":2}}`)
	})
	// A BARE ARRAY, unlike /admin/plugins/catalog one path segment down.
	mux.HandleFunc("GET /admin/plugins", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, 200, `[{"name":"request-logger","type":"logging","enabled":true},
			{"name":"word-filter","type":"guardrail","enabled":false}]`)
	})
	mux.HandleFunc("GET /admin/plugins/catalog", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, 200, `{"data":[
			{"name":"request-logger","type":"logging","summary":"Logs requests","settings":["persist"],"fails_open":true},
			{"name":"word-filter","type":"guardrail","summary":"Blocks words","settings":["blocked_words"],"fails_open":false}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSessionsUnwrapsDataEnvelope(t *testing.T) {
	var path string
	var query url.Values
	srv := servicesStub(t, &path, &query)

	sessions, err := testClient(t, srv.URL, "fgw_ok").Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/sessions" {
		t.Fatalf("want GET /admin/sessions, got %q", path)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	s := sessions[0]
	if s.ID != "s1" || s.CredentialID != "k1" || s.Subject != "ci" || len(s.Scopes) != 1 {
		t.Fatalf("bad decode: %+v", s)
	}
	if s.LastSeenAt == nil || !s.LastSeenAt.Equal(time.Date(2026, 8, 8, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("last_seen_at must decode: %+v", s.LastSeenAt)
	}
	if !s.ExpiresAt.Equal(time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("expires_at must decode: %+v", s.ExpiresAt)
	}
	// A session that has never been used has no last_seen_at at all.
	if sessions[1].LastSeenAt != nil {
		t.Fatalf("absent last_seen_at must stay nil: %+v", sessions[1])
	}
}

func TestSessions501IsNotSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotImplemented,
			`{"error":{"message":"sessions are disabled","type":"server_error"}}`)
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, "fgw_ok").Sessions(context.Background())
	if err == nil || !IsNotSupported(err) {
		t.Fatalf("sessions disabled must degrade one screen, got %v", err)
	}
}

func TestAuditDecodesAndEncodesQuery(t *testing.T) {
	var path string
	var query url.Values
	srv := servicesStub(t, &path, &query)
	c := testClient(t, srv.URL, "fgw_ok")

	since := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	page, err := c.Audit(context.Background(), AuditQuery{
		Action: "key.create", ActorID: "k2", Outcome: "ok", Since: since, Limit: 50, Offset: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/audit" {
		t.Fatalf("want GET /admin/audit, got %q", path)
	}
	want := url.Values{
		"action": {"key.create"}, "actor_id": {"k2"}, "outcome": {"ok"},
		"since": {"2026-08-08T00:00:00Z"}, "limit": {"50"}, "offset": {"10"},
	}
	if fmt.Sprint(query) != fmt.Sprint(want) {
		t.Fatalf("query = %v, want %v", query, want)
	}
	if page.Summary.TotalEntries != 2 || len(page.Data) != 2 {
		t.Fatalf("bad page: %+v", page)
	}
	e := page.Data[0]
	if e.Action != "key.create" || e.Actor != "ops" || e.ActorID != "k2" || e.TargetID != "k9" ||
		e.Outcome != "ok" || e.Detail == "" || e.SourceIP != "10.0.0.1" || e.TraceID != "t1" {
		t.Fatalf("bad decode: %+v", e)
	}
	if !e.OccurredAt.Equal(time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("occurred_at: %v", e.OccurredAt)
	}
	if page.Data[1].ActorID != "" || page.Data[1].Outcome != "denied" {
		t.Fatalf("optional fields: %+v", page.Data[1])
	}

	// A zero query sends nothing: an empty outcome is a 400 server-side.
	if _, err := c.Audit(context.Background(), AuditQuery{}); err != nil {
		t.Fatal(err)
	}
	if len(query) != 0 {
		t.Fatalf("zero query must send no parameters, got %v", query)
	}
}

func TestPluginsDecodesBareArray(t *testing.T) {
	var path string
	var query url.Values
	srv := servicesStub(t, &path, &query)

	plugins, err := testClient(t, srv.URL, "fgw_ok").Plugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/plugins" {
		t.Fatalf("want GET /admin/plugins, got %q", path)
	}
	if len(plugins) != 2 || plugins[0].Name != "request-logger" || plugins[0].Type != "logging" ||
		!plugins[0].Enabled || plugins[1].Enabled {
		t.Fatalf("bad decode: %+v", plugins)
	}
}

func TestPluginCatalogUnwrapsDataEnvelope(t *testing.T) {
	var path string
	var query url.Values
	srv := servicesStub(t, &path, &query)

	catalog, err := testClient(t, srv.URL, "fgw_ok").PluginCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/plugins/catalog" {
		t.Fatalf("want GET /admin/plugins/catalog, got %q", path)
	}
	if len(catalog) != 2 {
		t.Fatalf("want 2 builtins, got %d", len(catalog))
	}
	// fails_open lives only in the catalog — it is what the plugins table
	// merges in by name.
	if !catalog[0].FailsOpen || catalog[1].FailsOpen {
		t.Fatalf("fails_open must decode: %+v", catalog)
	}
	if catalog[0].Summary == "" || len(catalog[0].Settings) != 1 {
		t.Fatalf("bad decode: %+v", catalog[0])
	}
}
