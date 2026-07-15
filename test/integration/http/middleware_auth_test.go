//go:build integration
// +build integration

package http_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestMiddlewareAuth_MissingBearer_Returns401(t *testing.T) {
	env := newTestServer(t)

	// Request to an auth-protected endpoint without Authorization header.
	resp, err := http.Get(env.Server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	var errResp struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Error.Type != "authentication_error" {
		t.Errorf("expected type=authentication_error, got %q", errResp.Error.Type)
	}
}

func TestMiddlewareAuth_InvalidBearer_Returns401(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, "GET", env.Server.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer totally-wrong-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestMiddlewareAuth_ValidMasterKey_Accepted(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, "GET", env.Server.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMiddlewareAuth_AdminEndpoint_RejectsMissing(t *testing.T) {
	env := newTestServer(t)

	// Admin endpoints should also require auth.
	resp, err := http.Get(env.Server.URL + "/admin/keys")
	if err != nil {
		t.Fatalf("GET /admin/keys: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for admin without auth, got %d", resp.StatusCode)
	}
}
