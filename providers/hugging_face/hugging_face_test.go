package huggingface

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	testAPIKey              = "test-key"
	testBearerAPIKey        = "Bearer test-key"
	testChatCompletionsPath = "/chat/completions"
)

func TestNewHuggingFace(t *testing.T) {
	p, err := New(testAPIKey, "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "hugging-face" {
		t.Errorf("Name() = %q, want hugging-face", p.Name())
	}
	if p.BaseURL() != "https://router.huggingface.co/v1" {
		t.Errorf("BaseURL() = %q, want https://router.huggingface.co/v1", p.BaseURL())
	}
}

func TestHuggingFaceProvider_AuthHeaders(t *testing.T) {
	p, _ := New(testAPIKey, "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestHuggingFaceProvider_Interfaces(_ *testing.T) {
	p, _ := New(testAPIKey, "")
	var _ core.StreamProvider = p
	var _ core.EmbeddingProvider = p
	var _ core.ImageProvider = p
}

func TestHuggingFaceProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"meta-llama/Meta-Llama-3.1-8B-Instruct","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testChatCompletionsPath {
			t.Errorf("request path = %q, want %s", r.URL.Path, testChatCompletionsPath)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "meta-llama/Meta-Llama-3.1-8B-Instruct",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Provider != "hugging-face" {
		t.Errorf("Response.Provider = %q, want hugging-face", resp.Provider)
	}
}

func TestHuggingFaceProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "does-not-exist",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want non-nil on HTTP 400")
	}
	if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
		t.Errorf("ParseStatusCode = %d, want %d (err: %v)", got, http.StatusBadRequest, err)
	}
}

func TestHuggingFaceProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "meta-llama/Meta-Llama-3.1-8B-Instruct",
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

func TestHuggingFaceProvider_DiscoverModels_MockHTTP(t *testing.T) {
	listBody := `{"object":"list","data":[{"id":"model-a","owned_by":"acme"},{"id":"model-b"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("request path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(listBody))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models length = %d, want 2", len(models))
	}
	if models[0].ID != "model-a" {
		t.Errorf("models[0].ID = %q, want model-a", models[0].ID)
	}
}

func TestHuggingFaceProvider_Embed_Single(t *testing.T) {
	const wantPath = "/hf-inference/models/test-embed/pipeline/feature-extraction"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %s", r.URL.Path, wantPath)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"inputs":"hello"`) {
			t.Errorf("request body = %q, want to contain inputs:hello", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[0.1,0.2,0.3]`))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "test-embed",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
	if len(resp.Data[0].Embedding) != 3 {
		t.Errorf("embedding length = %d, want 3", len(resp.Data[0].Embedding))
	}
	if resp.Data[0].Object != "embedding" || resp.Data[0].Index != 0 {
		t.Errorf("embedding meta = {%q,%d}, want {embedding,0}", resp.Data[0].Object, resp.Data[0].Index)
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("Usage.TotalTokens = %d, want 0", resp.Usage.TotalTokens)
	}
}

func TestHuggingFaceProvider_Embed_Batch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[[0.1,0.2],[0.3,0.4]]`))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "test-embed",
		Input: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("Data length = %d, want 2", len(resp.Data))
	}
	if resp.Data[1].Index != 1 {
		t.Errorf("Data[1].Index = %d, want 1", resp.Data[1].Index)
	}
}

func TestHuggingFaceProvider_Embed_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"model loading"}}`))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: "test-embed", Input: "hello"})
	if err == nil {
		t.Fatal("Embed() error = nil, want non-nil on HTTP 503")
	}
	if got := core.ParseStatusCode(err); got != http.StatusServiceUnavailable {
		t.Errorf("ParseStatusCode = %d, want %d (err: %v)", got, http.StatusServiceUnavailable, err)
	}
}

func TestHuggingFaceProvider_GenerateImage_MockHTTP(t *testing.T) {
	const wantPath = "/hf-inference/models/black-forest-labs/FLUX.1-schnell"
	rawImage := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %s", r.URL.Path, wantPath)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"inputs":"A mountain at sunrise"`) {
			t.Errorf("request body = %q, want to contain the prompt", string(body))
		}
		if !strings.Contains(string(body), `"width":512`) || !strings.Contains(string(body), `"height":512`) {
			t.Errorf("request body = %q, want width/height parameters", string(body))
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rawImage)
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "black-forest-labs/FLUX.1-schnell",
		Prompt: "A mountain at sunrise",
		Size:   "512x512",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
	want := base64.StdEncoding.EncodeToString(rawImage)
	if resp.Data[0].B64JSON != want {
		t.Errorf("B64JSON = %q, want %q", resp.Data[0].B64JSON, want)
	}
}

func TestHuggingFaceProvider_GenerateImage_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
	}))
	defer srv.Close()

	p, _ := New(testAPIKey, srv.URL)
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "black-forest-labs/FLUX.1-schnell",
		Prompt: "A mountain at sunrise",
	})
	if err == nil {
		t.Fatal("GenerateImage() error = nil, want non-nil on HTTP 401")
	}
	if got := core.ParseStatusCode(err); got != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want %d (err: %v)", got, http.StatusUnauthorized, err)
	}
}
