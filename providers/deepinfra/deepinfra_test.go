package deepinfra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testBearerAPIKey = "Bearer test-key"

const testEmbeddingModel = "BAAI/bge-base-en-v1.5"

func TestNewDeepInfra(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "deepinfra" {
		t.Errorf("Name() = %q, want deepinfra", p.Name())
	}
	if p.BaseURL() != "https://api.deepinfra.com/v1/openai" {
		t.Errorf("BaseURL() = %q", p.BaseURL())
	}
}

func TestDeepInfraProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "deepseek-ai/DeepSeek-R1" {
			found = true
		}
	}
	if !found {
		t.Error("deepseek-ai/DeepSeek-R1 not found")
	}
}

func TestDeepInfraProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("deepseek-ai/DeepSeek-R1") {
		t.Error("expected deepseek-ai/DeepSeek-R1 to be supported")
	}
	if !p.SupportsModel("custom-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestDeepInfraProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "deepinfra" {
			t.Errorf("ModelInfo.OwnedBy = %q, want deepinfra", m.OwnedBy)
		}
	}
}

func TestDeepInfraProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestDeepInfraProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestDeepInfraProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"deepseek-ai/DeepSeek-R1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"deepseek-ai/DeepSeek-R1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"deepseek-ai/DeepSeek-R1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"deepseek-ai/DeepSeek-R1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "deepseek-ai/DeepSeek-R1",
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

func TestDeepInfraProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"deepseek-ai/DeepSeek-R1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "deepseek-ai/DeepSeek-R1",
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

func TestDeepInfraProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestDeepInfraProvider_Embed_MockHTTP(t *testing.T) {
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
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != testEmbeddingModel {
			t.Errorf("model = %v, want %s", body["model"], testEmbeddingModel)
		}
		if body["input"] != "hello world" {
			t.Errorf("input = %v, want hello world", body["input"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"` + testEmbeddingModel + `","usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: testEmbeddingModel, Input: "hello world"})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" || resp.Model != testEmbeddingModel {
		t.Errorf("resp = %+v, want list/%s", resp, testEmbeddingModel)
	}
	if len(resp.Data) != 1 || resp.Data[0].Index != 0 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("Data = %+v, want one embedding at index 0", resp.Data)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.TotalTokens != 3 {
		t.Errorf("Usage = %+v, want prompt=3 total=3", resp.Usage)
	}
}

func TestDeepInfraProvider_Embed_InvalidEncodingFormat(t *testing.T) {
	p, _ := New("test-key", "http://127.0.0.1:0")
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:          testEmbeddingModel,
		Input:          "hi",
		EncodingFormat: "base64",
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want unsupported encoding_format error")
	}
}

const (
	testChatModel = "deepseek-ai/DeepSeek-R1"
	testChatPath  = "/chat/completions"
)

// TestNew_RejectsInvalidBaseURL verifies the constructor fails fast when the base
// URL is not a valid absolute http(s) URL with a host.
func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("test-key", "://nope"); err == nil {
		t.Fatal("New() accepted an invalid base URL, want error")
	}
}

// TestDeepInfraProvider_Complete_ForwardsRequest verifies the outbound chat
// request shape: a POST to /chat/completions carrying bearer auth and the
// forwarded model, messages, and temperature.
func TestDeepInfraProvider_Complete_ForwardsRequest(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"` + testChatModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != testChatPath {
			t.Errorf("path = %q, want %s", r.URL.Path, testChatPath)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if got := body["model"]; got != testChatModel {
			t.Errorf("model = %v, want %s", got, testChatModel)
		}
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) == 0 {
			t.Errorf("messages = %#v, want non-empty array", body["messages"])
		}
		if got, ok := body["temperature"].(float64); !ok || got != 0.7 {
			t.Errorf("temperature = %#v, want 0.7", body["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	temperature := 0.7
	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:       testChatModel,
		Messages:    []core.Message{{Role: "user", Content: "Hi"}},
		Temperature: &temperature,
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "cmpl-1" {
		t.Errorf("Response.ID = %q, want cmpl-1", resp.ID)
	}
}

// TestDeepInfraProvider_Complete_UpstreamError verifies a non-2xx chat response
// surfaces an error carrying both the HTTP status and the upstream message.
func TestDeepInfraProvider_Complete_UpstreamError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
	}{
		{"rate-limited", http.StatusTooManyRequests, "slow down"},
		{"server-error", http.StatusInternalServerError, "internal boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != testChatPath {
					t.Errorf("path = %q, want %s", r.URL.Path, testChatPath)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"` + tc.message + `"}}`))
			}))
			defer srv.Close()

			p, _ := New("test-key", srv.URL)
			_, err := p.Complete(context.Background(), core.Request{
				Model:    testChatModel,
				Messages: []core.Message{{Role: "user", Content: "Hi"}},
			})
			if err == nil {
				t.Fatal("Complete() error = nil, want upstream error")
			}
			if got := core.ParseStatusCode(err); got != tc.status {
				t.Errorf("ParseStatusCode(err) = %d, want %d", got, tc.status)
			}
			if !strings.Contains(err.Error(), "deepinfra API error") {
				t.Errorf("error = %v, want it to contain %q", err, "deepinfra API error")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error = %v, want it to contain %q", err, tc.message)
			}
		})
	}
}

// TestDeepInfraProvider_DiscoverModels verifies live discovery issues a GET to
// /models with bearer auth and parses the returned model metadata.
func TestDeepInfraProvider_DiscoverModels(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"` + testChatModel + `","object":"model","created":1700000000,"owned_by":"deepinfra"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != testChatModel {
		t.Errorf("model[0].ID = %q, want %s", models[0].ID, testChatModel)
	}
	if models[0].OwnedBy != "deepinfra" {
		t.Errorf("model[0].OwnedBy = %q, want deepinfra", models[0].OwnedBy)
	}
}
