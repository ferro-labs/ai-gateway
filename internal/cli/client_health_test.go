package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /health answers 503 while degraded. Get must still treat that as a failed
// request, but GetHealth has to decode the body — a gateway that answers at all
// is reachable, and that distinction is the point of `ferrogw status`/`doctor`.
func TestGetHealthDecodesDegradedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"no_providers","providers":[]}`))
	}))
	defer server.Close()

	client := NewAdminClient(server.URL, "")

	var viaGet map[string]any
	if err := client.Get(t.Context(), "/health", &viaGet); err == nil {
		t.Fatal("Get should surface a 503 as an error")
	}

	var health map[string]any
	if err := client.GetHealth(t.Context(), "/health", &health); err != nil {
		t.Fatalf("GetHealth should tolerate a 503: %v", err)
	}
	if health["status"] != "no_providers" {
		t.Fatalf("status = %v, want no_providers", health["status"])
	}
}

// Tolerating 503 must not swallow the codes that mean the request itself failed.
func TestGetHealthStillFailsOnUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	client := NewAdminClient(server.URL, "")

	var health map[string]any
	err := client.GetHealth(t.Context(), "/health", &health)
	if err == nil {
		t.Fatal("GetHealth should surface a 401")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("error = %v, want it to carry the upstream message", err)
	}
}
