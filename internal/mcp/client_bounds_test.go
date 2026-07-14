package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestClient_RejectsOversizedResponseBody verifies the success-path read is
// bounded: an MCP server (an untrusted-content boundary) returning a body larger
// than maxResponseBodyBytes is rejected with a clear error rather than being read
// unbounded into memory.
func TestClient_RejectsOversizedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// One byte past the cap so the bounded read detects the overflow.
		_, _ = w.Write(make([]byte, maxResponseBodyBytes+1))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	_, err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected an error for an oversized MCP response body, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a size-limit error, got: %v", err)
	}
}
