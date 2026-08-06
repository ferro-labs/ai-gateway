//go:build integration
// +build integration

package http_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestProxy_PassThrough verifies that unknown /v1/* paths are forwarded to
// the upstream provider via the pass-through proxy.
func TestProxy_PassThrough(t *testing.T) {
	// Stand up a fake upstream that records inbound requests.
	var upstreamGot *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamGot = r
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer upstream.Close()

	env := newTestServer(t)
	env.Stub.SetBaseURL(upstream.URL + "/v1")

	// /v1/fine_tuning/jobs is not natively handled, so it exercises the generic
	// /v1/* pass-through (unlike /v1/files, which is now the batch surface).
	req := newTestRequest(t, http.MethodGet, env.Server.URL+"/v1/fine_tuning/jobs", nil)
	req.Header.Set("X-Provider", "stub")
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("got status %d; want 200. body: %s", resp.StatusCode, body)
	}
	if upstreamGot == nil {
		t.Fatal("upstream never received a request")
	}
	if !strings.HasPrefix(upstreamGot.URL.Path, "/v1/fine_tuning/jobs") {
		t.Errorf("upstream got path %q; want prefix /v1/fine_tuning/jobs", upstreamGot.URL.Path)
	}
}

// TestProxy_NoProvider returns 400 when no provider can be resolved.
func TestProxy_NoProvider(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/fine_tuning/jobs", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d; want 400", resp.StatusCode)
	}
}

// TestProxy_AuthHeadersInjected verifies that the proxy injects the stub's
// auth headers on the forwarded request.
func TestProxy_AuthHeadersInjected(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	env := newTestServer(t)
	env.Stub.SetBaseURL(upstream.URL + "/v1")

	req := newTestRequest(t, http.MethodGet, env.Server.URL+"/v1/fine_tuning/jobs", nil)
	req.Header.Set("X-Provider", "stub")
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if gotAuth != "Bearer stub-key" {
		t.Errorf("upstream auth header = %q; want %q", gotAuth, "Bearer stub-key")
	}
}

// TestProxy_PathTraversalRejectedBeforeUpstream drives SEC-009 through the
// production-wired router with gateway authentication enabled. The pass-through
// must reject traversal semantics before it contacts the selected target or
// installs that target's credential.
func TestProxy_PathTraversalRejectedBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("provider credential reached upstream")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	env := newTestServer(t)
	env.Stub.SetBaseURL(upstream.URL + "/api/v1")

	for _, path := range []string{
		"/v1/../control-plane/secrets",
		"/v1/%252e%252e/control-plane/secrets",
	} {
		t.Run(path, func(t *testing.T) {
			req := newTestRequest(t, http.MethodGet, env.Server.URL+path, nil)
			req.Header.Set("X-Provider", "stub")
			req.Header.Set("Authorization", "Bearer "+testMasterKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer closeTestBody(t, resp.Body)

			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusBadRequest, body)
			}
			assertOpenAIError(t, resp.Body, "invalid_request_error", "invalid_proxy_path")
		})
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}
