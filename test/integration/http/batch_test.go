//go:build integration
// +build integration

package http_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBatch_ForwardsToConfiguredTarget drives the wired router: a POST /v1/files
// is forwarded to the configured batch target's upstream with the provider
// credential, and the upstream File object comes back verbatim.
func TestBatch_ForwardsToConfiguredTarget(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-xyz","object":"file","purpose":"batch"}`))
	}))
	defer upstream.Close()

	env := newTestServer(t, withBatchTarget("stub"))
	env.Stub.SetBatchBaseURL(upstream.URL)

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/files", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/files: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}
	if gotPath != "/files" {
		t.Errorf("upstream path = %q, want /files", gotPath)
	}
	if gotAuth != "Bearer stub-batch-key" {
		t.Errorf("upstream Authorization = %q, want the provider batch credential", gotAuth)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"id":"file-xyz","object":"file","purpose":"batch"}` {
		t.Errorf("body = %q, want the upstream File object", body)
	}
}

// A bare batch-id retrieve forwards to the same backend — no model needed.
func TestBatch_RetrieveBareId(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"batch_9","object":"batch","status":"in_progress"}`))
	}))
	defer upstream.Close()

	env := newTestServer(t, withBatchTarget("stub"))
	env.Stub.SetBatchBaseURL(upstream.URL)

	req := newTestRequest(t, "GET", env.Server.URL+"/v1/batches/batch_9", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/batches/batch_9: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/batches/batch_9" {
		t.Errorf("upstream path = %q, want /batches/batch_9", gotPath)
	}
}

// With no batch_target configured, the surface is off and answers 501.
func TestBatch_NotConfigured_Returns501(t *testing.T) {
	env := newTestServer(t) // no withBatchTarget

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/batches", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
}

func TestBatch_RequiresAuth(t *testing.T) {
	env := newTestServer(t, withBatchTarget("stub"))

	req := newTestRequest(t, "GET", env.Server.URL+"/v1/files", nil)
	// No Authorization header.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
