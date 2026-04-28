package ollamacloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

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
	created := "2025-01-02T03:04:05Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s, want /api/chat", r.URL.Path)
		}
		assertCompleteRequest(t, r)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"` + testCloudModel + `",
			"created_at":"` + created + `",
			"message":{"role":"assistant","content":"hi there"},
			"done":true,
			"done_reason":"stop",
			"prompt_eval_count":11,
			"eval_count":7
		}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	temp, topP, maxTokens, maxCompletionTokens := 0.25, 0.9, 99, 8
	resp, err := p.Complete(context.Background(), core.Request{
		Model: testCloudModel,
		Messages: []core.Message{
			{Role: "user", Content: "hello"},
		},
		Temperature:         &temp,
		TopP:                &topP,
		MaxTokens:           &maxTokens,
		MaxCompletionTokens: &maxCompletionTokens,
		Tools: []core.Tool{
			{Type: "function", Function: core.Function{Name: "lookup"}},
		},
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
	if resp.Created != mustUnix(t, created) {
		t.Fatalf("created = %d, want %d", resp.Created, mustUnix(t, created))
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices length = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.Role != "assistant" || resp.Choices[0].Message.Content != "hi there" {
		t.Fatalf("message = %#v, want assistant hi there", resp.Choices[0].Message)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %#v, want 11/7/18", resp.Usage)
	}
}

func assertCompleteRequest(t *testing.T, r *http.Request) {
	t.Helper()

	if got := r.Header.Get("Authorization"); got != testAuthHeader {
		t.Errorf("Authorization = %q, want %s", got, testAuthHeader)
	}
	if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		Model    string         `json:"model"`
		Messages []core.Message `json:"messages"`
		Stream   bool           `json:"stream"`
		Options  *struct {
			Temperature *float64 `json:"temperature"`
			TopP        *float64 `json:"top_p"`
			NumPredict  *int     `json:"num_predict"`
		} `json:"options"`
		Tools []core.Tool `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode request body: %v", err)
	}
	if body.Model != testCloudModel {
		t.Errorf("model = %q, want %s", body.Model, testCloudModel)
	}
	if body.Stream {
		t.Error("stream = true, want false")
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "hello" {
		t.Errorf("messages = %#v, want user hello", body.Messages)
	}
	if body.Options == nil || body.Options.NumPredict == nil || *body.Options.NumPredict != 8 {
		t.Fatalf("num_predict = %#v, want 8", body.Options)
	}
	if body.Options.Temperature == nil || *body.Options.Temperature != 0.25 {
		t.Fatalf("temperature = %#v, want 0.25", body.Options.Temperature)
	}
	if body.Options.TopP == nil || *body.Options.TopP != 0.9 {
		t.Fatalf("top_p = %#v, want 0.9", body.Options.TopP)
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Name != "lookup" {
		t.Fatalf("tools = %#v, want lookup tool", body.Tools)
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
	if got := err.Error(); !strings.Contains(got, "400") || !strings.Contains(got, "model is required") {
		t.Fatalf("error = %q, want status code and response message", got)
	}
}

func TestCompleteStreamParsesNDJSONAndFinalUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s, want /api/chat", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testAuthHeader {
			t.Errorf("Authorization = %q, want %s", got, testAuthHeader)
		}

		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if !body.Stream {
			t.Fatal("stream = false, want true")
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"` + testCloudModel + `","created_at":"2025-01-02T03:04:05Z","message":{"role":"assistant","content":"hel"},"done":false}` + "\n"))
		_, _ = w.Write([]byte(`{"model":"` + testCloudModel + `","message":{"role":"assistant","content":"lo"},"done":false}` + "\n"))
		_, _ = w.Write([]byte(`{"model":"` + testCloudModel + `","done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}` + "\n"))
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
	if len(chunks) != 3 {
		t.Fatalf("chunks length = %d, want 3", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" || chunks[0].Choices[0].Delta.Content != "hel" {
		t.Fatalf("first delta = %#v, want assistant hel", chunks[0].Choices[0].Delta)
	}
	if chunks[1].Choices[0].Delta.Content != "lo" {
		t.Fatalf("second delta content = %q, want lo", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("final finish reason = %q, want stop", chunks[2].Choices[0].FinishReason)
	}
	if chunks[2].Usage == nil {
		t.Fatal("final usage is nil")
	}
	if chunks[2].Usage.PromptTokens != 3 || chunks[2].Usage.CompletionTokens != 2 || chunks[2].Usage.TotalTokens != 5 {
		t.Fatalf("final usage = %#v, want 3/2/5", *chunks[2].Usage)
	}
}

func TestDiscoverModelsParsesTagsAndUpdatesSupportsModel(t *testing.T) {
	firstModified := "2025-02-03T04:05:06Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %s, want /api/tags", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"models": [
				{"name":"gpt-oss:20b","modified_at":"` + firstModified + `"},
				{"model":"qwen3-coder:480b","modified_at":"2025-02-04T04:05:06.123Z"},
				{"name":"gpt-oss:20b","modified_at":"2025-02-05T04:05:06Z"}
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
	if models[0].ID != "gpt-oss:20b" || models[0].Object != "model" || models[0].OwnedBy != Name {
		t.Fatalf("first model = %#v, want gpt-oss:20b model owned by %s", models[0], Name)
	}
	if models[0].Created != mustUnix(t, firstModified) {
		t.Fatalf("first created = %d, want %d", models[0].Created, mustUnix(t, firstModified))
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

func mustUnix(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("failed to parse time %q: %v", value, err)
	}
	return parsed.Unix()
}
