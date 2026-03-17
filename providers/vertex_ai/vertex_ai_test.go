package vertexai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewVertexAI_APIKeyMode(t *testing.T) {
	p, err := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "vertex-ai" {
		t.Errorf("Name() = %q, want vertex-ai", p.Name())
	}
	if p.BaseURL() == "" {
		t.Error("BaseURL() should not be empty")
	}
}

func TestNewVertexAI_RequiresProjectID(t *testing.T) {
	_, err := New(Options{Region: "us-central1", APIKey: "test-key"})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestNewVertexAI_RequiresRegion(t *testing.T) {
	_, err := New(Options{ProjectID: "demo-project", APIKey: "test-key"})
	if err == nil {
		t.Fatal("expected error for missing region")
	}
}

func TestNewVertexAI_RequiresAuth(t *testing.T) {
	_, err := New(Options{ProjectID: "demo-project", Region: "us-central1"})
	if err == nil {
		t.Fatal("expected error when API key and service account JSON are both empty")
	}
}

func TestNewVertexAI_ServiceAccountInvalidJSON(t *testing.T) {
	_, err := New(Options{
		ProjectID:          "demo-project",
		Region:             "us-central1",
		ServiceAccountJSON: "{invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid service account JSON")
	}
}

func TestVertexAIProvider_AuthHeaders_APIKey(t *testing.T) {
	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	headers := p.AuthHeaders()
	if headers["x-goog-api-key"] != "test-key" {
		t.Errorf("x-goog-api-key = %q, want test-key", headers["x-goog-api-key"])
	}
}

func TestVertexAIProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	var _ core.StreamProvider = p
}

func TestVertexAIProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"gemini-2.5-flash","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	p.SetBaseURL(srv.URL)

	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Provider != "vertex-ai" {
		t.Errorf("Response.Provider = %q, want vertex-ai", resp.Provider)
	}
}

func TestVertexAIProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	p.SetBaseURL(srv.URL)

	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
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
}
