package ai21

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestNewAI21_RejectsInvalidBaseURL locks in the base-URL validation.
func TestNewAI21_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}

// TestAI21Provider_Complete_DetailError verifies AI21's native {"detail":…} error
// envelope surfaces the extracted message (not a raw JSON blob) on the Jamba path.
func TestAI21Provider_Complete_DetailError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Invalid API Key"}`))
	}))
	defer srv.Close()

	p, err := New("bad-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "jamba-large-1.7",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("error = %q, want the extracted detail message", err.Error())
	}
	if code := core.ParseStatusCode(err); code != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want 401", code)
	}
}
