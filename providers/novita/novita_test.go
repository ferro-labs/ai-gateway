package novita

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

const (
	testBearerAPIKey   = "Bearer test-key"
	testEmbeddingModel = "baai/bge-m3"
)

func TestNewNovita(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "novita" {
		t.Errorf("Name() = %q, want novita", p.Name())
	}
}

func TestNovitaProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "deepseek/deepseek-v3.2" {
			found = true
		}
	}
	if !found {
		t.Error("deepseek/deepseek-v3.2 not found")
	}
}

func TestNovitaProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("deepseek/deepseek-v3.2") {
		t.Error("expected deepseek/deepseek-v3.2 to be supported")
	}
	if !p.SupportsModel("custom-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestNovitaProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "novita" {
			t.Errorf("ModelInfo.OwnedBy = %q, want novita", m.OwnedBy)
		}
	}
}

func TestNovitaProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestNovitaProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestNovitaProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"deepseek/deepseek-v3.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"deepseek/deepseek-v3.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"deepseek/deepseek-v3.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"deepseek/deepseek-v3.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "deepseek/deepseek-v3.2",
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

func TestNovitaProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"deepseek/deepseek-v3.2","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "deepseek/deepseek-v3.2",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "cmpl-1" {
		t.Errorf("Response.ID = %q, want cmpl-1", resp.ID)
	}
	if len(resp.Choices) == 0 {
		t.Error("expected at least one choice")
	}
}

func TestNovitaProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestNovitaProvider_SupportedModels_Embeddings(t *testing.T) {
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

func TestNovitaProvider_Embed_StringInput_MockHTTP(t *testing.T) {
	testNovitaEmbedSuccess(t, "hello world")
}

func TestNovitaProvider_Embed_StringSliceInput_MockHTTP(t *testing.T) {
	testNovitaEmbedSuccess(t, []string{"hello", "world"})
}

func TestNovitaProvider_Embed_InvalidInput(t *testing.T) {
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

func TestNovitaProvider_Embed_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
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

func testNovitaEmbedSuccess(t *testing.T, input any) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
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
		assertNovitaEmbeddingInput(t, body["input"], input)

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

func assertNovitaEmbeddingInput(t *testing.T, got any, want any) {
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
