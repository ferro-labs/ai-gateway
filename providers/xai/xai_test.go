package xai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewXAI(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "xai" {
		t.Errorf("Name() = %q, want xai", p.Name())
	}
	if p.BaseURL() != "https://api.x.ai/v1" {
		t.Errorf("BaseURL() = %q, want https://api.x.ai/v1", p.BaseURL())
	}
}

func TestXAIProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("grok-2-latest") {
		t.Error("expected grok-2-latest to be supported")
	}
	if !p.SupportsModel("GROK-beta") {
		t.Error("expected GROK-beta to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("expected gpt-4o to be unsupported")
	}
}

func TestXAIProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != "Bearer test-key" {
		t.Errorf("AuthHeaders Authorization = %q, want Bearer test-key", headers["Authorization"])
	}
}

func TestXAIProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestXAIProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"grok-2-latest","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "grok-2-latest",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "chatcmpl-1" {
		t.Errorf("Response.ID = %q, want chatcmpl-1", resp.ID)
	}
	if resp.Provider != "xai" {
		t.Errorf("Response.Provider = %q, want xai", resp.Provider)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
}

func TestXAIProvider_GenerateImage_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.ImageProvider = p
}

func TestXAIProvider_GenerateImage_MockHTTP(t *testing.T) {
	respBody := `{"created":1700000000,"data":[{"b64_json":"aGVsbG8=","revised_prompt":"x"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("request path = %q, want /images/generations", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "grok-2-image",
		Prompt: "a red apple",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if resp.Created != 1700000000 {
		t.Errorf("Created = %d, want 1700000000 (from upstream created, not time.Now)", resp.Created)
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected at least one image in response")
	}
	if resp.Data[0].B64JSON != "aGVsbG8=" {
		t.Errorf("Data[0].B64JSON = %q, want aGVsbG8=", resp.Data[0].B64JSON)
	}
	if resp.Data[0].RevisedPrompt != "x" {
		t.Errorf("Data[0].RevisedPrompt = %q, want x", resp.Data[0].RevisedPrompt)
	}
}

func TestXAIProvider_GenerateImage_CreatedFallback(t *testing.T) {
	// Upstream omits "created" (decoded value 0); the provider falls back to
	// the current time so Created is still populated.
	respBody := `{"data":[{"b64_json":"aGVsbG8="}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	before := time.Now().Unix()
	p, _ := New("test-key", srv.URL)
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "grok-2-image",
		Prompt: "a red apple",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if resp.Created < before {
		t.Errorf("Created = %d, want >= %d (time.Now fallback)", resp.Created, before)
	}
}

func TestXAIProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"grok-2-latest\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"grok-2-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"grok-2-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"grok-2-latest\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "grok-2-latest",
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
