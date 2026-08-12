package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// keysStub serves the /admin/keys surface and records the last request body.
func keysStub(t *testing.T, lastBody *string, lastPath *string) *httptest.Server {
	t.Helper()
	record := func(r *http.Request) {
		*lastPath = r.URL.Path
		if r.Body == nil {
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		*lastBody = string(b)
	}
	mux := http.NewServeMux()
	// Deliberately a BARE ARRAY — /admin/keys is the one list endpoint with no
	// {"data":…} envelope.
	mux.HandleFunc("GET /admin/keys", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, 200, `[
			{"id":"k1","key":"fgw_head8...il4","name":"ci","scopes":["read_only"],
			 "created_at":"2026-08-01T10:00:00Z","expires_at":"2026-09-01T10:00:00Z",
			 "last_used_at":"2026-08-08T09:00:00Z","usage_count":42,"active":true},
			{"id":"k2","key":"fgw_dead8...ad4","name":"old","scopes":["admin"],
			 "created_at":"2026-07-01T10:00:00Z","revoked_at":"2026-07-20T10:00:00Z",
			 "usage_count":0,"active":false}]`)
	})
	mux.HandleFunc("GET /admin/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, 200, `{"id":"k1","key":"fgw_head8...il4","name":"ci","scopes":["read_only"],
			"created_at":"2026-08-01T10:00:00Z","usage_count":42,"active":true}`)
	})
	mux.HandleFunc("POST /admin/keys", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, http.StatusCreated, `{"id":"k9","key":"fgw_fullsecret000","name":"ci",
			"scopes":["read_only"],"created_at":"2026-08-08T10:00:00Z","usage_count":0,"active":true}`)
	})
	mux.HandleFunc("POST /admin/keys/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(t, w, 200, `{"id":"k1","key":"fgw_rotatedsecret","name":"ci","scopes":["read_only"],
			"created_at":"2026-08-01T10:00:00Z","rotated_at":"2026-08-08T10:00:00Z","usage_count":42,"active":true}`)
	})
	mux.HandleFunc("POST /admin/keys/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.PathValue("id") == "k404" {
			writeJSON(t, w, http.StatusNotFound, `{"error":{"message":"key not found","type":"not_found_error"}}`)
			return
		}
		if r.PathValue("id") == "kweird" {
			writeJSON(t, w, 200, `{"status":"queued"}`)
			return
		}
		writeJSON(t, w, 200, `{"status":"revoked"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestKeysDecodesBareArray(t *testing.T) {
	var body, path string
	srv := keysStub(t, &body, &path)

	keys, err := testClient(t, srv.URL, "fgw_ok").Keys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/keys" {
		t.Fatalf("want GET /admin/keys, got %q", path)
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %d", len(keys))
	}
	k := keys[0]
	if k.ID != "k1" || k.Name != "ci" || k.UsageCount != 42 || !k.Active ||
		len(k.Scopes) != 1 || k.Scopes[0] != "read_only" {
		t.Fatalf("bad decode: %+v", k)
	}
	if k.ExpiresAt == nil || !k.ExpiresAt.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("expires_at must decode to a pointer: %+v", k.ExpiresAt)
	}
	if k.RevokedAt != nil || k.RotatedAt != nil {
		t.Fatalf("absent timestamps must stay nil: %+v", k)
	}
	if keys[1].RevokedAt == nil {
		t.Fatal("revoked_at must decode")
	}
}

func TestKeyStateIsDerived(t *testing.T) {
	ts := time.Now()
	past := ts.Add(-24 * time.Hour)
	future := ts.Add(24 * time.Hour)
	cases := []struct {
		name string
		key  Key
		want string
	}{
		{"active", Key{Active: true}, "active"},
		{"active with an expiry still ahead", Key{Active: true, ExpiresAt: &future}, "active"},
		// The shape a real expired key actually has. The gateway clears active
		// only in Revoke, so an expired-but-not-revoked key is served
		// active:true with revoked_at:null — this case is the whole reason
		// KeyState reads expires_at, and it reported "active" until it did.
		{"expired", Key{Active: true, ExpiresAt: &past}, "expired"},
		{"revoked", Key{Active: false, RevokedAt: &ts}, "revoked"},
		// revoked wins even if the gateway still reports the key active.
		{"revoked beats active", Key{Active: true, RevokedAt: &ts}, "revoked"},
		// A key can be both; revoked is the more specific answer.
		{"revoked beats expired", Key{Active: false, RevokedAt: &ts, ExpiresAt: &past}, "revoked"},
		// Not a shape the gateway serves, but it authenticates nothing, and
		// "expired" would name a deadline that has not passed.
		{"inactive without a revocation", Key{Active: false}, "revoked"},
	}
	for _, tc := range cases {
		if got := KeyState(tc.key); got != tc.want {
			t.Errorf("%s: KeyState = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestKeyGetByID(t *testing.T) {
	var body, path string
	srv := keysStub(t, &body, &path)

	k, err := testClient(t, srv.URL, "fgw_ok").Key(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/keys/k1" {
		t.Fatalf("want GET /admin/keys/k1, got %q", path)
	}
	if k.ID != "k1" {
		t.Fatalf("bad decode: %+v", k)
	}
}

func TestCreateKeyAlwaysSendsScopes(t *testing.T) {
	var body, path string
	srv := keysStub(t, &body, &path)

	exp := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	k, err := testClient(t, srv.URL, "fgw_ok").CreateKey(context.Background(),
		KeyCreateRequest{Name: "ci", Scopes: []string{"read_only"}, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/keys" {
		t.Fatalf("want POST /admin/keys, got %q", path)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body is not JSON: %q", body)
	}
	scopes, ok := sent["scopes"].([]any)
	if !ok || len(scopes) == 0 {
		// The gateway defaults a scope-less request to read_only, not admin.
		// ferro still always sends one, so the scope is the operator's choice
		// rather than a server default that can differ between versions.
		t.Fatalf("scopes must always be sent: %q", body)
	}
	if got, _ := sent["expires_at"].(string); got != "2026-09-08T10:00:00Z" {
		t.Fatalf("expires_at must be RFC3339: %q", body)
	}
	if k.Key != "fgw_fullsecret000" {
		t.Fatalf("create must return the full secret once: %+v", k)
	}
}

func TestCreateKeyOmitsAbsentExpiry(t *testing.T) {
	var body, path string
	srv := keysStub(t, &body, &path)

	if _, err := testClient(t, srv.URL, "fgw_ok").CreateKey(context.Background(),
		KeyCreateRequest{Name: "ci", Scopes: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "expires_at") {
		t.Fatalf("no expiry means the field is omitted, not null: %q", body)
	}
}

func TestRotateKeyReturnsNewSecret(t *testing.T) {
	var body, path string
	srv := keysStub(t, &body, &path)

	k, err := testClient(t, srv.URL, "fgw_ok").RotateKey(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/keys/k1/rotate" {
		t.Fatalf("want POST /admin/keys/k1/rotate, got %q", path)
	}
	if k.Key != "fgw_rotatedsecret" || k.RotatedAt == nil {
		t.Fatalf("rotate must return the new secret and rotated_at: %+v", k)
	}
}

func TestRevokeKey(t *testing.T) {
	var body, path string
	srv := keysStub(t, &body, &path)
	c := testClient(t, srv.URL, "fgw_ok")

	if err := c.RevokeKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if path != "/admin/keys/k1/revoke" {
		t.Fatalf("want POST /admin/keys/k1/revoke, got %q", path)
	}
	// A 200 that does not say "revoked" is not a revocation.
	if err := c.RevokeKey(context.Background(), "kweird"); err == nil {
		t.Fatal("an unexpected status must not be reported as success")
	}
	if err := c.RevokeKey(context.Background(), "k404"); err == nil {
		t.Fatal("a 404 must propagate")
	}
}
