//go:build integration
// +build integration

package http_test

import (
	"net/http"
	"testing"
)

func TestMiddlewareOrder_CORSThenAuthThenRateLimit(t *testing.T) {
	// Boot server with CORS restricted + rate limiter active.
	env := newTestServer(t,
		withCORSOrigins("https://allowed.example.com"),
		withRateLimit(100, 100),
	)

	// An unauthenticated request should still get CORS headers (CORS runs
	// before auth), but should fail auth.
	req := newTestRequest(t, "GET", env.Server.URL+"/v1/models", nil)
	req.Header.Set("Origin", "https://allowed.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	// Auth should reject with 401.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// CORS headers should still be present on the 401 response (CORS
	// middleware ran before auth middleware).
	acao := resp.Header.Get("Access-Control-Allow-Origin")
	if acao != "https://allowed.example.com" {
		t.Errorf("expected CORS header on 401 response, got ACAO=%q", acao)
	}
}
