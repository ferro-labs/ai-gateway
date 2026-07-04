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
	if defaultBaseURL != "https://ollama.com" {
		t.Fatalf("default base URL must stay https://ollama.com (api.ollama.com 301-redirects), got %q", defaultBaseURL)
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

// TestCompleteSendsAuthAndMapsResponse verifies chat routes through the
// OpenAI-compatible /v1/chat/completions surface with Bearer auth, forwards the
// OpenAI-shaped request, and maps the standard OpenAI response.
func TestCompleteSendsAuthAndMapsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		assertChatRequest(t, r, false)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
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

	temp, topP, maxTokens := 0.25, 0.9, 99
	resp, err := p.Complete(context.Background(), core.Request{
		Model: testCloudModel,
		Messages: []core.Message{
			{Role: "user", Content: "hello"},
		},
		Temperature: &temp,
		TopP:        &topP,
		MaxTokens:   &maxTokens,
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
	if resp.ID != "chatcmpl-1" {
		t.Fatalf("id = %q, want chatcmpl-1", resp.ID)
	}
	if resp.Model != testCloudModel {
		t.Fatalf("model = %q, want %s", resp.Model, testCloudModel)
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

// assertChatRequest checks the outgoing body is the OpenAI Chat Completions shape
// (top-level model/messages/params, not the native /api/chat "options" nesting).
func assertChatRequest(t *testing.T, r *http.Request, wantStream bool) {
	t.Helper()

	if got := r.Header.Get("Authorization"); got != testAuthHeader {
		t.Errorf("Authorization = %q, want %s", got, testAuthHeader)
	}
	if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		Model       string         `json:"model"`
		Messages    []core.Message `json:"messages"`
		Stream      bool           `json:"stream"`
		Temperature *float64       `json:"temperature"`
		TopP        *float64       `json:"top_p"`
		MaxTokens   *int           `json:"max_tokens"`
		Tools       []core.Tool    `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode request body: %v", err)
	}
	if body.Model != testCloudModel {
		t.Errorf("model = %q, want %s", body.Model, testCloudModel)
	}
	if body.Stream != wantStream {
		t.Errorf("stream = %v, want %v", body.Stream, wantStream)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "hello" {
		t.Errorf("messages = %#v, want user hello", body.Messages)
	}
	if body.Temperature == nil || *body.Temperature != 0.25 {
		t.Errorf("temperature = %#v, want 0.25", body.Temperature)
	}
	if body.TopP == nil || *body.TopP != 0.9 {
		t.Errorf("top_p = %#v, want 0.9", body.TopP)
	}
	if body.MaxTokens == nil || *body.MaxTokens != 99 {
		t.Errorf("max_tokens = %#v, want 99", body.MaxTokens)
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Name != "lookup" {
		t.Errorf("tools = %#v, want lookup tool", body.Tools)
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
	if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
		t.Fatalf("ParseStatusCode(err) = %d, want 400", got)
	}
	if got := err.Error(); !strings.Contains(got, "400") || !strings.Contains(got, "model is required") {
		t.Fatalf("error = %q, want status code and response message", got)
	}
}

// TestCompleteStreamParsesSSEAndFinalUsage verifies the streaming path consumes a
// standard OpenAI SSE response (data: {...} frames terminated by [DONE]).
func TestCompleteStreamParsesSSEAndFinalUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		assertChatRequest(t, r, true)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"id\":\"c\",\"model\":\"" + testCloudModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"},\"finish_reason\":\"\"}]}\n\n" +
				"data: {\"id\":\"c\",\"model\":\"" + testCloudModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"\"}]}\n\n" +
				"data: {\"id\":\"c\",\"model\":\"" + testCloudModel + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"id\":\"c\",\"model\":\"" + testCloudModel + "\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
				"data: [DONE]\n\n"))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	temp, topP, maxTokens := 0.25, 0.9, 99
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model: testCloudModel,
		Messages: []core.Message{
			{Role: "user", Content: "hello"},
		},
		Temperature: &temp,
		TopP:        &topP,
		MaxTokens:   &maxTokens,
		Tools: []core.Tool{
			{Type: "function", Function: core.Function{Name: "lookup"}},
		},
	})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	var (
		chunks       []core.StreamChunk
		gotStop      bool
		gotContent   string
		gotFirstRole string
		finalUsage   *core.Usage
	)
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
		chunks = append(chunks, chunk)
		for _, choice := range chunk.Choices {
			if gotFirstRole == "" && choice.Delta.Role != "" {
				gotFirstRole = choice.Delta.Role
			}
			gotContent += choice.Delta.Content
			if choice.FinishReason == "stop" {
				gotStop = true
			}
		}
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}
	}

	if len(chunks) != 4 {
		t.Fatalf("chunks length = %d, want 4", len(chunks))
	}
	if gotFirstRole != "assistant" {
		t.Fatalf("first role = %q, want assistant", gotFirstRole)
	}
	if gotContent != "hello" {
		t.Fatalf("assembled content = %q, want hello", gotContent)
	}
	if !gotStop {
		t.Fatal("expected a chunk with finish_reason stop")
	}
	if finalUsage == nil {
		t.Fatal("final usage is nil")
	}
	if finalUsage.PromptTokens != 3 || finalUsage.CompletionTokens != 2 || finalUsage.TotalTokens != 5 {
		t.Fatalf("final usage = %#v, want 3/2/5", *finalUsage)
	}
}

// TestDiscoverModelsHitsV1Models verifies model discovery uses the
// OpenAI-compatible GET /v1/models endpoint with Bearer auth and that the
// discovered IDs feed SupportsModel.
func TestDiscoverModelsHitsV1Models(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testAuthHeader {
			t.Errorf("Authorization = %q, want %s", got, testAuthHeader)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[
				{"id":"gpt-oss:20b","object":"model","created":1730000000,"owned_by":"ollama-cloud"},
				{"id":"qwen3-coder:480b","object":"model","created":1730000001}
			]
		}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{"configured:model"})
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
	if models[0].Created != 1730000000 {
		t.Fatalf("first created = %d, want 1730000000", models[0].Created)
	}
	if models[1].ID != "qwen3-coder:480b" || models[1].OwnedBy != Name {
		t.Fatalf("second model = %#v, want qwen3-coder:480b owned by %s (owned_by fallback)", models[1], Name)
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

func TestEmbedSendsAuthAndMapsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s, want /api/embed", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testAuthHeader {
			t.Errorf("Authorization = %q, want %s", got, testAuthHeader)
		}

		var reqBody struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if reqBody.Model != "embed-model" || reqBody.Input != "hello world" {
			t.Errorf("request = %#v, want embed-model / hello world", reqBody)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"embed-model","embeddings":[[0.1,0.2,0.3]],"prompt_eval_count":5}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "embed-model",
		Input: "hello world",
	})
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}

	if resp.Object != "list" || resp.Model != "embed-model" {
		t.Fatalf("response envelope = %#v, want list / embed-model", resp)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data length = %d, want 1", len(resp.Data))
	}
	if !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2, 0.3}) {
		t.Fatalf("embedding = %#v, want [0.1 0.2 0.3]", resp.Data[0].Embedding)
	}
	if resp.Data[0].Index != 0 || resp.Data[0].Object != "embedding" {
		t.Fatalf("embedding entry = %#v, want index 0 embedding", resp.Data[0])
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v, want 5/5", resp.Usage)
	}
}

func TestEmbedNon200Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = p.Embed(context.Background(), core.EmbeddingRequest{Model: "embed-model", Input: "hi"})
	if err == nil {
		t.Fatal("expected Embed to return an error")
	}
	if got := err.Error(); !strings.Contains(got, "400") || !strings.Contains(got, "model not found") {
		t.Fatalf("error = %q, want status code and response message", got)
	}
}

// TestEmbed_ForwardsDimensions verifies req.Dimensions reaches the native
// /api/embed request body rather than being silently dropped.
func TestEmbed_ForwardsDimensions(t *testing.T) {
	var gotDims *int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Dimensions *int `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotDims = body.Dimensions
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","embeddings":[[0.1]],"prompt_eval_count":1}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dims := 8
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: "m", Input: "hi", Dimensions: &dims}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotDims == nil || *gotDims != 8 {
		t.Errorf("dimensions forwarded = %v, want 8", gotDims)
	}
}
