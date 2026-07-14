//go:build integration
// +build integration

package http_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestHealth_OK(t *testing.T) {
	env := newTestServer(t)

	resp, err := http.Get(env.Server.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode json: %v", err)
	}

	status, ok := result["status"].(string)
	if !ok || status != "ok" {
		t.Fatalf("expected status=ok, got %v", result["status"])
	}

	providers, ok := result["providers"].([]any)
	if !ok {
		t.Fatalf("expected providers array, got %T", result["providers"])
	}
	if len(providers) == 0 {
		t.Fatal("expected at least one provider in health response")
	}

	// Verify the stub provider is listed.
	p0, ok := providers[0].(map[string]any)
	if !ok {
		t.Fatalf("expected provider object, got %T", providers[0])
	}
	if p0["name"] != "stub" {
		t.Fatalf("expected provider name=stub, got %v", p0["name"])
	}
	if p0["status"] != "available" {
		t.Fatalf("expected status=available, got %v", p0["status"])
	}
}
