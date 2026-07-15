//go:build integration
// +build integration

package http_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	env.Stub.SetBaseURL(upstream.URL)

	req := newTestRequest(t, http.MethodGet, env.Server.URL+"/v1/files", nil)
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
	if !strings.HasPrefix(upstreamGot.URL.Path, "/v1/files") {
		t.Errorf("upstream got path %q; want prefix /v1/files", upstreamGot.URL.Path)
	}
}

// TestProxy_NoProvider returns 400 when no provider can be resolved.
func TestProxy_NoProvider(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/files", strings.NewReader(`{}`))
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
	env.Stub.SetBaseURL(upstream.URL)

	req := newTestRequest(t, http.MethodGet, env.Server.URL+"/v1/files", nil)
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
