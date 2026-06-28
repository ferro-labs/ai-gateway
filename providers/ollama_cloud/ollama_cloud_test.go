package ollamacloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	testAuthHeader  = "Bearer test-key"
	testCloudModel  = "gpt-oss:20b"
	testCloudAPIKey = "test-key"
)

func TestNewValidationAndDefaults(t *testing.T) {
	if _, err := New("", "", nil); err == nil {
		t.Fatal("expected empty API key to be rejected")
	}
	if _, err := New("   ", "", nil); err == nil {
		t.Fatal("expected whitespace API key to be rejected")
	}

	for _, baseURL := range []string{"ftp://example.com", "http://", "example.com", "://bad"} {
		if _, err := New("test-key", baseURL, nil); err == nil {
			t.Fatalf("expected invalid base URL %q to be rejected", baseURL)
		}
	}

	p, err := New(" test-key ", "", nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if p.apiKey != "test-key" {
		t.Fatalf("api key was not trimmed, got %q", p.apiKey)
	}
	if p.baseURL != defaultBaseURL {
		t.Fatalf("default base URL = %q, want %q", p.baseURL, defaultBaseURL)
	}
	wantModels := []string{"gpt-oss:120b", "gpt-oss:20b", "qwen3-coder:480b", "deepseek-v3.1:671b"}
	if !reflect.DeepEqual(p.SupportedModels(), wantModels) {
		t.Fatalf("default models = %#v, want %#v", p.SupportedModels(), wantModels)
	}
	if _, ok := any(p).(core.ProxiableProvider); ok {
		t.Fatal("ollama-cloud must not implement core.ProxiableProvider")
	}

	p, err = New("test-key", "https://example.com///", []string{" custom ", "", "custom"})
	if err != nil {
		t.Fatalf("New with custom URL returned error: %v", err)
	}
	if p.baseURL != "https://example.com" {
		t.Fatalf("base URL = %q, want trimmed URL", p.baseURL)
	}
	if !reflect.DeepEqual(p.SupportedModels(), []string{"custom"}) {
		t.Fatalf("custom models = %#v, want [custom]", p.SupportedModels())
	}
}

func TestCompleteSendsAuthAndMapsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testAuthHeader {
			t.Errorf("Authorization = %q, want %s", got, testAuthHeader)
		}
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var body struct {
			Model     string         `json:"model"`
			Messages  []core.Message `json:"messages"`
			Stream    bool           `json:"stream"`
			MaxTokens *int           `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body.Model != testCloudModel {
			t.Errorf("model = %q, want %s", body.Model, testCloudModel)
		}
		if body.Stream {
			t.Error("stream = true, want false for non-streaming")
		}
		if body.MaxTokens == nil || *body.MaxTokens != 99 {
			t.Errorf("max_tokens = %v, want 99", body.MaxTokens)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"cmpl-1",
			"object":"chat.completion",
			"created":1735786245,
			"model":"` + testCloudModel + `",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}
		}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	maxTokens := 99
	resp, err := p.Complete(context.Background(), core.Request{
		Model:     testCloudModel,
		Messages:  []core.Message{{Role: "user", Content: "hello"}},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}

	if resp.Provider != Name {
		t.Fatalf("provider = %q, want %q", resp.Provider, Name)
	}
	if resp.Model != testCloudModel {
		t.Fatalf("model = %q, want %s", resp.Model, testCloudModel)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices length = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.Role != "assistant" || resp.Choices[0].Message.Content != "hi there" {
		t.Fatalf("message = %#v, want assistant/hi there", resp.Choices[0].Message)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %#v, want 11/7/18", resp.Usage)
	}
}

func TestCompleteNon200Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model is required"}}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = p.Complete(context.Background(), core.Request{Model: testCloudModel})
	if err == nil {
		t.Fatal("expected Complete to return an error")
	}
	if got := err.Error(); !strings.Contains(got, "400") {
		t.Fatalf("error = %q, want status 400 in message", got)
	}
}

func TestCompleteStreamParsesSSEAndFinalUsage(t *testing.T) {
	ssePayload := "data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1735786245,\"model\":\"" + testCloudModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1735786245,\"model\":\"" + testCloudModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1735786245,\"model\":\"" + testCloudModel + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testAuthHeader {
			t.Errorf("Authorization = %q, want %s", got, testAuthHeader)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    testCloudModel,
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	var chunks []core.StreamChunk
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) < 3 {
		t.Fatalf("chunks length = %d, want at least 3", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Content != "hel" {
		t.Fatalf("first delta content = %q, want hel", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[1].Choices[0].Delta.Content != "lo" {
		t.Fatalf("second delta content = %q, want lo", chunks[1].Choices[0].Delta.Content)
	}
	lastIdx := len(chunks) - 1
	if chunks[lastIdx].Choices[0].FinishReason != "stop" {
		t.Fatalf("final finish reason = %q, want stop", chunks[lastIdx].Choices[0].FinishReason)
	}
}

func TestDiscoverModelsUpdatesSupportsModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id":"gpt-oss:20b","object":"model","owned_by":"ollama-cloud"},
				{"id":"qwen3-coder:480b","object":"model","owned_by":"ollama-cloud"}
			]
		}`))
	}))
	defer server.Close()

	p, err := New("test-key", server.URL, []string{"configured:model"})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels returned error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models length = %d, want 2", len(models))
	}
	if models[0].ID != "gpt-oss:20b" || models[0].Object != "model" {
		t.Fatalf("first model = %#v, want gpt-oss:20b", models[0])
	}
	if models[1].ID != "qwen3-coder:480b" {
		t.Fatalf("second model ID = %q, want qwen3-coder:480b", models[1].ID)
	}
	if !p.SupportsModel("qwen3-coder:480b") {
		t.Fatal("SupportsModel should include discovered model")
	}
	if !p.SupportsModel("ollama-cloud/qwen3-coder:480b") {
		t.Fatal("SupportsModel should accept ollama-cloud-prefixed discovered model")
	}
}

func TestSupportsModel(t *testing.T) {
	p, err := New("test-key", "https://example.com", []string{"gpt-oss:20b"})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if !p.SupportsModel("gpt-oss:20b") {
		t.Fatal("SupportsModel(gpt-oss:20b) = false, want true")
	}
	if !p.SupportsModel("ollama-cloud/gpt-oss:20b") {
		t.Fatal("SupportsModel(ollama-cloud/gpt-oss:20b) = false, want true")
	}
	if p.SupportsModel("gpt-4o") {
		t.Fatal("SupportsModel(gpt-4o) = true, want false")
	}
	if p.SupportsModel("other/gpt-oss:20b") {
		t.Fatal("SupportsModel(other/gpt-oss:20b) = true, want false")
	}
}
