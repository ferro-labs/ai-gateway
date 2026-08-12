package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cnt reads a supplied count, and answers -1 for one the gateway never gave —
// a value no real count can take, so "absent" can never satisfy an assertion
// about a number.
func cnt(n *int) int {
	if n == nil {
		return -1
	}
	return *n
}

func TestStatusConnectedWithAdmin(t *testing.T) {
	srv := gatewayStub(t, true)
	r, err := testClient(t, srv.URL, "fgw_ok").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A half-open circuit and an unroutable target both degrade the report.
	if r.State != "degraded" || cnt(r.Providers) != 2 || cnt(r.Models) != 2 || r.Targets == nil ||
		r.Targets.Total != 2 || r.Targets.Routable != 1 ||
		r.Circuits.HalfOpen != 1 || r.Circuits.Open != 0 ||
		r.MCP == nil || r.MCP.Ready != 1 || r.MCP.Total != 2 ||
		r.Auth != "admin" || len(r.Warnings) == 0 {
		t.Fatalf("bad report: %+v", r)
	}
	if r.URL != strings.TrimRight(srv.URL, "/") {
		t.Fatalf("report must name the gateway: %q", r.URL)
	}
	if !strings.Contains(strings.Join(r.Warnings, ";"), "openai") {
		t.Fatalf("the half-open provider must be named: %v", r.Warnings)
	}
	if r.LatencyMs < 0 {
		t.Fatal("latency must be measured")
	}
}

func TestStatusUnauthorizedStillReports(t *testing.T) {
	srv := gatewayStub(t, true)
	// No key: /health and /readyz still answer; admin and models do not.
	r, err := testClient(t, srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Models is nil, not 0: without an accepted credential /v1/models is never
	// asked, and reporting zero would state a fact the CLI does not have.
	if r.Auth != "unauthorized" || r.Models != nil || cnt(r.Providers) != 2 {
		t.Fatalf("unauth status must degrade gracefully: %+v", r)
	}
	if r.Targets == nil || r.Targets.Total != 2 || r.MCP == nil {
		t.Fatalf("unauthenticated ground truth must still be read: %+v", r)
	}
}

func TestStatusUnreachable(t *testing.T) {
	c := testClient(t, "http://127.0.0.1:1", "")
	r, err := c.Status(context.Background())
	if err == nil || r == nil || r.State != "unreachable" {
		t.Fatalf("want unreachable report + error, got %+v, %v", r, err)
	}
	if r.URL == "" {
		t.Fatal("an unreachable report still names the URL it tried")
	}
}

func TestStatusDegradedOn503(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusServiceUnavailable, `{"status":"no_providers","providers":[]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusServiceUnavailable, `{"status":"not_ready","reason":"no routable targets"}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"no_providers","providers":[],"components":[],"scopes":["read_only"]}`)
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"object":"list","data":[]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := testClient(t, srv.URL, "fgw_ok").Status(context.Background())
	if err != nil {
		t.Fatalf("503 is a report, not a failure: %v", err)
	}
	joined := strings.Join(r.Warnings, ";")
	if r.State != "degraded" || !strings.Contains(joined, "no_providers") || !strings.Contains(joined, "no routable targets") {
		t.Fatalf("bad 503 report: %+v", r)
	}
	if r.Auth != "read_only" {
		t.Fatalf("scope must be reported: %+v", r)
	}
}

func TestStatusConnectedWhenAllHealthy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ok","providers":[{"name":"openai","status":"available","circuit":"closed","models":3}]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ready","targets":[{"name":"openai-primary","routable":true}]}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"healthy","providers":[],"components":[]}`)
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"object":"list","data":[{"id":"gpt-5.1","object":"model","created":0,"owned_by":"openai"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := testClient(t, srv.URL, "fgw_ok").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "connected" || len(r.Warnings) != 0 || r.MCP != nil {
		t.Fatalf("healthy gateway must report connected with no warnings: %+v", r)
	}
	// No scopes in the body: authenticated, but the scope is unknown — models
	// are still enriched because the probe did not fail.
	if r.Auth != "" || cnt(r.Models) != 1 {
		t.Fatalf("scope-less admin health: %+v", r)
	}
}

func TestStatusDegradedWhenReadinessCannotBeRead(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ok","providers":[]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{not-json`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, `{"error":{"message":"nope"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := testClient(t, srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "degraded" || !strings.Contains(strings.Join(r.Warnings, ";"), "readiness") {
		t.Fatalf("a failed readiness probe must degrade the report: %+v", r)
	}
}

func TestStatusPreservesReportedEmptyTargets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ok","providers":[]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ready","targets":[]}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, `{"error":{"message":"nope"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := testClient(t, srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Targets == nil || r.Targets.Total != 0 {
		t.Fatalf("a reported empty target list is known zero, not absent: %+v", r.Targets)
	}
}

func TestStatusMeasuresRealLatency(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(25 * time.Millisecond)
		writeJSON(t, w, 200, `{"status":"ok","providers":[]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ready"}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, `{"error":{"message":"nope","type":"authentication_error"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := testClient(t, srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.LatencyMs < 20 {
		t.Fatalf("latency must be the measured /health RTT, got %dms", r.LatencyMs)
	}
}

// healthyExceptAdmin serves a gateway whose unauthenticated ground truth is
// perfect, so nothing but the admin probe can degrade the report.
func healthyExceptAdmin(t *testing.T, adminStatus int, adminBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ok","providers":[{"name":"openai","status":"available","circuit":"closed","models":3}]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ready","targets":[{"name":"openai-primary","routable":true}]}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, adminStatus, adminBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatusDegradesWhenCredentialIsRejected(t *testing.T) {
	srv := healthyExceptAdmin(t, http.StatusUnauthorized, `{"error":{"message":"key expired"}}`)

	// A credential was presented and refused: healthy probes must not hide it.
	r, err := testClient(t, srv.URL, "fgw_expired").Status(context.Background())
	if err != nil {
		t.Fatalf("an auth failure degrades, it never fails: %v", err)
	}
	if r.State != StateDegraded || r.Auth != AuthUnauthorized {
		t.Fatalf("a rejected credential must degrade: %+v", r)
	}
	if !strings.Contains(strings.Join(r.Warnings, ";"), "credential rejected") {
		t.Fatalf("the report must say why it degraded: %v", r.Warnings)
	}

	// No credential at all is not a fault — nothing was rejected.
	r, err = testClient(t, srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateConnected || r.Auth != AuthUnauthorized || len(r.Warnings) != 0 {
		t.Fatalf("an unauthenticated probe must report connected: %+v", r)
	}
}

func TestStatusWarnsWhenAdminProbeFailsForANonAuthReason(t *testing.T) {
	srv := healthyExceptAdmin(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)

	r, err := testClient(t, srv.URL, "fgw_ok").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Without the warning a broken probe is indistinguishable from a gateway
	// that authenticated fine and serves no models.
	if !strings.Contains(strings.Join(r.Warnings, ";"), "admin health unavailable") {
		t.Fatalf("a non-auth probe failure must be visible: %+v", r)
	}
	if r.Auth != "" {
		t.Fatalf("a 500 says nothing about the credential: %+v", r)
	}
}

func TestStatusPreservesEmptyMCPAndSelectsKnownScope(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ok","providers":[]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ready","targets":[],"mcp_servers":[]}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"healthy","providers":[],"components":[],"scopes":["custom","read_only","admin"]}`)
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, `{"error":{"message":"catalog failed"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := testClient(t, srv.URL, "fgw_ok").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.MCP == nil || r.MCP.Total != 0 || r.Auth != "admin" {
		t.Fatalf("empty-present MCP and scope precedence were lost: %+v", r)
	}
	if !strings.Contains(strings.Join(r.Warnings, ";"), "models unavailable") {
		t.Fatalf("model enrichment failure must be visible: %v", r.Warnings)
	}
}

// A gateway that answers zero and one that never answered are different facts.
// Before Providers/Models became pointers both rendered as a real 0, so a
// /health that failed on a still-reachable gateway reported "0 providers" —
// and the status table printed "0" for providers beside "-" for models under
// exactly the same condition.
func TestSuppliedZeroIsNotAbsent(t *testing.T) {
	mux := http.NewServeMux()
	// /health answers, and honestly reports no providers at all.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ok","providers":[]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ready","targets":[]}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"healthy","providers":[],"components":[],"scopes":["admin"]}`)
	})
	// /v1/models is not served by this build at all — never answered.
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotImplemented, `{"error":{"message":"not supported"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := testClient(t, srv.URL, "fgw_ok").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Providers == nil || *r.Providers != 0 {
		t.Fatalf("a reported empty provider list is a known zero, not absent: %v", cnt(r.Providers))
	}
	if r.Models != nil {
		t.Fatalf("an endpoint this build does not serve leaves the count absent, got %v", cnt(r.Models))
	}
}

// The frozen --format json contract: an absent count is omitted entirely
// rather than serialized as 0, so a consumer can tell the two apart too.
func TestAbsentCountIsOmittedFromJSON(t *testing.T) {
	zero := 0
	body, err := json.Marshal(&StatusReport{State: "degraded", Providers: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"providers":0`) {
		t.Fatalf("a supplied zero must serialize as 0: %s", body)
	}
	if strings.Contains(string(body), `"models"`) {
		t.Fatalf("an absent count must be omitted, not sent as 0: %s", body)
	}
}
