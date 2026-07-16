//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMetrics_ExposeGatewayCounters(t *testing.T) {
	env := newTestServer(t)

	// First, make a chat request to generate some metrics.
	chatBody := `{
		"model": "` + stubModelName + `",
		"messages": [{"role": "user", "content": "Hello"}]
	}`
	chatReq := newTestRequest(t, "POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(chatBody))
	chatReq.Header.Set("Authorization", "Bearer "+testMasterKey)
	chatReq.Header.Set("Content-Type", "application/json")

	chatResp, err := http.DefaultClient.Do(chatReq)
	if err != nil {
		t.Fatalf("chat request: %v", err)
	}
	defer closeTestBody(t, chatResp.Body)

	if chatResp.StatusCode != http.StatusOK {
		t.Fatalf("chat request failed: %d", chatResp.StatusCode)
	}

	// Now fetch metrics.
	metricsReq := newTestRequest(t, "GET", env.Server.URL+"/metrics", nil)
	metricsReq.Header.Set("Authorization", "Bearer "+testMasterKey)

	metricsResp, err := http.DefaultClient.Do(metricsReq)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer closeTestBody(t, metricsResp.Body)

	if metricsResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(metricsResp.Body)
		t.Fatalf("expected 200, got %d: %s", metricsResp.StatusCode, body)
	}

	body, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}

	metricsText := string(body)

	// Verify that gateway_* counters are present (metric prefix is "gateway_").
	expectedSubstrings := []string{
		"gateway_requests_total",
		"gateway_request_duration_seconds",
	}
	for _, substr := range expectedSubstrings {
		if !strings.Contains(metricsText, substr) {
			t.Errorf("expected metrics output to contain %q", substr)
		}
	}
}

func TestMetrics_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	resp, err := http.Get(env.Server.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated metrics, got %d", resp.StatusCode)
	}
}
