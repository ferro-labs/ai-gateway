package deepseek

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewDeepSeek(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "deepseek" {
		t.Errorf("Name() = %q, want deepseek", p.Name())
	}
}

func TestNewDeepSeek_InvalidBaseURL(t *testing.T) {
	if _, err := New("test-key", "://bad"); err == nil {
		t.Error("New() with invalid base URL should return an error")
	}
}

func TestNewDeepSeek_TrimsTrailingV1(t *testing.T) {
	p, err := New("test-key", "https://api.deepseek.com/v1")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.BaseURL() != "https://api.deepseek.com" {
		t.Errorf("BaseURL() = %q, want https://api.deepseek.com", p.BaseURL())
	}
}

func TestDeepSeekProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "deepseek-chat" {
			found = true
		}
	}
	if !found {
		t.Error("deepseek-chat not found")
	}
}

func TestDeepSeekProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("deepseek-chat") {
		t.Error("expected deepseek-chat to be supported")
	}
	if !p.SupportsModel("deepseek-reasoner") {
		t.Error("expected deepseek-reasoner to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("deepseek should not support gpt-4o")
	}
}

func TestDeepSeekProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "deepseek" {
			t.Errorf("ModelInfo.OwnedBy = %q, want deepseek", m.OwnedBy)
		}
	}
}

func TestDeepSeekProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestDeepSeekProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "deepseek-chat",
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

func TestDeepSeekProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping: DEEPSEEK_API_KEY not set")
	}

	p, _ := New(apiKey, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Complete(ctx, core.Request{
		Model:     "deepseek-chat",
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

func TestDeepSeekProvider_Complete_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key","type":"authentication_error"}}`))
	}))
	defer srv.Close()

	p, _ := New("bad-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "deepseek-chat",
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

func TestDeepSeekProvider_DiscoverModels(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","owned_by":"deepseek"},{"id":"deepseek-reasoner","object":"model"}]}`))
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
	if models[0].ID != "deepseek-chat" || models[0].OwnedBy != "deepseek" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].OwnedBy != "deepseek" {
		t.Errorf("model[1] owned_by fallback = %q, want deepseek", models[1].OwnedBy)
	}
}

func intPtr(i int) *int { return &i }

// TestDeepSeekProvider_Complete_NormalizesFinishReason verifies the hand-rolled
// non-streaming decode normalizes finish reasons to the canonical OpenAI
// vocabulary, matching the shared streaming path.
func TestDeepSeekProvider_Complete_NormalizesFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"model_length"}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "deepseek-chat",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Choices[0].FinishReason != core.FinishReasonLength {
		t.Errorf("finish_reason = %q, want length (normalized from model_length)", resp.Choices[0].FinishReason)
	}
}
