package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewAzureFoundry(t *testing.T) {
	p, err := NewAzureFoundry(testAPIKey, "https://example.services.ai.azure.com", "")
	if err != nil {
		t.Fatalf("NewAzureFoundry() error: %v", err)
	}
	if p.Name() != "azure-foundry" {
		t.Errorf("Name() = %q, want azure-foundry", p.Name())
	}
	if p.APIVersion() != azureFoundryDefaultAPIVersion {
		t.Errorf("apiVersion = %q, want %s", p.APIVersion(), azureFoundryDefaultAPIVersion)
	}
}

func TestNewAzureFoundry_RequiresBaseURL(t *testing.T) {
	_, err := NewAzureFoundry(testAPIKey, "", "")
	if err == nil {
		t.Fatal("expected error when baseURL is empty")
	}
}

func TestAzureFoundryProvider_AuthHeaders(t *testing.T) {
	p, _ := NewAzureFoundry(testAPIKey, "https://example.services.ai.azure.com", "")
	headers := p.AuthHeaders()
	if headers["api-key"] != testAPIKey {
		t.Errorf("AuthHeaders api-key = %q, want %s", headers["api-key"], testAPIKey)
	}
}

func TestAzureFoundryProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewAzureFoundry(testAPIKey, "https://example.services.ai.azure.com", "")
	var _ StreamProvider = p
}

func TestAzureFoundryProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testChatCompletionsPath {
			t.Errorf("request path = %q, want %s", r.URL.Path, testChatCompletionsPath)
		}
		if r.URL.Query().Get("api-version") != azureFoundryDefaultAPIVersion {
			t.Errorf("api-version = %q, want %s", r.URL.Query().Get("api-version"), azureFoundryDefaultAPIVersion)
		}
		if got := r.Header.Get("api-key"); got != testAPIKey {
			t.Errorf("api-key = %q, want %s", got, testAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, err := NewAzureFoundry(testAPIKey, srv.URL, "")
	if err != nil {
		t.Fatalf("NewAzureFoundry() error: %v", err)
	}
	resp, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Provider != "azure-foundry" {
		t.Errorf("Response.Provider = %q, want azure-foundry", resp.Provider)
	}
}

func TestAzureFoundryProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewAzureFoundry(testAPIKey, srv.URL, "")
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "gpt-4o",
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
}
