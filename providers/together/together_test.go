package together

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testEmbeddingModel = "BAAI/bge-base-en-v1.5"

func TestTogetherProvider_DiscoverModels(t *testing.T) {
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
		// Together AI returns a BARE JSON ARRAY whose items omit owned_by.
		// This is a regression guard for the dual-shape parser + fallback.
		_, _ = w.Write([]byte(`[{"id":"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo","object":"model","created":1700000000},{"id":"Qwen/Qwen2.5-72B-Instruct-Turbo","object":"model"}]`))
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
	if models[0].ID != "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo" || models[0].OwnedBy != "together" {
		t.Errorf("unexpected model[0] (bare-array fallback): %+v", models[0])
	}
	if models[1].ID != "Qwen/Qwen2.5-72B-Instruct-Turbo" || models[1].OwnedBy != "together" {
		t.Errorf("unexpected model[1] (bare-array fallback): %+v", models[1])
	}
}

func TestNewTogether(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "together" {
		t.Errorf("Name() = %q, want together", p.Name())
	}
}

func TestTogetherProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo" {
			found = true
		}
	}
	if !found {
		t.Error("meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo not found")
	}
}

func TestTogetherProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo") {
		t.Error("expected meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo to be supported")
	}
	if !p.SupportsModel("gpt-4o") {
		t.Error("passthrough: expected any model including gpt-4o to return true")
	}
}

func TestTogetherProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "together" {
			t.Errorf("ModelInfo.OwnedBy = %q, want together", m.OwnedBy)
		}
	}
}

func TestTogetherProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestTogetherProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"!\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
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
	if chunks[1].Choices[0].Delta.Content != "Hi" {
		t.Errorf("delta content = %q, want Hi", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != "!" {
		t.Errorf("delta content = %q, want !", chunks[2].Choices[0].Delta.Content)
	}
}

func TestTogetherProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("TOGETHER_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping: TOGETHER_API_KEY not set")
	}

	p, _ := New(apiKey, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Complete(ctx, core.Request{
		Model:     "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
		Messages:  []core.Message{{Role: "user", Content: "Say 'test ok' and nothing else."}},
		MaxTokens: intPtr(10),
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID == "" {
		t.Error("Response ID is empty")
	}
	t.Logf("Response: %+v", resp)
}

func intPtr(i int) *int { return &i }

func TestTogetherProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestTogetherProvider_SupportedModels_Embeddings(t *testing.T) {
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

func TestTogetherProvider_Embed_StringInput_MockHTTP(t *testing.T) {
	testTogetherEmbedSuccess(t, "hello world")
}

func TestTogetherProvider_Embed_StringSliceInput_MockHTTP(t *testing.T) {
	testTogetherEmbedSuccess(t, []string{"hello", "world"})
}

func TestTogetherProvider_Embed_InvalidInput(t *testing.T) {
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

func TestTogetherProvider_Embed_UpstreamError(t *testing.T) {
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

func testTogetherEmbedSuccess(t *testing.T, input any) {
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
		assertTogetherEmbeddingInput(t, body["input"], input)

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

func assertTogetherEmbeddingInput(t *testing.T, got any, want any) {
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
