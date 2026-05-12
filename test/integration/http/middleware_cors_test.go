//go:build integration
// +build integration

package http_test

import (
	"net/http"
	"testing"
)

func TestMiddlewareCORS_Preflight_Wildcard(t *testing.T) {
	// No CORS_ORIGINS set → wildcard '*'.
	env := newTestServer(t)

	req, _ := http.NewRequest("OPTIONS", env.Server.URL+"/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for preflight, got %d", resp.StatusCode)
	}

	origin := resp.Header.Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Fatalf("expected ACAO=*, got %q", origin)
	}

	methods := resp.Header.Get("Access-Control-Allow-Methods")
	if methods == "" {
		t.Error("expected Access-Control-Allow-Methods to be set")
	}

	headers := resp.Header.Get("Access-Control-Allow-Headers")
	if headers == "" {
		t.Error("expected Access-Control-Allow-Headers to be set")
	}
}

func TestMiddlewareCORS_RestrictedOrigins(t *testing.T) {
	env := newTestServer(t, withCORSOrigins("https://allowed.example.com"))

	// Allowed origin.
	req, _ := http.NewRequest("OPTIONS", env.Server.URL+"/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://allowed.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	resp.Body.Close()

	origin := resp.Header.Get("Access-Control-Allow-Origin")
	if origin != "https://allowed.example.com" {
		t.Fatalf("expected ACAO=https://allowed.example.com, got %q", origin)
	}

	// Disallowed origin.
	req2, _ := http.NewRequest("OPTIONS", env.Server.URL+"/v1/chat/completions", nil)
	req2.Header.Set("Origin", "https://evil.example.com")
	req2.Header.Set("Access-Control-Request-Method", "POST")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	resp2.Body.Close()

	origin2 := resp2.Header.Get("Access-Control-Allow-Origin")
	if origin2 != "" {
		t.Fatalf("expected no ACAO for disallowed origin, got %q", origin2)
	}
}
