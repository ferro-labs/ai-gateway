package perplexity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	testAPIKey              = "test-key"
	testBearerAPIKey        = "Bearer test-key"
	testChatCompletionsPath = "/chat/completions"
)

func TestNewPerplexity(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "perplexity" {
		t.Errorf("Name() = %q, want perplexity", p.Name())
	}
}

func TestPerplexityProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "sonar" {
			found = true
		}
	}
	if !found {
		t.Error("sonar not found in supported models")
	}
}

func TestPerplexityProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("sonar") {
		t.Error("expected sonar to be supported")
	}
	if !p.SupportsModel("any-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestPerplexityProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "perplexity" {
			t.Errorf("ModelInfo.OwnedBy = %q, want perplexity", m.OwnedBy)
		}
	}
}

func TestPerplexityProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestPerplexityProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestPerplexityProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, testChatCompletionsPath) {
			t.Errorf("path = %q, want suffix %s", r.URL.Path, testChatCompletionsPath)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "sonar" {
			t.Errorf("model = %v, want sonar", body["model"])
		}
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 1 {
			t.Fatalf("messages = %v, want one message forwarded", body["messages"])
		}
		if m0, _ := msgs[0].(map[string]any); m0["role"] != "user" || m0["content"] != "Hi" {
			t.Errorf("message[0] = %v, want {role:user, content:Hi}", msgs[0])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "sonar",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}
	if chunks[1].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[2].Choices[0].Delta.Content)
	}
}

func TestPerplexityProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"sonar","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, testChatCompletionsPath) {
			t.Errorf("path = %q, want suffix %s", r.URL.Path, testChatCompletionsPath)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "sonar" {
			t.Errorf("model = %v, want sonar", body["model"])
		}
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 1 {
			t.Fatalf("messages = %v, want one message forwarded", body["messages"])
		}
		if m0, _ := msgs[0].(map[string]any); m0["role"] != "user" || m0["content"] != "Hi" {
			t.Errorf("message[0] = %v, want {role:user, content:Hi}", msgs[0])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "sonar",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "chatcmpl-1" {
		t.Errorf("Response.ID = %q, want chatcmpl-1", resp.ID)
	}
	if len(resp.Choices) == 0 {
		t.Error("expected at least one choice")
	}
}

// TestPerplexityProvider_Complete_CapturesCitations verifies the Sonar-specific
// top-level "citations" field is surfaced into core.Response.Metadata via the
// shared ExtraResponseFields seam.
func TestPerplexityProvider_Complete_CapturesCitations(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"sonar","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"citations":["https://example.com/a","https://example.com/b"],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "sonar",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Metadata == nil {
		t.Fatal("expected Metadata to be populated with citations")
	}
	citations, ok := resp.Metadata["citations"].([]any)
	if !ok {
		t.Fatalf("Metadata[\"citations\"] = %T, want []any", resp.Metadata["citations"])
	}
	if len(citations) != 2 {
		t.Errorf("len(citations) = %d, want 2", len(citations))
	}
}

// TestPerplexityProvider_Complete_ErrorStatus verifies a non-2xx chat response is
// surfaced as a provider error rather than a decoded response.
func TestPerplexityProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "sonar",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if resp != nil {
		t.Errorf("expected nil response on error, got %+v", resp)
	}
}

// TestNewPerplexity_InvalidBaseURL verifies a malformed base URL is rejected at
// construction time.
func TestNewPerplexity_InvalidBaseURL(t *testing.T) {
	if _, err := New(testAPIKey, "not-a-url"); err == nil {
		t.Error("expected error for invalid base URL, got nil")
	}
}
