package mistral

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testEmbeddingModel = "mistral-embed"

func TestMistralProvider_DiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mistral-large-latest","object":"model","created":1700000000,"owned_by":"mistralai"},{"id":"codestral-latest","object":"model"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "mistral-large-latest" || models[0].OwnedBy != "mistralai" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].ID != "codestral-latest" || models[1].OwnedBy != "mistral" {
		t.Errorf("model[1] owned_by fallback = %q, want mistral", models[1].OwnedBy)
	}
}

func TestNewMistral(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "mistral" {
		t.Errorf("Name() = %q, want mistral", p.Name())
	}
}

func TestMistralProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "mistral-large-latest" {
			found = true
		}
	}
	if !found {
		t.Error("mistral-large-latest not found")
	}
}

func TestMistralProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("mistral-large-latest") {
		t.Error("expected mistral-large-latest to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("mistral should not support gpt-4o")
	}
}

func TestMistralProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "mistral" {
			t.Errorf("ModelInfo.OwnedBy = %q, want mistral", m.OwnedBy)
		}
	}
}

func TestMistralProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestMistralProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "mistral-large-latest",
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

func TestMistralProvider_Complete_Success_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if got := body["model"]; got != "mistral-large-latest" {
			t.Errorf("model = %v, want mistral-large-latest", got)
		}
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 1 {
			t.Fatalf("messages = %#v, want 1-element array", body["messages"])
		}
		if m0, _ := msgs[0].(map[string]any); m0["role"] != "user" || m0["content"] != "Hi" {
			t.Errorf("messages[0] = %#v, want {role:user, content:Hi}", msgs[0])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// finish_reason "model_length" exercises the shared normalization to "length".
		_, _ = w.Write([]byte(`{"id":"chatcmpl-abc","object":"chat.completion","model":"mistral-large-latest","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"model_length"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "mistral-large-latest",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "chatcmpl-abc" {
		t.Errorf("ID = %q, want chatcmpl-abc", resp.ID)
	}
	if resp.Model != "mistral-large-latest" {
		t.Errorf("Model = %q, want mistral-large-latest", resp.Model)
	}
	if resp.Provider != "mistral" {
		t.Errorf("Provider = %q, want mistral", resp.Provider)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("Choices length = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("Choices[0].Message.Content = %q, want Hello!", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "length" {
		t.Errorf("Choices[0].FinishReason = %q, want length (normalized from model_length)", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 7 {
		t.Errorf("Usage = %+v, want prompt=5 completion=2 total=7", resp.Usage)
	}
}

func TestMistralProvider_Complete_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid model","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "bogus-model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want upstream error")
	}
	if !strings.Contains(err.Error(), "invalid model") {
		t.Fatalf("error = %v, want invalid model message", err)
	}
}

func TestMistralProvider_Complete_SeedRewrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if _, ok := body["seed"]; ok {
			t.Errorf("body contains %q, want it suppressed in favor of random_seed", "seed")
		}
		if got := body["random_seed"]; got != float64(42) {
			t.Errorf("random_seed = %#v, want 42", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"mistral-large-latest","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	seed := int64(42)
	p, _ := New("test-key", srv.URL)
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    "mistral-large-latest",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
		Seed:     &seed,
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
}

func TestMistralProvider_Embed_DimensionsRewrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if _, ok := body["dimensions"]; ok {
			t.Errorf("body contains %q, want it renamed to output_dimension", "dimensions")
		}
		if got := body["output_dimension"]; got != float64(256) {
			t.Errorf("output_dimension = %#v, want 256", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"` + testEmbeddingModel + `","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer srv.Close()

	dims := 256
	p, _ := New("test-key", srv.URL)
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:      testEmbeddingModel,
		Input:      "hello",
		Dimensions: &dims,
	}); err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
}

func TestMistralProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestMistralProvider_SupportedModels_Embeddings(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	found := false
	for _, m := range models {
		if m == testEmbeddingModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("embedding model %q not found in SupportedModels()", testEmbeddingModel)
	}
	if !p.SupportsModel(testEmbeddingModel) {
		t.Fatalf("SupportsModel(%q) = false, want true", testEmbeddingModel)
	}
}

func TestMistralProvider_Embed_StringInput_MockHTTP(t *testing.T) {
	testMistralEmbedSuccess(t, "hello world")
}

func TestMistralProvider_Embed_StringSliceInput_MockHTTP(t *testing.T) {
	testMistralEmbedSuccess(t, []string{"hello", "world"})
}

func TestMistralProvider_Embed_InvalidInput(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	badInputs := []struct {
		name  string
		input any
	}{
		{"nil", nil},
		{"integer", 42},
		{"empty-string-slice", []string{}},
		{"empty-interface-slice", []any{}},
		{"non-string-array-member", []any{"ok", 42}},
	}
	for _, tc := range badInputs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Embed(context.Background(), core.EmbeddingRequest{
				Model: testEmbeddingModel,
				Input: tc.input,
			})
			if err == nil {
				t.Fatalf("Embed() error = nil, want error")
			}
		})
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestMistralProvider_Embed_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q, want /v1/embeddings", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: testEmbeddingModel,
		Input: "hello",
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want upstream error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v, want rate limited message", err)
	}
}

func testMistralEmbedSuccess(t *testing.T, input any) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q, want /v1/embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if got := body["model"]; got != testEmbeddingModel {
			t.Errorf("model = %v, want %s", got, testEmbeddingModel)
		}
		assertMistralEmbeddingInput(t, body["input"], input)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0},{"object":"embedding","embedding":[0.3,0.4],"index":1}],"model":"` + testEmbeddingModel + `","usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: testEmbeddingModel,
		Input: input,
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("Object = %q, want list", resp.Object)
	}
	if resp.Model != testEmbeddingModel {
		t.Errorf("Model = %q, want %s", resp.Model, testEmbeddingModel)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("Data length = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" || resp.Data[0].Index != 0 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("Data[0] = %+v, want mapped embedding at index 0", resp.Data[0])
	}
	if resp.Data[1].Index != 1 || !reflect.DeepEqual(resp.Data[1].Embedding, []float64{0.3, 0.4}) {
		t.Errorf("Data[1] = %+v, want mapped embedding at index 1", resp.Data[1])
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.TotalTokens != 3 {
		t.Errorf("Usage = %+v, want prompt=3 total=3", resp.Usage)
	}
}

func assertMistralEmbeddingInput(t *testing.T, got any, want any) {
	t.Helper()

	switch w := want.(type) {
	case string:
		if got != w {
			t.Fatalf("input = %#v, want %q", got, w)
		}
	case []string:
		arr, ok := got.([]any)
		if !ok {
			t.Fatalf("input type = %T, want JSON array", got)
		}
		if len(arr) != len(w) {
			t.Fatalf("input length = %d, want %d", len(arr), len(w))
		}
		for i := range w {
			if arr[i] != w[i] {
				t.Fatalf("input[%d] = %#v, want %q", i, arr[i], w[i])
			}
		}
	default:
		t.Fatalf("unsupported test input type %T", want)
	}
}
