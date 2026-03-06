package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewHuggingFace(t *testing.T) {
	p, err := NewHuggingFace(testAPIKey, "")
	if err != nil {
		t.Fatalf("NewHuggingFace() error: %v", err)
	}
	if p.Name() != "hugging-face" {
		t.Errorf("Name() = %q, want hugging-face", p.Name())
	}
	if p.BaseURL() != "https://api-inference.huggingface.co/v1" {
		t.Errorf("BaseURL() = %q, want https://api-inference.huggingface.co/v1", p.BaseURL())
	}
}

func TestHuggingFaceProvider_AuthHeaders(t *testing.T) {
	p, _ := NewHuggingFace(testAPIKey, "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestHuggingFaceProvider_Interfaces(_ *testing.T) {
	p, _ := NewHuggingFace(testAPIKey, "")
	var _ StreamProvider = p
	var _ EmbeddingProvider = p
	var _ ImageProvider = p
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

	p, _ := NewHuggingFace(testAPIKey, srv.URL)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "meta-llama/Meta-Llama-3.1-8B-Instruct",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Provider != "hugging-face" {
		t.Errorf("Response.Provider = %q, want hugging-face", resp.Provider)
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

	p, _ := NewHuggingFace(testAPIKey, srv.URL)
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "meta-llama/Meta-Llama-3.1-8B-Instruct",
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

func TestHuggingFaceProvider_Embed_MockHTTP(t *testing.T) {
	embedResp := `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"test-embed","usage":{"prompt_tokens":2,"total_tokens":2}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("request path = %q, want /embeddings", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(embedResp))
	}))
	defer srv.Close()

	p, _ := NewHuggingFace(testAPIKey, srv.URL)
	resp, err := p.Embed(context.Background(), EmbeddingRequest{
		Model: "test-embed",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
}

func TestHuggingFaceProvider_GenerateImage_MockHTTP(t *testing.T) {
	imageResp := `{"created":123,"data":[{"url":"https://example.com/image.png"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("request path = %q, want /images/generations", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(imageResp))
	}))
	defer srv.Close()

	p, _ := NewHuggingFace(testAPIKey, srv.URL)
	resp, err := p.GenerateImage(context.Background(), ImageRequest{
		Model:  "black-forest-labs/FLUX.1-schnell",
		Prompt: "A mountain at sunrise",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
}
