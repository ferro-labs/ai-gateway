package xai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestXAIProvider_Complete_NestedUsage verifies xAI's nested usage accounting
// (completion_tokens_details.reasoning_tokens and prompt_tokens_details.cached_tokens)
// is surfaced onto the canonical core.Usage.
func TestXAIProvider_Complete_NestedUsage(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"grok-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28,"completion_tokens_details":{"reasoning_tokens":5},"prompt_tokens_details":{"cached_tokens":12}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "grok-4",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.ReasoningTokens != 5 {
		t.Errorf("Usage.ReasoningTokens = %d, want 5 (nested completion_tokens_details.reasoning_tokens)", resp.Usage.ReasoningTokens)
	}
	if resp.Usage.CacheReadTokens != 12 {
		t.Errorf("Usage.CacheReadTokens = %d, want 12 (nested prompt_tokens_details.cached_tokens)", resp.Usage.CacheReadTokens)
	}
}

// TestXAIProvider_Complete_ErrorStatus verifies the chat error path returns a
// core.APIError carrying the upstream status code and message.
func TestXAIProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "grok-4",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx chat response, got nil")
	}
	if !strings.Contains(err.Error(), "xai API error (400)") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "xai API error (400)")
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Errorf("error = %q, want it to contain upstream message %q", err.Error(), "bad model")
	}
}

// TestXAIProvider_GenerateImage_ErrorStatus verifies the image error path
// returns a core.APIError carrying the upstream status code and message.
func TestXAIProvider_GenerateImage_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "grok-2-image",
		Prompt: "a red apple",
	})
	if err == nil {
		t.Fatal("expected error for non-2xx image response, got nil")
	}
	if !strings.Contains(err.Error(), "xai API error (500)") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "xai API error (500)")
	}
	if !strings.Contains(err.Error(), "upstream boom") {
		t.Errorf("error = %q, want it to contain upstream message %q", err.Error(), "upstream boom")
	}
}

// TestXAIProvider_DiscoverModels verifies live model discovery against an
// OpenAI-compatible /models endpoint.
func TestXAIProvider_DiscoverModels(t *testing.T) {
	respBody := `{"object":"list","data":[{"id":"grok-4","object":"model","owned_by":"xai"},{"id":"grok-3","object":"model","owned_by":"xai"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("request path = %q, want /models", r.URL.Path)
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
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("DiscoverModels() returned %d models, want 2", len(models))
	}
	found := false
	for _, m := range models {
		if m.ID == "grok-4" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DiscoverModels() = %+v, want a model with ID grok-4", models)
	}
}

// TestDroppedImageParams verifies the warn-on-drop helper reports exactly the
// image parameters Grok image models ignore.
func TestDroppedImageParams(t *testing.T) {
	cases := []struct {
		name string
		req  core.ImageRequest
		want []string
	}{
		{"none", core.ImageRequest{}, nil},
		{"size", core.ImageRequest{Size: "1024x1024"}, []string{"size"}},
		{"quality", core.ImageRequest{Quality: "hd"}, []string{"quality"}},
		{"style", core.ImageRequest{Style: "vivid"}, []string{"style"}},
		{"all", core.ImageRequest{Size: "1024x1024", Quality: "hd", Style: "vivid"}, []string{"size", "quality", "style"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := droppedImageParams(tc.req)
			if len(got) != len(tc.want) {
				t.Fatalf("droppedImageParams = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("droppedImageParams[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNewXAI_RejectsInvalidBaseURL locks in the base-URL validation.
func TestNewXAI_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
