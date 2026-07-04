package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testBearerAPIKey = "Bearer test-key"

func TestNewCloudflare(t *testing.T) {
	p, err := New("test-key", "acct-123", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "cloudflare" {
		t.Errorf("Name() = %q, want cloudflare", p.Name())
	}
	if got := p.BaseURL(); got != "https://api.cloudflare.com/client/v4/accounts/acct-123/ai/v1" {
		t.Errorf("BaseURL() = %q", got)
	}
}

func TestCloudflareProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "@cf/meta/llama-3.1-8b-instruct" {
			found = true
		}
	}
	if !found {
		t.Error("@cf/meta/llama-3.1-8b-instruct not found")
	}
}

func TestCloudflareProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	if !p.SupportsModel("@cf/meta/llama-3.1-8b-instruct") {
		t.Error("expected model to be supported")
	}
	if !p.SupportsModel("@cf/custom/model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestCloudflareProvider_Models(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "cloudflare" {
			t.Errorf("ModelInfo.OwnedBy = %q, want cloudflare", m.OwnedBy)
		}
	}
}

func TestCloudflareProvider_Interfaces(_ *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	var _ core.StreamProvider = p
	var _ core.EmbeddingProvider = p
}

func TestCloudflareProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestCloudflareProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", "", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "@cf/meta/llama-3.1-8b-instruct",
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

func TestCloudflareProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"@cf/meta/llama-3.1-8b-instruct","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", "", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "@cf/meta/llama-3.1-8b-instruct",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "cmpl-1" {
		t.Errorf("Response.ID = %q, want cmpl-1", resp.ID)
	}
}

func TestCloudflareProvider_Embed_MockHTTP(t *testing.T) {
	embedResp := `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"@cf/baai/bge-large-en-v1.5","usage":{"prompt_tokens":2,"total_tokens":2}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Fatalf("path = %q, want suffix /embeddings", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(embedResp))
	}))
	defer srv.Close()

	p, _ := New("test-key", "", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "@cf/baai/bge-large-en-v1.5",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
}

// TestNew_RejectsInvalidBaseURL verifies the constructor fails fast when the base
// URL is not a valid absolute http(s) URL with a host.
func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("test-key", "", "://nope"); err == nil {
		t.Fatal("New() accepted an invalid base URL, want error")
	}
}

// errorServer returns a test server that replies with the given status and an
// OpenAI-shaped error envelope on the expected endpoint suffix.
func errorServer(t *testing.T, suffix string, status int, message string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, suffix) {
			t.Errorf("path = %q, want suffix %s", r.URL.Path, suffix)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"` + message + `"}}`))
	}))
}

// TestCloudflareProvider_Complete_UpstreamError verifies a non-2xx chat response
// surfaces an error carrying both the HTTP status and the upstream message.
func TestCloudflareProvider_Complete_UpstreamError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
	}{
		{"bad-request", http.StatusBadRequest, "invalid model"},
		{"rate-limited", http.StatusTooManyRequests, "slow down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := errorServer(t, "/chat/completions", tc.status, tc.message)
			defer srv.Close()

			p, _ := New("test-key", "", srv.URL)
			_, err := p.Complete(context.Background(), core.Request{
				Model:    "@cf/meta/llama-3.1-8b-instruct",
				Messages: []core.Message{{Role: "user", Content: "Hi"}},
			})
			if err == nil {
				t.Fatal("Complete() error = nil, want upstream error")
			}
			if got := core.ParseStatusCode(err); got != tc.status {
				t.Errorf("ParseStatusCode(err) = %d, want %d", got, tc.status)
			}
			if !strings.Contains(err.Error(), "cloudflare API error") {
				t.Errorf("error = %v, want it to contain %q", err, "cloudflare API error")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error = %v, want it to contain %q", err, tc.message)
			}
		})
	}
}

// TestCloudflareProvider_CompleteStream_UpstreamError verifies a non-2xx streaming
// response is drained and surfaced as an error before any chunk is produced.
func TestCloudflareProvider_CompleteStream_UpstreamError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
	}{
		{"bad-request", http.StatusBadRequest, "invalid model"},
		{"rate-limited", http.StatusTooManyRequests, "slow down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := errorServer(t, "/chat/completions", tc.status, tc.message)
			defer srv.Close()

			p, _ := New("test-key", "", srv.URL)
			ch, err := p.CompleteStream(context.Background(), core.Request{
				Model:    "@cf/meta/llama-3.1-8b-instruct",
				Messages: []core.Message{{Role: "user", Content: "Hi"}},
			})
			if err == nil {
				t.Fatal("CompleteStream() error = nil, want upstream error")
			}
			if ch != nil {
				t.Error("CompleteStream() channel = non-nil, want nil on error")
			}
			if got := core.ParseStatusCode(err); got != tc.status {
				t.Errorf("ParseStatusCode(err) = %d, want %d", got, tc.status)
			}
			if !strings.Contains(err.Error(), "cloudflare API error") {
				t.Errorf("error = %v, want it to contain %q", err, "cloudflare API error")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error = %v, want it to contain %q", err, tc.message)
			}
		})
	}
}

// TestCloudflareProvider_Embed_UpstreamError verifies a non-2xx embeddings
// response surfaces an error carrying the HTTP status and the upstream message.
func TestCloudflareProvider_Embed_UpstreamError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
	}{
		{"bad-request", http.StatusBadRequest, "invalid input"},
		{"rate-limited", http.StatusTooManyRequests, "slow down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := errorServer(t, "/embeddings", tc.status, tc.message)
			defer srv.Close()

			p, _ := New("test-key", "", srv.URL)
			_, err := p.Embed(context.Background(), core.EmbeddingRequest{
				Model: "@cf/baai/bge-large-en-v1.5",
				Input: "hello",
			})
			if err == nil {
				t.Fatal("Embed() error = nil, want upstream error")
			}
			if got := core.ParseStatusCode(err); got != tc.status {
				t.Errorf("ParseStatusCode(err) = %d, want %d", got, tc.status)
			}
			if !strings.Contains(err.Error(), "cloudflare API error") {
				t.Errorf("error = %v, want it to contain %q", err, "cloudflare API error")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error = %v, want it to contain %q", err, tc.message)
			}
		})
	}
}

// TestCloudflareProvider_CompleteStream_ToolCallAndUsage verifies the shared SSE
// decoder surfaces a tool_call delta and the terminal usage chunk.
func TestCloudflareProvider_CompleteStream_ToolCallAndUsage(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"SF\\\"}\"}}]},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", "", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "@cf/meta/llama-3.1-8b-instruct",
		Messages: []core.Message{{Role: "user", Content: "Weather?"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var (
		toolName string
		toolArgs string
		gotUsage *core.Usage
	)
	for c := range ch {
		for _, choice := range c.Choices {
			for _, tcall := range choice.Delta.ToolCalls {
				if tcall.Function.Name != "" {
					toolName = tcall.Function.Name
				}
				toolArgs += tcall.Function.Arguments
			}
		}
		if c.Usage != nil {
			gotUsage = c.Usage
		}
	}

	if toolName != "get_weather" {
		t.Errorf("tool_call name = %q, want get_weather", toolName)
	}
	if toolArgs != `{"city":"SF"}` {
		t.Errorf("tool_call arguments = %q, want %s", toolArgs, `{"city":"SF"}`)
	}
	if gotUsage == nil {
		t.Fatal("usage = nil, want decoded terminal usage chunk")
	}
	if gotUsage.TotalTokens != 15 {
		t.Errorf("usage.TotalTokens = %d, want 15", gotUsage.TotalTokens)
	}
}
