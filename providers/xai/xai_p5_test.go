package xai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestXAIProvider_Complete_NestedUsage verifies xAI's nested usage accounting
// (completion_tokens_details.reasoning_tokens and prompt_tokens_details.cached_tokens)
// is surfaced onto the canonical core.Usage.
func TestXAIProvider_Complete_NestedUsage(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"grok-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28,"completion_tokens_details":{"reasoning_tokens":5},"prompt_tokens_details":{"cached_tokens":12}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "grok-4",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.ReasoningTokens != 5 {
		t.Errorf("Usage.ReasoningTokens = %d, want 5 (nested completion_tokens_details.reasoning_tokens)", resp.Usage.ReasoningTokens)
	}
	if resp.Usage.CacheReadTokens != 12 {
		t.Errorf("Usage.CacheReadTokens = %d, want 12 (nested prompt_tokens_details.cached_tokens)", resp.Usage.CacheReadTokens)
	}
}

// TestXAIProvider_Complete_ErrorStatus verifies the chat error path returns a
// core.APIError carrying the upstream status code and message.
func TestXAIProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "grok-4",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx chat response, got nil")
	}
	if !strings.Contains(err.Error(), "xai API error (400)") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "xai API error (400)")
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Errorf("error = %q, want it to contain upstream message %q", err.Error(), "bad model")
	}
}

// TestXAIProvider_GenerateImage_ErrorStatus verifies the image error path
// returns a core.APIError carrying the upstream status code and message.
func TestXAIProvider_GenerateImage_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "grok-2-image",
		Prompt: "a red apple",
	})
	if err == nil {
		t.Fatal("expected error for non-2xx image response, got nil")
	}
	if !strings.Contains(err.Error(), "xai API error (500)") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "xai API error (500)")
	}
	if !strings.Contains(err.Error(), "upstream boom") {
		t.Errorf("error = %q, want it to contain upstream message %q", err.Error(), "upstream boom")
	}
}

// TestXAIProvider_DiscoverModels verifies live model discovery against an
// OpenAI-compatible /models endpoint.
func TestXAIProvider_DiscoverModels(t *testing.T) {
	respBody := `{"object":"list","data":[{"id":"grok-4","object":"model","owned_by":"xai"},{"id":"grok-3","object":"model","owned_by":"xai"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("request path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("DiscoverModels() returned %d models, want 2", len(models))
	}
	found := false
	for _, m := range models {
		if m.ID == "grok-4" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DiscoverModels() = %+v, want a model with ID grok-4", models)
	}
}
