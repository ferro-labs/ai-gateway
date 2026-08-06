//go:build integration
// +build integration

package http_test

import (
	"net/http"
	"testing"
)

func TestMiddlewareCORS_Preflight_NoOriginsConfigured_Blocked(t *testing.T) {
	// No CORS_ORIGINS set → the request is denied by WITHHOLDING
	// Access-Control-Allow-Origin, never by refusing the preflight. The CORS
	// layer answers the preflight 204 ahead of authentication and ahead of the
	// route's method guard, so neither a 401 (a preflight carries no
	// credentials) nor a 405 (the resource does serve OPTIONS, and answers 204
	// the moment the origin is listed) can leak out. The header is what the
	// browser enforces: without it, it blocks the request that would have
	// followed.
	env := newTestServer(t)

	req := newTestRequest(t, "OPTIONS", env.Server.URL+"/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for unconfigured-origin preflight (denial is the missing ACAO, not the status), got %d", resp.StatusCode)
	}
	if origin := resp.Header.Get("Access-Control-Allow-Origin"); origin != "" {
		t.Fatalf("expected no ACAO header when no origins configured, got %q", origin)
	}
}

func TestMiddlewareCORS_RestrictedOrigins(t *testing.T) {
	env := newTestServer(t, withCORSOrigins("https://allowed.example.com"))

	// Allowed origin.
	req := newTestRequest(t, "OPTIONS", env.Server.URL+"/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://allowed.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	// Both branches below answer 204: the preflight is settled by the CORS
	// layer either way, and only the presence of ACAO differs.
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for allowed-origin preflight, got %d", resp.StatusCode)
	}
	origin := resp.Header.Get("Access-Control-Allow-Origin")
	if origin != "https://allowed.example.com" {
		t.Fatalf("expected ACAO=https://allowed.example.com, got %q", origin)
	}

	// Disallowed origin.
	req2 := newTestRequest(t, "OPTIONS", env.Server.URL+"/v1/chat/completions", nil)
	req2.Header.Set("Origin", "https://evil.example.com")
	req2.Header.Set("Access-Control-Request-Method", "POST")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer closeTestBody(t, resp2.Body)

	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for disallowed-origin preflight, got %d", resp2.StatusCode)
	}
	origin2 := resp2.Header.Get("Access-Control-Allow-Origin")
	if origin2 != "" {
		t.Fatalf("expected no ACAO for disallowed origin, got %q", origin2)
	}
}
