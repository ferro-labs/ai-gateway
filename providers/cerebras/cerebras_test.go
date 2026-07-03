package cerebras

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testBearerAPIKey = "Bearer test-key"

func TestNewCerebras(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "cerebras" {
		t.Errorf("Name() = %q, want cerebras", p.Name())
	}
}

func TestCerebrasProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "llama-3.3-70b" {
			found = true
		}
	}
	if !found {
		t.Error("llama-3.3-70b not found")
	}
}

func TestCerebrasProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("llama-3.3-70b") {
		t.Error("expected llama-3.3-70b to be supported")
	}
	if !p.SupportsModel("custom-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestCerebrasProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "cerebras" {
			t.Errorf("ModelInfo.OwnedBy = %q, want cerebras", m.OwnedBy)
		}
	}
}

func TestCerebrasProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestCerebrasProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestCerebrasProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"llama-3.3-70b\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"llama-3.3-70b\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"llama-3.3-70b\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"llama-3.3-70b\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "llama-3.3-70b",
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

func TestCerebrasProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"llama-3.3-70b","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "llama-3.3-70b",
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

func intPtr(i int) *int { return &i }

func float64Ptr(f float64) *float64 { return &f }

// captureCerebrasChatBody runs one Complete against a stub server and returns the
// raw JSON keys the provider sent, so tests can assert the outgoing wire body.
func captureCerebrasChatBody(t *testing.T, req core.Request) map[string]json.RawMessage {
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
		_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","model":"llama-3.3-70b","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	return body
}

// TestCerebrasProvider_Complete_RequestBody asserts the outgoing chat request
// carries the expected method, path, Bearer auth, model, and a sampling param.
func TestCerebrasProvider_Complete_RequestBody(t *testing.T) {
	body := captureCerebrasChatBody(t, core.Request{
		Model:       "llama-3.3-70b",
		Messages:    []core.Message{{Role: "user", Content: "Hi"}},
		Temperature: float64Ptr(0.7),
	})
	if got := string(body["model"]); got != `"llama-3.3-70b"` {
		t.Errorf("model = %s, want \"llama-3.3-70b\"", got)
	}
	if got := string(body["temperature"]); got != "0.7" {
		t.Errorf("temperature = %s, want 0.7", got)
	}
}

// TestCerebrasProvider_Complete_PrefersMaxCompletionTokens verifies that when the
// gateway seam sets both max_tokens and max_completion_tokens, Cerebras forwards
// only max_completion_tokens (the field its chat API validates) and drops the
// legacy max_tokens.
func TestCerebrasProvider_Complete_PrefersMaxCompletionTokens(t *testing.T) {
	body := captureCerebrasChatBody(t, core.Request{
		Model:               "llama-3.3-70b",
		Messages:            []core.Message{{Role: "user", Content: "Hi"}},
		MaxTokens:           intPtr(256),
		MaxCompletionTokens: intPtr(256),
	})
	if _, ok := body["max_tokens"]; ok {
		t.Errorf("max_tokens must not be sent when max_completion_tokens present, body=%v", body)
	}
	if got := string(body["max_completion_tokens"]); got != "256" {
		t.Errorf("max_completion_tokens = %s, want 256", got)
	}
}

// TestCerebrasProvider_Complete_ForwardsMaxTokensWhenAlone verifies a legacy
// request that sets only max_tokens still forwards it (PreferCompletionTokens is
// a no-op when max_completion_tokens is absent).
func TestCerebrasProvider_Complete_ForwardsMaxTokensWhenAlone(t *testing.T) {
	body := captureCerebrasChatBody(t, core.Request{
		Model:     "llama-3.3-70b",
		Messages:  []core.Message{{Role: "user", Content: "Hi"}},
		MaxTokens: intPtr(256),
	})
	if got := string(body["max_tokens"]); got != "256" {
		t.Errorf("max_tokens = %s, want 256 (a max_tokens-only request must still forward it)", got)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Errorf("max_completion_tokens must not appear for a max_tokens-only request, body=%v", body)
	}
}

func TestCerebrasProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "llama-3.3-70b",
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

func TestCerebrasProvider_CompleteStream_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "llama-3.3-70b",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want upstream error")
	}
	if code := core.ParseStatusCode(err); code != http.StatusInternalServerError {
		t.Errorf("ParseStatusCode = %d, want 500 (err=%v)", code, err)
	}
}

func TestCerebrasProvider_DiscoverModels(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"llama-3.3-70b","object":"model","created":1700000000,"owned_by":"Cerebras"},{"id":"qwen-3-32b","object":"model"}]}`))
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
	if models[0].ID != "llama-3.3-70b" || models[0].OwnedBy != "Cerebras" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].OwnedBy != "cerebras" {
		t.Errorf("model[1] owned_by fallback = %q, want cerebras", models[1].OwnedBy)
	}
}
