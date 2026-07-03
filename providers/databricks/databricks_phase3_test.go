package databricks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestDatabricks_CompleteErrorPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"endpoint overloaded"}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "databricks-claude-sonnet-4-5",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "endpoint overloaded") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want status + message", err)
	}
}

func TestDatabricks_CompleteStreamErrorPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.CompleteStream(context.Background(), core.Request{
		Model:    "databricks-claude-sonnet-4-5",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want status + message", err)
	}
}

func TestDatabricks_NewRejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}

// TestDatabricks_EmbedErrorPath verifies a valid Embed request whose upstream
// returns a non-200 surfaces the shared core.APIError behavior through
// openaicompat.PostEmbeddings (input-validation errors are covered separately).
func TestDatabricks_EmbedErrorPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"embed overloaded"}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "databricks-bge-large-en",
		Input: "hello",
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "embed overloaded") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want status + upstream message", err)
	}
}

// TestDatabricks_NewTrimsWhitespaceBeforeValidating verifies a whitespace-padded
// base URL (common from env/secrets) is trimmed before validation, not rejected.
func TestDatabricks_NewTrimsWhitespaceBeforeValidating(t *testing.T) {
	p, err := New("k", "  https://demo.databricks.com  ")
	if err != nil {
		t.Fatalf("New rejected a whitespace-padded valid URL: %v", err)
	}
	if !strings.HasPrefix(p.BaseURL(), "https://demo.databricks.com") {
		t.Errorf("BaseURL = %q, want trimmed", p.BaseURL())
	}
}
