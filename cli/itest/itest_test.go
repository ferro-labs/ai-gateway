//go:build integration

// Package itest checks ferro's typed client against a running gateway.
// A decode failure indicates that the client is incompatible with the
// gateway response.
//
// Every test skips cleanly when FERRO_ITEST_URL is unset, so
// `go test -tags integration ./itest/` is always safe to run. Boot a gateway
// and run the suite with ./scripts/with-gateway.sh.
package itest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
)

func client(t *testing.T) *api.Client {
	t.Helper()
	u := os.Getenv("FERRO_ITEST_URL")
	if u == "" {
		t.Skip("FERRO_ITEST_URL not set")
	}
	c, err := api.New(u, os.Getenv("FERRO_ITEST_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// raw fetches a path with no decoding, so a failure can quote what the gateway
// actually sent rather than only how the typed decode objected to it.
func raw(t *testing.T, p string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(os.Getenv("FERRO_ITEST_URL"), "/")+p, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if k := os.Getenv("FERRO_ITEST_KEY"); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", p, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("GET %s: read body: %v", p, err)
	}
	return resp.StatusCode, b
}

func TestStatusAgainstRealGateway(t *testing.T) {
	r, err := client(t).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v (report %+v)", err, r)
	}
	// Providers is deliberately NOT asserted non-zero: a gateway booted with no
	// provider credentials reports no_providers and zero targets, which is the
	// state this suite runs in by default.
	if r.State == "unreachable" {
		t.Fatalf("expected a live gateway, got %+v", r)
	}
	if r.URL == "" || r.State == "" {
		t.Fatalf("status report is not usable: %+v", r)
	}
	// Targets is nil when /readyz answered 503 — its not_ready body carries no
	// targets array at all. That is the default state of this harness (no
	// provider credentials), so this is the common path here, not the edge one.
	targets := "—"
	if r.Targets != nil {
		targets = fmt.Sprintf("%d/%d", r.Targets.Routable, r.Targets.Total)
	}
	t.Logf("status: state=%s providers=%d models=%d targets=%s auth=%q warnings=%v",
		r.State, r.Providers, r.Models, targets, r.Auth, r.Warnings)
}

// A gateway with no routable targets must never report a target count. The
// client cannot know one: /readyz 503s with only {status, reason}. Reporting
// 0/0 would read as "none configured" when the truth is "none routable", and
// this suite runs in exactly that state, so the assertion is always live.
func TestNotReadyGatewayReportsNoTargetCount(t *testing.T) {
	r, err := client(t).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ready, status, err := client(t).Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status == http.StatusServiceUnavailable {
		if len(ready.Targets) != 0 {
			t.Fatalf("a not_ready body must carry no targets, got %d", len(ready.Targets))
		}
		if r.Targets != nil {
			t.Fatalf("Status must leave Targets nil when the gateway reported none, got %+v", r.Targets)
		}
		if len(r.Warnings) == 0 {
			t.Fatal("a not-ready gateway must explain itself in warnings")
		}
	}
}

func TestKeyLifecycle(t *testing.T) {
	c := client(t)
	if os.Getenv("FERRO_ITEST_KEY") == "" {
		t.Skip("needs admin key")
	}
	ctx := context.Background()
	exp := time.Now().Add(1 * time.Hour).UTC()
	created, err := c.CreateKey(ctx, api.KeyCreateRequest{
		Name: "ferro-itest", Scopes: []string{"read_only"}, ExpiresAt: &exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deferred so a failure below never leaves a live credential behind.
	defer func() { _ = c.RevokeKey(ctx, created.ID) }()

	if !strings.HasPrefix(created.Key, "fgw_") || strings.Contains(created.Key, "...") {
		t.Fatalf("create response did not contain a valid one-time secret (length=%d, masked=%v)",
			len(created.Key), strings.Contains(created.Key, "..."))
	}
	if created.ID == "" || api.KeyState(*created) != "active" {
		t.Fatalf("created key is not usable: id_present=%v state=%s", created.ID != "", api.KeyState(*created))
	}

	got, err := c.Key(ctx, created.ID)
	if err != nil {
		t.Fatalf("read back %s: %v", created.ID, err)
	}
	if got.Key == created.Key || strings.Contains(got.Key, strings.TrimPrefix(created.Key, "fgw_")) {
		t.Fatalf("read-back leaked the full one-time secret for key id %s", created.ID)
	}
	if !strings.Contains(got.Key, "...") {
		t.Fatalf("read-back must be masked for key id %s", created.ID)
	}
	if s := api.KeyState(*got); s != "active" {
		t.Fatalf("state after create = %q, want active for key id %s", s, created.ID)
	}

	rotated, err := c.RotateKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !strings.HasPrefix(rotated.Key, "fgw_") || strings.Contains(rotated.Key, "...") {
		t.Fatalf("rotate response did not contain a valid one-time secret (length=%d, masked=%v)",
			len(rotated.Key), strings.Contains(rotated.Key, "..."))
	}
	if rotated.Key == created.Key {
		t.Fatal("rotate returned the previous secret")
	}

	if err := c.RevokeKey(ctx, created.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	after, err := c.Key(ctx, created.ID)
	if err != nil {
		if !api.IsNotSupported(err) { // a store that deletes on revoke would 404
			t.Fatalf("read back after revoke: %v", err)
		}
		return
	}
	if s := api.KeyState(*after); s != "revoked" {
		t.Fatalf("state after revoke = %q, want revoked for key id %s", s, created.ID)
	}
}

// TestAdminKeysWireShape asserts the two shapes internal/fixture hard-codes.
// A disagreement here means the local fixture must be reconciled with the
// running gateway before it can support other CLI tests.
func TestAdminKeysWireShape(t *testing.T) {
	c := client(t)
	if os.Getenv("FERRO_ITEST_KEY") == "" {
		t.Skip("needs admin key")
	}

	status, body := raw(t, "/admin/keys")
	if status != http.StatusOK {
		t.Fatalf("GET /admin/keys = %d: %s", status, body)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		t.Fatalf("/admin/keys is not a bare array (%v).\nredacted JSON: %s", err, safeBody(body))
	}

	ctx := context.Background()
	created, err := c.CreateKey(ctx, api.KeyCreateRequest{Name: "ferro-itest-shape", Scopes: []string{"read_only"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.RevokeKey(ctx, created.ID) }()

	listed, err := c.Keys(ctx)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	var found *api.Key
	for i := range listed {
		if listed[i].ID == created.ID {
			found = &listed[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("created key %s is missing from the listing (%d rows)", created.ID, len(listed))
	}
	if found.Key == created.Key {
		t.Fatalf("SECRET LEAK: /admin/keys serves the full secret for %s", created.ID)
	}
	if !strings.Contains(found.Key, "...") {
		t.Fatalf("listed key %s is not masked head...tail", created.ID)
	}
}

// contract is one endpoint of the parity table.
type contract struct {
	path     string
	optional bool // may answer 404/501 on a build without the feature
	call     func(context.Context, *api.Client) (any, error)
}

// TestContractParity calls every endpoint ferro reads through the typed client
// and asserts it decodes. A decode failure means internal/api's types have
// drifted from the gateway — the whole reason this suite exists.
func TestContractParity(t *testing.T) {
	c := client(t)

	cases := []contract{
		{path: "/health", call: func(ctx context.Context, c *api.Client) (any, error) {
			r, _, err := c.Health(ctx)
			return r, err
		}},
		{path: "/readyz", call: func(ctx context.Context, c *api.Client) (any, error) {
			r, _, err := c.Ready(ctx)
			return r, err
		}},
		{path: "/admin/health", call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.AdminHealth(ctx)
		}},
		{path: "/v1/models", call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.Models(ctx)
		}},
		{path: "/admin/keys", call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.Keys(ctx)
		}},
		{path: "/admin/logs", optional: true, call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.Logs(ctx, api.LogsQuery{Limit: 5})
		}},
		{path: "/admin/logs/stats", optional: true, call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.LogStats(ctx, api.LogsQuery{})
		}},
		{path: "/admin/plugins", call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.Plugins(ctx)
		}},
		{path: "/admin/plugins/catalog", call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.PluginCatalog(ctx)
		}},
		{path: "/admin/sessions", optional: true, call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.Sessions(ctx)
		}},
		{path: "/admin/audit", optional: true, call: func(ctx context.Context, c *api.Client) (any, error) {
			return c.Audit(ctx, api.AuditQuery{Limit: 5})
		}},
	}

	authed := os.Getenv("FERRO_ITEST_KEY") != ""
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// /admin/* and /v1/* sit behind the same bearer middleware, so
			// without a credential they answer 401 — which proves nothing about
			// the payload contract this test exists to check.
			if !authed && tc.path != "/health" && tc.path != "/readyz" {
				t.Skip("needs FERRO_ITEST_KEY")
			}
			// The wire bytes, not the round-tripped struct: a decoded struct
			// cannot show a field internal/api silently drops, and the point of
			// this suite is what the gateway actually sends.
			status, body := raw(t, tc.path)
			t.Logf("HTTP %d %s", status, safeBody(body))

			_, err := tc.call(ctx, c)
			switch {
			case err == nil:
			case tc.optional && api.IsNotSupported(err):
				t.Logf("absent on this build (expected for an optional endpoint): %v", err)
			default:
				t.Fatalf("typed client could not read %s: %v\nHTTP %d, redacted JSON: %s",
					tc.path, err, status, safeBody(body))
			}
		})
	}
}

// TestPluginCatalogIsPopulated pins the one parity case an empty decode hides:
// /admin/plugins/catalog is fixed for the life of the binary and never empty,
// so a successful decode of zero rows means the envelope moved.
func TestPluginCatalogIsPopulated(t *testing.T) {
	c := client(t)
	if os.Getenv("FERRO_ITEST_KEY") == "" {
		t.Skip("needs admin key")
	}
	got, err := c.PluginCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		_, body := raw(t, "/admin/plugins/catalog")
		t.Fatalf("catalog decoded to zero plugins — the {\"data\":…} envelope has moved.\nredacted JSON: %s", safeBody(body))
	}
	for _, p := range got {
		if p.Name == "" || p.Type == "" {
			t.Fatalf("catalog row is missing name/type: %+v", p)
		}
	}
	t.Logf("catalog: %d built-in plugins", len(got))
}

func TestChatStreamAndAttribution(t *testing.T) {
	model := os.Getenv("FERRO_ITEST_CHAT_MODEL")
	if model == "" {
		t.Skip("FERRO_ITEST_CHAT_MODEL not set (needs live provider credentials)")
	}
	c := client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now().Add(-2 * time.Minute)
	st, err := c.StreamChat(ctx, api.ChatRequest{
		Model:    model,
		Messages: []api.ChatMessage{{Role: "user", Content: "Reply with the word ok."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Cancel()

	var text strings.Builder
	var sawUsage, sawDone bool
	for ev := range st.Events {
		switch {
		case ev.Err != nil:
			t.Fatalf("stream error: %v", ev.Err)
		case ev.Done:
			sawDone = true
		case ev.Chunk != nil:
			if ev.Chunk.Usage != nil {
				sawUsage = true
			}
			for _, ch := range ev.Chunk.Choices {
				text.WriteString(ch.Delta.Content)
			}
		}
	}
	if !sawDone || !sawUsage || text.Len() == 0 {
		t.Fatalf("done=%v usage=%v answer_bytes=%d", sawDone, sawUsage, text.Len())
	}

	probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
	_, err = c.Logs(probeCtx, api.LogsQuery{Limit: 1})
	cancelProbe()
	if api.IsNotSupported(err) {
		return
	} else if err != nil {
		t.Fatalf("probe request-log store: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		attributionCtx, cancelAttribution := context.WithTimeout(ctx, 5*time.Second)
		entry, err := c.TraceAttribution(attributionCtx, st.RequestID, start)
		cancelAttribution()
		if err != nil {
			t.Fatal(err)
		}
		if entry != nil {
			if entry.Provider == "" {
				t.Fatalf("matched log row should attribute a provider: trace=%s", entry.TraceID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("request-log store is enabled but trace %s was not attributed within 5s", st.RequestID)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func safeBody(b []byte) string {
	var value any
	if json.Unmarshal(b, &value) != nil {
		return fmt.Sprintf("<%d non-JSON bytes>", len(b))
	}
	redact(value)
	b, _ = json.Marshal(value)
	const max = 600
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "… (truncated)"
	}
	return s
}

func TestSafeBodyRedactsCompoundCredentials(t *testing.T) {
	got := safeBody([]byte(`{
		"access_token":"one",
		"nested":{"refresh_token":"two","client_secret":"three"},
		"items":[{"private_key":"four"}],
		"apiKey":"five","X-API-Key":"six","sessionToken":"seven",
		"note":"visible"
	}`))
	for _, secret := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		if strings.Contains(got, secret) {
			t.Fatalf("safeBody leaked %q: %s", secret, got)
		}
	}
	if strings.Count(got, "[REDACTED]") != 7 || !strings.Contains(got, "visible") {
		t.Fatalf("every compound credential field must be redacted: %s", got)
	}
}

func redact(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normalized {
			case "key", "apikey", "xapikey", "privatekey",
				"secret", "clientsecret", "token", "accesstoken",
				"refreshtoken", "sessiontoken", "authorization":
				value[key] = "[REDACTED]"
			default:
				redact(child)
			}
		}
	case []any:
		for _, child := range value {
			redact(child)
		}
	}
}
