package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testClient builds a Client against a stub, failing the test on a bad URL.
func testClient(t *testing.T, base, key string) *Client {
	t.Helper()
	c, err := New(base, key)
	if err != nil {
		t.Fatalf("New(%q): %v", base, err)
	}
	return c
}

// writeJSON is the stub-side body writer; status 0 means 200.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("stub write: %v", err)
	}
}

// gatewayStub is the healthy-gateway fixture shared by the endpoint and status
// tests. admin=false makes /admin/health answer 401 for every caller.
func gatewayStub(t *testing.T, admin bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ok","providers":[
			{"name":"anthropic","status":"available","circuit":"closed","models":412},
			{"name":"openai","status":"available","circuit":"half_open","models":1104}]}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"status":"ready",
			"providers":[{"name":"anthropic","circuit":"closed"}],
			"targets":[{"name":"anthropic-primary","routable":true},{"name":"openai-primary","routable":false}],
			"mcp_servers":[{"name":"filesystem","ready":true,"required":false},{"name":"search","ready":false,"required":false}]}`)
	})
	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, r *http.Request) {
		if !admin || r.Header.Get("Authorization") != "Bearer fgw_ok" {
			writeJSON(t, w, http.StatusUnauthorized,
				`{"error":{"message":"unauthorized","type":"authentication_error","code":"unauthorized"}}`)
			return
		}
		writeJSON(t, w, 200, `{"status":"healthy",
			"providers":[{"name":"anthropic","status":"healthy","models":412},
				{"name":"openai","status":"degraded","models":1104,"message":"rate limited"}],
			"components":[{"name":"API","status":"healthy"}],
			"mcp_servers":[{"name":"search","ready":false,"required":false,"last_error":"dial tcp: refused"}],
			"scopes":["admin"]}`)
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			writeJSON(t, w, http.StatusUnauthorized, `{"error":{"message":"unauthorized","type":"authentication_error"}}`)
			return
		}
		writeJSON(t, w, 200, `{"object":"list","data":[
			{"id":"claude-sonnet-4-6","object":"model","created":0,"owned_by":"anthropic","capabilities":["streaming","vision"],"context_window":200000},
			{"id":"gpt-5.1","object":"model","created":0,"owned_by":"openai"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthDecodesAndPath(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeJSON(t, w, 200, `{"status":"ok","providers":[{"name":"openai","status":"available","circuit":"open","models":7}]}`)
	}))
	defer srv.Close()

	h, status, err := testClient(t, srv.URL, "").Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/health" {
		t.Fatalf("want GET /health, got %q", path)
	}
	if status != 200 || h.Status != "ok" || len(h.Providers) != 1 ||
		h.Providers[0].Circuit != "open" || h.Providers[0].Models != 7 {
		t.Fatalf("bad decode: status=%d %+v", status, h)
	}
}

func TestHealthTolerates503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusServiceUnavailable, `{"status":"no_providers","providers":[]}`)
	}))
	defer srv.Close()

	h, status, err := testClient(t, srv.URL, "").Health(context.Background())
	if err != nil {
		t.Fatalf("503 must decode, not error: %v", err)
	}
	if status != http.StatusServiceUnavailable || h.Status != "no_providers" {
		t.Fatalf("bad tolerated 503: status=%d %+v", status, h)
	}
}

func TestReadyDecodesNotReady503(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeJSON(t, w, http.StatusServiceUnavailable, `{"status":"not_ready","reason":"no routable targets"}`)
	}))
	defer srv.Close()

	rep, status, err := testClient(t, srv.URL, "").Ready(context.Background())
	if err != nil {
		t.Fatalf("503 readyz must decode: %v", err)
	}
	if path != "/readyz" {
		t.Fatalf("want GET /readyz, got %q", path)
	}
	if status != http.StatusServiceUnavailable || rep.Status != "not_ready" || rep.Reason != "no routable targets" {
		t.Fatalf("bad decode: status=%d %+v", status, rep)
	}
	// Optional collections stay nil rather than becoming empty non-nil slices.
	if rep.Targets != nil || rep.MCPServers != nil || rep.Providers != nil {
		t.Fatalf("absent optional fields must stay nil: %+v", rep)
	}
}

func TestReadyDecodesFullBody(t *testing.T) {
	srv := gatewayStub(t, true)
	rep, status, err := testClient(t, srv.URL, "").Ready(context.Background())
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if len(rep.Targets) != 2 || rep.Targets[0].Name != "anthropic-primary" || !rep.Targets[0].Routable ||
		rep.Targets[1].Routable {
		t.Fatalf("targets decode: %+v", rep.Targets)
	}
	if len(rep.MCPServers) != 2 || !rep.MCPServers[0].Ready || rep.MCPServers[1].Ready {
		t.Fatalf("mcp decode: %+v", rep.MCPServers)
	}
	if len(rep.Providers) != 1 || rep.Providers[0].Circuit != "closed" {
		t.Fatalf("providers decode: %+v", rep.Providers)
	}
}

func TestAdminHealthDecodesAndPropagates401(t *testing.T) {
	srv := gatewayStub(t, true)

	ah, err := testClient(t, srv.URL, "fgw_ok").AdminHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ah.Status != "healthy" || len(ah.Providers) != 2 || ah.Providers[1].Message != "rate limited" ||
		len(ah.Components) != 1 || len(ah.Scopes) != 1 || ah.Scopes[0] != "admin" {
		t.Fatalf("bad decode: %+v", ah)
	}
	if len(ah.MCPServers) != 1 || ah.MCPServers[0].LastError == "" {
		t.Fatalf("admin mcp servers carry last_error: %+v", ah.MCPServers)
	}

	_, err = testClient(t, srv.URL, "fgw_bad").AdminHealth(context.Background())
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Fatalf("401 must propagate as api.Error: %v", err)
	}
}

func TestModelsUnwrapsEnvelope(t *testing.T) {
	var path string
	srv := gatewayStub(t, true)
	c := testClient(t, srv.URL, "fgw_ok")
	c.hc.Transport = pathRecorder{&path}

	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/models" {
		t.Fatalf("want GET /v1/models, got %q", path)
	}
	if len(models) != 2 || models[0].ID != "claude-sonnet-4-6" || models[0].OwnedBy != "anthropic" ||
		len(models[0].Capabilities) != 2 || models[0].ContextWindow != 200000 {
		t.Fatalf("bad decode: %+v", models)
	}
	// Absent optional fields decode to zero values, not errors.
	if models[1].ContextWindow != 0 || models[1].Capabilities != nil || models[1].Deprecated {
		t.Fatalf("optional model fields must be zero: %+v", models[1])
	}
}

func TestModelsUnauthorized(t *testing.T) {
	srv := gatewayStub(t, true)
	_, err := testClient(t, srv.URL, "").Models(context.Background())
	if !errors.As(err, new(*Error)) {
		t.Fatalf("want api.Error, got %v", err)
	}
}

// pathRecorder captures the request path without changing transport behavior.
type pathRecorder struct{ path *string }

func (p pathRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	*p.path = r.URL.Path
	return http.DefaultTransport.RoundTrip(r)
}
