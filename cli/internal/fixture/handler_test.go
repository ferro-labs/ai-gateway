package fixture

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// reply is everything that outlives a finished request. do reads the body to
// completion and closes it, so there is no *http.Response worth handing back —
// only a spent connection a caller could forget to release.
type reply struct {
	status int
	header http.Header
	body   []byte
}

// do runs one request against a fresh server wrapping h. Every test goes
// through it, so the request context, the body read and the close are done in
// one place rather than re-hand-rolled per test.
func do(t *testing.T, h http.Handler, method, path, bearer, body string) reply {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: out}
}

// get is do for the read surface, which is most of it.
func get(t *testing.T, h http.Handler, method, path, bearer string) reply {
	t.Helper()
	return do(t, h, method, path, bearer, "")
}

func TestHealthShapeMatchesContract(t *testing.T) {
	res := get(t, Handler(Default()), "GET", "/health", "")
	if res.status != 200 {
		t.Fatalf("healthy /health must be 200, got %d", res.status)
	}
	var out struct {
		Status    string `json:"status"`
		Providers []struct {
			Name    string `json:"name"`
			Circuit string `json:"circuit"`
			Models  int    `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "ok" || len(out.Providers) == 0 || out.Providers[0].Models == 0 {
		t.Fatalf("fixture drifted from the documented /health shape: %s", res.body)
	}
}

// Degraded and provider-less are independent axes on the real gateway, and an
// earlier version of this fixture conflated them into one 503. Verified live:
// a half-open circuit with live providers still answers 200 "ok"; only an
// empty provider set makes /health non-200.
func TestDegradedIsA200WithAHalfOpenCircuit(t *testing.T) {
	res := get(t, Handler(State{Degraded: true}), "GET", "/health", "")
	if res.status != 200 {
		t.Fatalf("a circuit does not make /health non-200, got %d", res.status)
	}
	if !strings.Contains(string(res.body), "half_open") ||
		!strings.Contains(string(res.body), `"status":"ok"`) {
		t.Fatalf("degraded must report ok with a half-open circuit: %s", res.body)
	}
}

func TestNoProvidersIs503WithAnEmptyProviderSet(t *testing.T) {
	res := get(t, Handler(State{NoProviders: true}), "GET", "/health", "")
	if res.status != 503 {
		t.Fatalf("an empty provider set is the one 503 /health has, got %d", res.status)
	}
	if !strings.Contains(string(res.body), `"no_providers"`) ||
		!strings.Contains(string(res.body), `"providers":[]`) {
		t.Fatalf("no_providers must come with an EMPTY providers array: %s", res.body)
	}
}

// The not-ready body carries only status and reason. Serving the arrays here
// taught ferro status to read target counts that are not there, and report
// "0/0 targets" for a gateway whose targets were merely dead.
func TestNotReadyCarriesOnlyStatusAndReason(t *testing.T) {
	res := get(t, Handler(State{NoProviders: true}), "GET", "/readyz", "")
	if res.status != 503 {
		t.Fatalf("want 503, got %d", res.status)
	}
	// Assert on decoded KEYS, not substrings: the reason text is literally
	// "no routable targets", which contains the very word being banned.
	var doc map[string]any
	if err := json.Unmarshal(res.body, &doc); err != nil {
		t.Fatalf("decode: %v (%s)", err, res.body)
	}
	// doc["reason"] is nil when the key is absent, and nil != "", so comparing
	// against the empty string alone cannot fail. Assert presence and a
	// non-empty string instead.
	reason, ok := doc["reason"].(string)
	if doc["status"] != "not_ready" || !ok || reason == "" {
		t.Fatalf("want status and a non-empty reason: %s", res.body)
	}
	for _, absent := range []string{"targets", "providers", "mcp_servers"} {
		if _, present := doc[absent]; present {
			t.Fatalf("a not_ready body must not carry %q: %s", absent, res.body)
		}
	}
}

func TestAdminRoutesRequireBearer(t *testing.T) {
	h := Handler(Default())
	res := get(t, h, "GET", "/admin/keys", "")
	if res.status != 401 || !strings.Contains(string(res.body), "authentication_error") {
		t.Fatalf("want the documented 401 envelope, got %d %s", res.status, res.body)
	}
	if res = get(t, h, "GET", "/admin/keys", "fgw_test"); res.status != 200 {
		t.Fatalf("valid bearer must be accepted, got %d", res.status)
	}
}

func TestFeatureAbsenceIs501(t *testing.T) {
	res := get(t, Handler(State{NoLogStore: true}), "GET", "/admin/logs", "")
	if res.status != 501 {
		t.Fatalf("absent log store must 501 so the CLI can feature-detect, got %d", res.status)
	}
}

func TestKeysAreStatefulAcrossRequests(t *testing.T) {
	h := Handler(Default()) // one handler, two requests: the store must persist
	res := do(t, h, "POST", "/admin/keys", "fgw_test",
		`{"name":"itest","scopes":["read_only"]}`)
	if res.status != 201 {
		t.Fatalf("create must be 201: %d %s", res.status, res.body)
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(res.body, &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, "fgw_") || len(created.Key) < 20 {
		t.Fatalf("create must return the full secret once, got %q", created.Key)
	}
	list := get(t, h, "GET", "/admin/keys", "fgw_test")
	if !strings.Contains(string(list.body), "itest") {
		t.Fatal("created key must appear in the list — the fake is stateful by design")
	}
	if strings.Contains(string(list.body), created.Key) {
		t.Fatal("list must mask the secret, like the real gateway does")
	}
}

// GET/rotate/revoke/delete had no unit coverage: only cli/itest exercised
// them, and only against a live gateway, skipping entirely when
// FERRO_ITEST_KEY is unset — so a regression in keyStore.rotate/revoke/remove
// could pass CI. This covers the mutating half of the key surface the way
// TestKeysAreStatefulAcrossRequests covers create/list.
func TestKeyLifecycleMutatesTheStore(t *testing.T) {
	h := Handler(Default())

	rotated := do(t, h, http.MethodPost, "/admin/keys/key_01/rotate", "fgw_test", "")
	if rotated.status != http.StatusOK {
		t.Fatalf("rotate must be 200: %d %s", rotated.status, rotated.body)
	}
	var row struct {
		Key       string  `json:"key"`
		RotatedAt *string `json:"rotated_at"`
	}
	if err := json.Unmarshal(rotated.body, &row); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(row.Key, "fgw_") || row.RotatedAt == nil {
		t.Fatalf("rotate must serve the new secret once and stamp rotated_at: %s", rotated.body)
	}
	if listed := get(t, h, http.MethodGet, "/admin/keys/key_01", "fgw_test"); strings.Contains(string(listed.body), row.Key) {
		t.Fatal("a later read must mask the rotated secret")
	}

	if res := do(t, h, http.MethodPost, "/admin/keys/key_02/revoke", "fgw_test", ""); res.status != http.StatusOK {
		t.Fatalf("revoke must be 200: %d %s", res.status, res.body)
	}
	if res := do(t, h, http.MethodDelete, "/admin/keys/key_02", "fgw_test", ""); res.status != http.StatusNoContent {
		t.Fatalf("delete must be 204: %d %s", res.status, res.body)
	}
	res := get(t, h, http.MethodGet, "/admin/keys/key_02", "fgw_test")
	if res.status != http.StatusNotFound || !strings.Contains(string(res.body), "not_found_error") {
		t.Fatalf("a deleted key must 404 in the documented envelope: %d %s", res.status, res.body)
	}
}

// The real gateway defaults an unspecified scope list to read_only, in the key
// store both its backends share. A fake that stored the empty list instead
// would hand every fixture-backed test a scopeless key the gateway never mints.
func TestCreateWithoutScopesDefaultsToReadOnly(t *testing.T) {
	for _, body := range []string{
		`{"name":"absent"}`,
		`{"name":"null","scopes":null}`,
		`{"name":"empty","scopes":[]}`,
	} {
		res := do(t, Handler(Default()), "POST", "/admin/keys", "fgw_test", body)
		if res.status != 201 {
			t.Fatalf("create must be 201: %d %s", res.status, res.body)
		}
		var created struct {
			Scopes []string `json:"scopes"`
		}
		if err := json.Unmarshal(res.body, &created); err != nil {
			t.Fatal(err)
		}
		if len(created.Scopes) != 1 || created.Scopes[0] != "read_only" {
			t.Fatalf("%s must mint a read-only key, got %v", body, created.Scopes)
		}
	}
}

// A real log is read newest first, and the tail's since-cursor advances on that
// order. A seeded row out of order would validate paging against an order the
// gateway cannot produce.
func TestSeededLogIsNewestFirst(t *testing.T) {
	res := get(t, Handler(Default()), "GET", "/admin/logs?stage=all", "fgw_test")
	var page struct {
		Data []struct {
			Stage     string `json:"stage"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) < 2 {
		t.Fatalf("stage=all must expose every seeded row: %s", res.body)
	}
	previous := time.Now().UTC().Add(time.Hour)
	for _, row := range page.Data {
		at, err := time.Parse(time.RFC3339, row.CreatedAt)
		if err != nil {
			t.Fatalf("created_at must be RFC3339: %v", err)
		}
		if at.After(previous) {
			t.Fatalf("row %q at %s breaks descending created_at order: %s",
				row.Stage, row.CreatedAt, res.body)
		}
		previous = at
	}
}

func TestChatStreamFraming(t *testing.T) {
	res := do(t, Handler(Default()), "POST", "/v1/chat/completions", "fgw_test",
		`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if ct := res.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("want SSE content type, got %q", ct)
	}
	if res.header.Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID is how the CLI attributes a stream — it must be set")
	}
	got := string(res.body)
	if !strings.Contains(got, "data: ") || !strings.Contains(got, `"usage"`) ||
		!strings.Contains(got, "data: [DONE]") {
		t.Fatalf("stream must be data-framed, carry a usage chunk, and end with [DONE]:\n%s", got)
	}
}

func TestChatErrorFrameHasNoDone(t *testing.T) {
	// The real gateway ends an errored stream WITHOUT [DONE]; a fake that sends
	// one anyway would hide the CLI bug this behavior exists to catch.
	//
	// The WHOLE stream is read, not its first frame: "there is no [DONE]" is a
	// claim about the end of the stream, and a partial read can only fail to
	// find one that was in fact sent.
	res := do(t, Handler(State{ChatFails: true}), "POST", "/v1/chat/completions", "",
		`{"model":"m","messages":[],"stream":true}`)
	got := string(res.body)
	if !strings.Contains(got, "stream_error") || strings.Contains(got, "[DONE]") {
		t.Fatalf("errored stream must carry stream_error and no [DONE]:\n%s", got)
	}
}

// The real gateway puts /v1/* behind the same bearer check as /admin/*. If the
// fake did not, the CLI's model discovery and playground could be built without
// sending a credential and would 401 on first contact with a real gateway.
func TestV1RoutesRequireBearerToo(t *testing.T) {
	h := Handler(Default())
	// Each route is probed with the METHOD the CLI reaches it by. A handler
	// that authenticated GET /v1/chat/completions but not the POST the
	// playground sends would pass a GET-only loop and 401 in production.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/models", ""},
		{http.MethodPost, "/v1/chat/completions", `{"model":"m","messages":[],"stream":true}`},
	} {
		res := do(t, h, tc.method, tc.path, "", tc.body)
		if res.status != 401 {
			t.Fatalf("%s %s without a bearer must be 401, got %d", tc.method, tc.path, res.status)
		}
		if !strings.Contains(string(res.body), "authentication_error") {
			t.Fatalf("%s %s must use the documented error envelope: %s", tc.method, tc.path, res.body)
		}
	}
	if res := get(t, h, "GET", "/v1/models", "fgw_test"); res.status != 200 {
		t.Fatalf("valid bearer must be accepted on /v1/models, got %d", res.status)
	}
}

// ...while the liveness surface stays open, as it is on the real router.
func TestHealthSurfaceStaysUnauthenticated(t *testing.T) {
	h := Handler(Default())
	for _, path := range []string{"/health", "/readyz"} {
		if res := get(t, h, "GET", path, ""); res.status == 401 {
			t.Fatalf("%s must not require a credential", path)
		}
	}
	res := get(t, h, "GET", "/livez", "")
	if res.status != http.StatusOK || strings.TrimSpace(string(res.body)) != `{"status":"ok"}` {
		t.Fatalf("/livez must be an open 200 status payload, got %d %s", res.status, res.body)
	}
}

func TestChatRequiresStreaming(t *testing.T) {
	h := Handler(Default())
	for _, body := range []string{
		`{"model":"m"}`,
		`{"model":"m","stream":false}`,
		`{"model":"m","stream":true}{"stream":true}`,
		`{"stream":false,"stream":true}`,
	} {
		res := do(t, h, http.MethodPost, "/v1/chat/completions", "fgw_test", body)
		if res.status != http.StatusBadRequest {
			t.Fatalf("non-streaming request must be rejected, got %d", res.status)
		}
	}
}

func TestAuditFiltersAndPagesRows(t *testing.T) {
	h := Handler(Default())
	res := get(t, h, "GET", "/admin/audit?outcome=ok&limit=1&offset=1", "fgw_test")
	if res.status != http.StatusOK {
		t.Fatalf("audit query failed: %d %s", res.status, res.body)
	}
	var page struct {
		Data    []map[string]any `json:"data"`
		Summary struct {
			TotalEntries int `json:"total_entries"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(res.body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Summary.TotalEntries != 2 || len(page.Data) != 1 ||
		page.Data[0]["outcome"] != "ok" || page.Data[0]["action"] != "key.revoke" {
		t.Fatalf("filters must apply before paging: %s", res.body)
	}
}

func TestAuditRejectsInvalidSince(t *testing.T) {
	res := get(t, Handler(Default()), "GET", "/admin/audit?since=not-a-time", "fgw_test")
	if res.status != http.StatusBadRequest ||
		!strings.Contains(string(res.body), `"type":"invalid_request_error"`) {
		t.Fatalf("invalid since must use the invalid-request envelope: %d %s", res.status, res.body)
	}
}

// The real gateway's parseLimit 400s a limit that is <= 0 or not an integer
// (internal/admin/handlers/queryparams.go); an earlier version of this fixture
// silently fell back to its default and served an empty page instead. Both
// endpoints share the page[T] helper, so one table covers both.
func TestNonPositiveLimitIs400OnLogsAndAudit(t *testing.T) {
	for _, path := range []string{
		"/admin/logs?limit=0",
		"/admin/logs?limit=-1",
		"/admin/logs?limit=abc",
		"/admin/audit?limit=0",
		"/admin/audit?limit=-1",
		"/admin/audit?limit=abc",
	} {
		res := get(t, Handler(Default()), "GET", path, "fgw_test")
		if res.status != http.StatusBadRequest ||
			!strings.Contains(string(res.body), `"type":"invalid_request_error"`) {
			t.Fatalf("%s: want the documented 400 envelope for a non-positive limit, got %d %s", path, res.status, res.body)
		}
	}
}

// parseOffset rejects the same two shapes parseLimit does, one bound over.
func TestNegativeOffsetIs400OnLogsAndAudit(t *testing.T) {
	for _, path := range []string{
		"/admin/logs?offset=-1",
		"/admin/logs?offset=abc",
		"/admin/audit?offset=-1",
		"/admin/audit?offset=abc",
	} {
		res := get(t, Handler(Default()), "GET", path, "fgw_test")
		if res.status != http.StatusBadRequest ||
			!strings.Contains(string(res.body), `"type":"invalid_request_error"`) {
			t.Fatalf("%s: want the documented 400 envelope for a bad offset, got %d %s", path, res.status, res.body)
		}
	}
}

// A present-but-empty bound is the one shape that must NOT 400: parseLimit and
// parseOffset both read through url.Values.Get and return their default on "",
// so `?limit=` is the same request as no limit at all. A fixture that rejected
// it would send someone hunting a CLI bug that does not exist.
func TestEmptyBoundIsReadAsAbsentNotRejected(t *testing.T) {
	for _, path := range []string{
		"/admin/logs?limit=",
		"/admin/logs?offset=",
		"/admin/logs?limit=&offset=",
		"/admin/audit?limit=",
		"/admin/audit?offset=",
	} {
		res := get(t, Handler(Default()), "GET", path, "fgw_test")
		if res.status != http.StatusOK {
			t.Fatalf("%s: an empty bound means absent, want 200, got %d %s", path, res.status, res.body)
		}
	}
}

// The real stats endpoint 400s a malformed since exactly as /admin/logs and
// /admin/audit do (internal/admin/handlers/queryparams.go's parseSince is
// shared by all three) — this fixture used to accept anything on
// /admin/logs/stats because it never looked at the query at all.
func TestMalformedSinceIs400OnLogsAndStats(t *testing.T) {
	for _, path := range []string{
		"/admin/logs?since=not-a-time",
		"/admin/logs/stats?since=not-a-time",
	} {
		res := get(t, Handler(Default()), "GET", path, "fgw_test")
		if res.status != http.StatusBadRequest ||
			!strings.Contains(string(res.body), `"type":"invalid_request_error"`) {
			t.Fatalf("%s: want the documented 400 envelope for a malformed since, got %d %s", path, res.status, res.body)
		}
	}
}

type statsSummary struct {
	Summary struct {
		TotalEntries int `json:"total_entries"`
	} `json:"summary"`
	ByProvider map[string]json.RawMessage `json:"by_provider"`
}

func decodeStats(t *testing.T, path string) statsSummary {
	t.Helper()
	res := get(t, Handler(Default()), "GET", path, "fgw_test")
	if res.status != http.StatusOK {
		t.Fatalf("%s: want 200, got %d %s", path, res.status, res.body)
	}
	var out statsSummary
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

// Before this fix /admin/logs/stats served the same fixed aggregate no matter
// what the query asked for, so a CLI regression that dropped a filter before
// it reached the wire was invisible against this fixture. Each of these
// narrows a five-row seed a different way, and the unfiltered count (5) proves
// the baseline itself is real data, not a coincidence of one filter.
func TestStatsFiltersNarrowTheAggregate(t *testing.T) {
	base := decodeStats(t, "/admin/logs/stats")
	if base.Summary.TotalEntries != 5 {
		t.Fatalf("unfiltered stats: want all 5 seeded rows, got %d", base.Summary.TotalEntries)
	}
	if _, ok := base.ByProvider["anthropic"]; !ok {
		t.Fatalf("unfiltered by_provider must include anthropic: %+v", base.ByProvider)
	}
	if _, ok := base.ByProvider["openai"]; !ok {
		t.Fatalf("unfiltered by_provider must include openai: %+v", base.ByProvider)
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/admin/logs/stats?provider=openai", 2},
		{"/admin/logs/stats?stage=before_request", 1},
		{"/admin/logs/stats?model=does-not-exist", 0},
	} {
		got := decodeStats(t, tc.path)
		if got.Summary.TotalEntries != tc.want {
			t.Fatalf("%s: want %d entries, got %d (a fixture that dropped this filter would still report %d)",
				tc.path, tc.want, got.Summary.TotalEntries, base.Summary.TotalEntries)
		}
	}

	// provider=openai must also drop anthropic out of the by_provider
	// breakdown, not just shrink the total.
	openaiOnly := decodeStats(t, "/admin/logs/stats?provider=openai")
	if _, ok := openaiOnly.ByProvider["anthropic"]; ok {
		t.Fatalf("provider=openai must drop anthropic from by_provider: %+v", openaiOnly.ByProvider)
	}
}

// A present-but-empty filter value means absent, the same rule the other
// admin list routes apply (TestEmptyBoundIsReadAsAbsentNotRejected): it must
// not 400, and — because /admin/logs/stats used to ignore every parameter —
// it must not silently narrow the aggregate either.
func TestStatsEmptyFilterValueIsAbsentNotRejected(t *testing.T) {
	for _, path := range []string{
		"/admin/logs/stats?model=",
		"/admin/logs/stats?provider=",
		"/admin/logs/stats?stage=",
		"/admin/logs/stats?since=",
	} {
		got := decodeStats(t, path)
		if got.Summary.TotalEntries != 5 {
			t.Fatalf("%s: an empty value means absent, want all 5 seeded rows, got %d", path, got.Summary.TotalEntries)
		}
	}
}

// since keeps only rows at or after the cursor (internal/requestlog/store.go's
// SQLWriter.Stats: "created_at >= ?"), so a cursor five minutes back must drop
// exactly the row seeded nine minutes back and keep the other four.
func TestStatsSinceKeepsOnlyRowsAtOrAfterTheCursor(t *testing.T) {
	since := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	got := decodeStats(t, "/admin/logs/stats?since="+since)
	if got.Summary.TotalEntries != 4 {
		t.Fatalf("since=-5m must drop the row seeded 9 minutes ago, want 4 entries, got %d", got.Summary.TotalEntries)
	}
}

// model.ValidateScopes refuses anything outside the closed set with a 400, so
// the fixture must too — a fake that accepts "readonly" lets a CLI that sends
// the wrong spelling pass every test here and fail against a real gateway.
func TestCreateRejectsAnUnknownScope(t *testing.T) {
	h := Handler(Default())
	res := do(t, h, http.MethodPost, "/admin/keys", "fgw_test",
		`{"name":"typo","scopes":["readonly"]}`)
	if res.status != http.StatusBadRequest {
		t.Fatalf("an unknown scope must be 400, got %d %s", res.status, res.body)
	}
	for _, want := range []string{`unknown scope \"readonly\"`, "admin, read_only", "invalid_request_error"} {
		if !strings.Contains(string(res.body), want) {
			t.Fatalf("want %s in the gateway's own wording: %s", want, res.body)
		}
	}
	// The refusal must not have created anything.
	if list := get(t, h, http.MethodGet, "/admin/keys", "fgw_test"); strings.Contains(string(list.body), "typo") {
		t.Fatalf("a refused create must not reach the store: %s", list.body)
	}
}

// Both members of the closed set are accepted, so the guard above cannot pass
// by rejecting everything.
func TestCreateAcceptsBothValidScopes(t *testing.T) {
	h := Handler(Default())
	for _, scope := range []string{scopeAdmin, scopeReadOnly} {
		res := do(t, h, http.MethodPost, "/admin/keys", "fgw_test",
			`{"name":"ok-`+scope+`","scopes":["`+scope+`"]}`)
		if res.status != http.StatusCreated {
			t.Fatalf("%s must be accepted, got %d %s", scope, res.status, res.body)
		}
	}
}

// An expired key is served the way the gateway serves one: active:true,
// revoked_at:null, expires_at in the past. Seeding active:false here was
// fixture-only fiction that hid a real CLI bug.
func TestExpiredKeyKeepsActiveTrueOnTheWire(t *testing.T) {
	res := get(t, Handler(Default()), http.MethodGet, "/admin/keys", "fgw_test")
	var rows []struct {
		Name      string  `json:"name"`
		RevokedAt *string `json:"revoked_at"`
		ExpiresAt *string `json:"expires_at"`
		Active    bool    `json:"active"`
	}
	if err := json.Unmarshal(res.body, &rows); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.Name != "demo-temp" {
			continue
		}
		found = true
		if !r.Active || r.RevokedAt != nil || r.ExpiresAt == nil {
			t.Fatalf("an expired key stays active:true with revoked_at:null and an expires_at: %+v", r)
		}
		expiry, err := time.Parse(time.RFC3339, *r.ExpiresAt)
		if err != nil {
			t.Fatal(err)
		}
		if !time.Now().After(expiry) {
			t.Fatalf("the seeded expiry must already have passed, got %s", *r.ExpiresAt)
		}
	}
	if !found {
		t.Fatal("the expired fixture key must be seeded")
	}
}
