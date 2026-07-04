package sambanova

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testEmbeddingModel = "E5-Mistral-7B-Instruct"

func float64Ptr(f float64) *float64 { return &f }

// captureSambaNovaChatBody runs one Complete against a stub server and returns the
// raw JSON keys the provider sent, so tests can assert the outgoing wire body.
func captureSambaNovaChatBody(t *testing.T, req core.Request) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","model":"Meta-Llama-3.1-70B-Instruct","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	return body
}

// TestSambaNovaProvider_Complete_RequestBody asserts the outgoing chat request
// carries the expected method, path, Bearer auth, model, and a sampling param.
func TestSambaNovaProvider_Complete_RequestBody(t *testing.T) {
	body := captureSambaNovaChatBody(t, core.Request{
		Model:       "Meta-Llama-3.1-70B-Instruct",
		Messages:    []core.Message{{Role: "user", Content: "Hi"}},
		Temperature: float64Ptr(0.7),
	})
	if got := string(body["model"]); got != `"Meta-Llama-3.1-70B-Instruct"` {
		t.Errorf("model = %s, want \"Meta-Llama-3.1-70B-Instruct\"", got)
	}
	if got := string(body["temperature"]); got != "0.7" {
		t.Errorf("temperature = %s, want 0.7", got)
	}
}

func TestSambaNovaProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "Meta-Llama-3.1-70B-Instruct",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want upstream error")
	}
	if code := core.ParseStatusCode(err); code != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode = %d, want 429 (err=%v)", code, err)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v, want rate limited message", err)
	}
}

func TestSambaNovaProvider_DiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Meta-Llama-3.1-70B-Instruct","object":"model","created":1700000000,"owned_by":"SambaNova"},{"id":"DeepSeek-R1","object":"model"}]}`))
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
	if models[0].ID != "Meta-Llama-3.1-70B-Instruct" || models[0].OwnedBy != "SambaNova" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].OwnedBy != "sambanova" {
		t.Errorf("model[1] owned_by fallback = %q, want sambanova", models[1].OwnedBy)
	}
}

func TestSambaNovaProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestSambaNovaProvider_Embed_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
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
		if got := body["input"]; got != "hello world" {
			t.Errorf("input = %#v, want %q", got, "hello world")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"` + testEmbeddingModel + `","usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: testEmbeddingModel,
		Input: "hello world",
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
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" || resp.Data[0].Index != 0 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("Data[0] = %+v, want mapped embedding at index 0", resp.Data[0])
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.TotalTokens != 3 {
		t.Errorf("Usage = %+v, want prompt=3 total=3", resp.Usage)
	}
}

func TestSambaNovaProvider_Embed_UpstreamError(t *testing.T) {
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
		t.Errorf("error = %v, want rate limited message", err)
	}
}
