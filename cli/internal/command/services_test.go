package command

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/fixture"
)

func TestModelsTable(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "models")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"ID", "OWNED BY", "MODE", "CONTEXT", "CAPABILITIES", "STATUS"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	if !strings.Contains(stdout, "claude-sonnet-4-6") || !strings.Contains(stdout, "anthropic") {
		t.Fatalf("model rows missing: %q", stdout)
	}
}

func TestModelsJSONIsOneDocument(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "models", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := decodeOne(stdout, &rows); err != nil {
		t.Fatalf("stdout must be one JSON document: %v (%q)", err, stdout)
	}
	if len(rows) == 0 {
		t.Fatalf("expected models: %q", stdout)
	}
}

func TestProvidersMergesCircuitFromHealth(t *testing.T) {
	s := fixture.Default()
	s.Degraded = true
	srv := gateway(t, s)

	stdout, _, err := run(t, srv, "providers")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"PROVIDER", "STATUS", "CIRCUIT", "MODELS", "MESSAGE"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	// Circuit comes from /health, status/models/message from /admin/health:
	// neither endpoint alone can fill this table.
	if !strings.Contains(stdout, "half_open") {
		t.Fatalf("circuit must come from /health: %q", stdout)
	}
	if !strings.Contains(stdout, "circuit half_open after") {
		t.Fatalf("message must come from /admin/health: %q", stdout)
	}
}

// Unauthenticated is a smaller answer, not an error: /health alone still names
// every provider and its circuit.
func TestMergeProvidersFallsBackToHealthAlone(t *testing.T) {
	h := &api.HealthReport{Providers: []api.ProviderHealth{
		{Name: "anthropic", Status: "available", Circuit: "open", Models: 412},
	}}
	rows := mergeProviders(h, nil)
	if len(rows) != 1 || rows[0].Circuit != "open" || rows[0].Models != 412 {
		t.Fatalf("health-only fallback lost data: %+v", rows)
	}
	if rows[0].Message != "" {
		t.Fatalf("there is no message without /admin/health: %+v", rows)
	}
}

func TestMergeProvidersKeepsHealthOnlyRowsAndNumericCounts(t *testing.T) {
	h := &api.HealthReport{Providers: []api.ProviderHealth{
		{Name: "openai", Status: "available", Circuit: "closed", Models: 10},
		{Name: "fallback", Status: "available", Circuit: "open", Models: 2},
	}}
	ah := &api.AdminHealth{Providers: []api.AdminProviderHealth{{Name: "openai", Status: "healthy", Models: 10}}}
	rows := mergeProviders(h, ah)
	if len(rows) != 2 || rows[1].Name != "fallback" || rows[1].Models != 2 {
		t.Fatalf("admin health omissions must not hide /health providers: %+v", rows)
	}
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "providers", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc []map[string]any
	if err := decodeOne(stdout, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) == 0 {
		t.Fatal("expected provider rows")
	}
	if _, ok := doc[0]["models"].(float64); !ok {
		t.Fatalf("models must be a JSON number, got %T (%v)", doc[0]["models"], doc[0]["models"])
	}
}

func TestPluginsMergeShowsFailsOpenFromCatalog(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "plugins")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"NAME", "TYPE", "ENABLED", "FAILS", "SUMMARY"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	// fails_open lives only in the catalog, so its presence proves the merge.
	if !strings.Contains(stdout, "open") || !strings.Contains(stdout, "closed") {
		t.Fatalf("FAILS column must be merged from the catalog: %q", stdout)
	}
	if !strings.Contains(stdout, "Records one row per request stage") {
		t.Fatalf("SUMMARY must be merged from the catalog: %q", stdout)
	}
}

// A plugin the build does not ship (registered out of tree) still lists, with
// the catalog-only columns blank rather than guessed.
func TestMergePluginsKeepsUncatalogedPlugins(t *testing.T) {
	rows := mergePlugins(
		[]api.PluginInfo{{Name: "vendor-thing", Type: "guardrail", Enabled: true}},
		[]api.BuiltinPlugin{{Name: "budget", Type: "guardrail", FailsOpen: false}},
	)
	if len(rows) != 1 || rows[0].Name != "vendor-thing" {
		t.Fatalf("configured plugins drive the listing: %+v", rows)
	}
	if rows[0].Fails != "-" || rows[0].Summary != "" {
		t.Fatalf("unknown fail policy must not be guessed: %+v", rows)
	}
}

func TestMCPTable(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"NAME", "READY", "REQUIRED", "LAST ERROR"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	if !strings.Contains(stdout, "filesystem") {
		t.Fatalf("mcp rows missing: %q", stdout)
	}
}

func TestServicesAggregateIsReachable(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "services")
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"MCP servers", "Plugins", "Sessions", "Audit"} {
		if !strings.Contains(stdout, service) {
			t.Fatalf("services output missing %q: %s", service, stdout)
		}
	}
}

func TestServicesMarksUnsupportedComponentWithoutDroppingOthers(t *testing.T) {
	s := fixture.Default()
	s.NoSessions = true
	srv := gateway(t, s)
	stdout, _, err := run(t, srv, "services", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []serviceSummary
	if err := decodeOne(stdout, &rows); err != nil {
		t.Fatalf("stdout must be one JSON document: %v (%q)", err, stdout)
	}
	if len(rows) != 4 {
		t.Fatalf("one unsupported component must not hide the others: %+v", rows)
	}
	for _, row := range rows {
		if row.Name == "Sessions" && row.State != "unsupported" {
			t.Fatalf("unsupported sessions must not be reported as a healthy zero: %+v", rows)
		}
	}
}

// /admin/health carries last_error but needs a credential; /readyz carries the
// same servers unauthenticated. A bad key degrades to the smaller answer.
func TestMCPFallsBackToReadyzWithoutCredential(t *testing.T) {
	srv := gateway(t, fixture.Default())
	t.Setenv("FERRO_API_KEY", "fgw_wrong")
	stdout, stderr, err := execute(t, "mcp", "--gateway-url", srv.URL)
	if err != nil {
		t.Fatalf("an unauthorized caller still gets the readyz view: %v", err)
	}
	if !strings.Contains(stdout, "filesystem") {
		t.Fatalf("fallback lost the rows: %q", stdout)
	}
	if !strings.Contains(stderr, "readyz") {
		t.Fatalf("the degraded source must be stated on stderr: %q", stderr)
	}
}

func TestSessionsTable(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "sessions")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"SUBJECT", "SCOPES", "CREATED", "LAST SEEN", "EXPIRES"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	if !strings.Contains(stdout, "ops-laptop") {
		t.Fatalf("session rows missing: %q", stdout)
	}
	// ci-pipeline has never been seen. Only its LAST SEEN cell may be a dash:
	// a "-" anywhere in the table would also be satisfied by a zero-time
	// 0001-01-01 in that cell, or by every cell collapsing to a dash.
	var row string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "ci-pipeline") {
			row = l
		}
	}
	if row == "" {
		t.Fatalf("expected the seeded ci-pipeline session row: %q", stdout)
	}
	// SUBJECT SCOPES CREATED LAST-SEEN EXPIRES — every cell is whitespace-free,
	// so the table still splits into fields for anything piping it.
	fields := strings.Fields(row)
	if len(fields) != 5 {
		t.Fatalf("want 5 whitespace-free columns, got %d in %q", len(fields), row)
	}
	if fields[3] != "-" {
		t.Fatalf("a null last_seen_at must render as -, got %q in %q", fields[3], row)
	}
	if fields[2] == "-" || fields[4] == "-" {
		t.Fatalf("created_at and expires_at are set on this session and must render as dates: %q", row)
	}
}

func TestSessionsDisabledDegradesWithAHint(t *testing.T) {
	s := fixture.Default()
	s.NoSessions = true
	srv := gateway(t, s)

	_, _, err := run(t, srv, "sessions")
	if err == nil || !strings.Contains(err.Error(), "sessions") {
		t.Fatalf("a 501 must be reported as a disabled feature: %v", err)
	}
	if strings.Contains(err.Error(), "501") && !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("the hint must lead, not the status code: %v", err)
	}
}

func TestAuditTableAndFilters(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "audit", "--outcome", "ok", "--limit", "10")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"TIME", "ACTION", "ACTOR", "OUTCOME", "TARGET"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	if !strings.Contains(stdout, "key.create") {
		t.Fatalf("audit rows missing: %q", stdout)
	}
}

// The gateway answers 400 for an outcome outside its vocabulary. Refusing it
// here spends no request and gives the same vocabulary back.
func TestAuditRejectsBadOutcomeClientSide(t *testing.T) {
	_, _, err := execute(t, "audit", "--outcome", "bogus", "--gateway-url", "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("--outcome must be validated client-side against ok|denied|error: %v", err)
	}
}

// A dash is a rendering of absence, not a value the gateway sent. Every field
// of providerRow must therefore stay literal in --format json, and be dashed
// only where the table is drawn. circuit briefly broke that rule on its own,
// so one document carried "-" for an absent circuit beside an empty string
// for an absent status — two conventions in one object.
func TestProvidersJSONKeepsAbsenceLiteral(t *testing.T) {
	h := &api.HealthReport{Providers: []api.ProviderHealth{
		{Name: "anthropic", Status: "available", Models: 412}, // no circuit reported
	}}
	rows := mergeProviders(h, nil)
	if len(rows) != 1 {
		t.Fatalf("want one row, got %d", len(rows))
	}
	if rows[0].Circuit != "" {
		t.Fatalf("an absent circuit stays empty in the structured row, got %q", rows[0].Circuit)
	}

	body, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"-"`) {
		t.Fatalf("no field may serialize a table dash: %s", body)
	}
	if !strings.Contains(string(body), `"circuit":""`) {
		t.Fatalf("circuit must serialize as the empty string it is: %s", body)
	}
}

// "No admin credential accepted" is a specific claim. A gateway that times out
// or 500s on /admin/health costs the same two columns, but says so — telling
// the operator to check a credential that was never the problem is a wrong
// answer, and the one they would act on first.
func TestProvidersNamesWhyAdminHealthWasUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
		absent string
	}{
		{"refused credential", http.StatusUnauthorized, `{"error":{"message":"nope"}}`,
			"no admin credential accepted", "admin health unavailable"},
		{"broken gateway", http.StatusInternalServerError, `{"error":{"message":"boom"}}`,
			"admin health unavailable", "no admin credential accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok","providers":[{"name":"openai","status":"available","circuit":"closed","models":3}]}`))
			})
			mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			_, stderr, err := run(t, srv, "providers")
			if err != nil {
				t.Fatalf("an unusable admin probe costs columns, not the command: %v", err)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("want %q in the warning, got %q", tc.want, stderr)
			}
			if strings.Contains(stderr, tc.absent) {
				t.Fatalf("must not claim %q for a %d, got %q", tc.absent, tc.status, stderr)
			}
		})
	}
}
