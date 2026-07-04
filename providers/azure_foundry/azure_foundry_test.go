package azurefoundry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	testAPIKey                    = "test-key"
	testBearerAPIKey              = "Bearer test-key"
	testChatCompletionsPath       = "/openai/v1/chat/completions"
	azureFoundryDefaultAPIVersion = "2024-05-01-preview"
)

func TestNewAzureFoundry(t *testing.T) {
	p, err := New(testAPIKey, "https://example.services.ai.azure.com", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "azure-foundry" {
		t.Errorf("Name() = %q, want azure-foundry", p.Name())
	}
	if p.APIVersion() != azureFoundryDefaultAPIVersion {
		t.Errorf("apiVersion = %q, want %s", p.APIVersion(), azureFoundryDefaultAPIVersion)
	}
}

func TestNewAzureFoundry_RequiresBaseURL(t *testing.T) {
	_, err := New(testAPIKey, "", "")
	if err == nil {
		t.Fatal("expected error when baseURL is empty")
	}
}

func TestAzureFoundryProvider_AuthHeaders(t *testing.T) {
	p, _ := New(testAPIKey, "https://example.services.ai.azure.com", "")
	headers := p.AuthHeaders()
	if headers["api-key"] != testAPIKey {
		t.Errorf("AuthHeaders api-key = %q, want %s", headers["api-key"], testAPIKey)
	}
}

func TestAzureFoundryProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New(testAPIKey, "https://example.services.ai.azure.com", "")
	var _ core.StreamProvider = p
}

func TestAzureFoundryProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testChatCompletionsPath {
			t.Errorf("request path = %q, want %s", r.URL.Path, testChatCompletionsPath)
		}
		if v := r.URL.Query().Get("api-version"); v != "" {
			t.Errorf("api-version = %q, want none (GA v1 route takes no api-version)", v)
		}
		if got := r.Header.Get("api-key"); got != testAPIKey {
			t.Errorf("api-key = %q, want %s", got, testAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, err := New(testAPIKey, srv.URL, "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
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

	p, _ := New(testAPIKey, srv.URL, "")
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gpt-4o",
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

// TestComplete_SetsExtraParametersAndDecodes verifies the request carries the
// "extra-parameters: drop" header and the response is decoded (content + usage).
func TestComplete_SetsExtraParametersAndDecodes(t *testing.T) {
	var extraParams string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extraParams = r.Header.Get("extra-parameters")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer srv.Close()

	p, err := New(testAPIKey, srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if extraParams != "drop" {
		t.Errorf("extra-parameters header = %q, want drop", extraParams)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("decoded choices = %+v", resp.Choices)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want total 7", resp.Usage)
	}
}

func TestComplete_ErrorPathReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad model"}}`)
	}))
	defer srv.Close()

	p, err := New(testAPIKey, srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "bad model") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v, want status + message", err)
	}
}

func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New(testAPIKey, "://bad", ""); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
