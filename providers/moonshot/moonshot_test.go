package moonshot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testBearerAPIKey = "Bearer test-key"

func TestNewMoonshot(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "moonshot" {
		t.Errorf("Name() = %q, want moonshot", p.Name())
	}
	if p.BaseURL() != "https://api.moonshot.ai/v1" {
		t.Errorf("BaseURL() = %q", p.BaseURL())
	}
}

func TestMoonshotProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "kimi-latest" {
			found = true
		}
	}
	if !found {
		t.Error("kimi-latest not found")
	}
}

func TestMoonshotProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("kimi-latest") {
		t.Error("expected kimi-latest to be supported")
	}
	if !p.SupportsModel("custom-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestMoonshotProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "moonshot" {
			t.Errorf("ModelInfo.OwnedBy = %q, want moonshot", m.OwnedBy)
		}
	}
}

func TestMoonshotProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestMoonshotProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestMoonshotProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"kimi-latest\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"kimi-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"kimi-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"kimi-latest\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "kimi-latest",
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

func TestMoonshotProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"kimi-latest","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "kimi-latest",
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

func TestMoonshotProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key","type":"authentication_error"}}`))
	}))
	defer srv.Close()

	p, _ := New("bad-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "kimi-latest",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete() expected an error for 401 response")
	}
	if code := core.ParseStatusCode(err); code != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode() = %d, want 401", code)
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error = %q, want it to contain the upstream message", err.Error())
	}
}

func TestMoonshotProvider_Complete_ForwardsRequestBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"cmpl-1","model":"kimi-latest","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "kimi-latest",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if gotBody["model"] != "kimi-latest" {
		t.Errorf("forwarded model = %v, want kimi-latest", gotBody["model"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("forwarded messages = %#v, want 1 message", gotBody["messages"])
	}
	msg, _ := msgs[0].(map[string]any)
	if msg["role"] != core.RoleUser || msg["content"] != "Hi" {
		t.Errorf("forwarded message = %#v, want role=user content=Hi", msg)
	}
}

// TestMoonshotProvider_Complete_NestedUsage asserts the shared core.Usage decode
// folds Moonshot's OpenAI-standard nested cache-token accounting
// (usage.prompt_tokens_details.cached_tokens) into CacheReadTokens without any
// provider-local usage decode. See the note on Complete() re: the unverified
// flat-field (top-level usage.cached_tokens) case.
func TestMoonshotProvider_Complete_NestedUsage(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"kimi-latest","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":64}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "kimi-latest",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 20 || resp.Usage.TotalTokens != 120 {
		t.Errorf("Usage = %+v, want prompt=100 completion=20 total=120", resp.Usage)
	}
	if resp.Usage.CacheReadTokens != 64 {
		t.Errorf("Usage.CacheReadTokens = %d, want 64 (from prompt_tokens_details.cached_tokens)", resp.Usage.CacheReadTokens)
	}
}

func TestMoonshotProvider_DiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"kimi-k2.5","object":"model","owned_by":"moonshot"},{"id":"kimi-latest","object":"model"}]}`))
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
	if models[0].ID != "kimi-k2.5" || models[0].OwnedBy != "moonshot" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].OwnedBy != "moonshot" {
		t.Errorf("model[1] owned_by fallback = %q, want moonshot", models[1].OwnedBy)
	}
}

// TestNewMoonshot_RejectsInvalidBaseURL locks in the base-URL validation.
func TestNewMoonshot_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
