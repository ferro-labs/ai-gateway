package ai21

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestAI21Provider_Complete_JambaAPIError verifies a non-2xx Jamba response is
// surfaced as a core.APIError carrying the parseable status code and message.
func TestAI21Provider_Complete_JambaAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "jamba-mini-1.7",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if got := core.ParseStatusCode(err); got != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode = %d, want %d", got, http.StatusTooManyRequests)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error %q does not contain upstream message", err.Error())
	}
}

// TestAI21Provider_CompleteStream_JambaAPIError verifies a non-2xx streaming
// Jamba response is surfaced as a core.APIError before any chunk is emitted.
func TestAI21Provider_CompleteStream_JambaAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"server boom"}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "jamba-mini-1.7",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx stream response, got nil")
	}
	if got := core.ParseStatusCode(err); got != http.StatusInternalServerError {
		t.Errorf("ParseStatusCode = %d, want %d", got, http.StatusInternalServerError)
	}
	if !strings.Contains(err.Error(), "server boom") {
		t.Errorf("error %q does not contain upstream message", err.Error())
	}
}

// TestAI21Provider_Complete_JambaRequestShape verifies the Jamba path issues a
// POST to /chat/completions with Bearer auth and forwards the requested model.
func TestAI21Provider_Complete_JambaRequestShape(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotModel  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotModel = body.Model

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"jamba-large-1.7","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    "jamba-large-1.7",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want 'Bearer test-key'", gotAuth)
	}
	if gotModel != "jamba-large-1.7" {
		t.Errorf("forwarded model = %q, want jamba-large-1.7", gotModel)
	}
}
