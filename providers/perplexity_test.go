package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPerplexity(t *testing.T) {
	p, err := NewPerplexity("test-key", "")
	if err != nil {
		t.Fatalf("NewPerplexity() error: %v", err)
	}
	if p.Name() != "perplexity" {
		t.Errorf("Name() = %q, want perplexity", p.Name())
	}
}

func TestPerplexityProvider_SupportedModels(t *testing.T) {
	p, _ := NewPerplexity("test-key", "")
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
	p, _ := NewPerplexity("test-key", "")
	if !p.SupportsModel("sonar") {
		t.Error("expected sonar to be supported")
	}
	if !p.SupportsModel("any-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestPerplexityProvider_Models(t *testing.T) {
	p, _ := NewPerplexity("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "perplexity" {
			t.Errorf("ModelInfo.OwnedBy = %q, want perplexity", m.OwnedBy)
		}
	}
}

func TestPerplexityProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewPerplexity("test-key", "")
	var _ StreamProvider = p
}

func TestPerplexityProvider_AuthHeaders(t *testing.T) {
	p, _ := NewPerplexity("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != "Bearer test-key" {
		t.Errorf("AuthHeaders Authorization = %q, want Bearer test-key", headers["Authorization"])
	}
}

func TestPerplexityProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewPerplexity("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "sonar",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []StreamChunk
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := NewPerplexity("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "sonar",
		Messages: []Message{{Role: "user", Content: "Hi"}},
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
