package azureopenai

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

func TestAzureOpenAIProvider_CapabilityInterfaces(_ *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	var _ core.EmbeddingProvider = p
	var _ core.ImageProvider = p
}

func TestNewAzureOpenAI(t *testing.T) {
	p, err := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "azure-openai" {
		t.Errorf("Name() = %q, want azure-openai", p.Name())
	}
}

func TestAzureOpenAIProvider_DefaultApiVersion(t *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	if p.APIVersion() != "2024-10-21" {
		t.Errorf("APIVersion() = %q, want 2024-10-21", p.APIVersion())
	}
}

func TestAzureOpenAIProvider_CustomApiVersion(t *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "2024-06-01")
	if p.APIVersion() != "2024-06-01" {
		t.Errorf("APIVersion() = %q, want 2024-06-01", p.APIVersion())
	}
}

func TestAzureOpenAIProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	models := p.SupportedModels()
	if len(models) != 1 {
		t.Fatalf("SupportedModels() returned %d models, want 1", len(models))
	}
	if models[0] != "gpt-4o" {
		t.Errorf("SupportedModels()[0] = %q, want gpt-4o", models[0])
	}
}

func TestAzureOpenAIProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	if !p.SupportsModel("gpt-4o") {
		t.Error("expected gpt-4o to be supported")
	}
	if !p.SupportsModel("gpt-3.5-turbo") {
		t.Error("passthrough: expected any model to return true")
	}
}

func TestAzureOpenAIProvider_Models(t *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	models := p.Models()
	if len(models) != 1 {
		t.Fatalf("Models() returned %d, want 1", len(models))
	}
	if models[0].OwnedBy != "azure-openai" {
		t.Errorf("ModelInfo.OwnedBy = %q, want azure-openai", models[0].OwnedBy)
	}
}

func TestAzureOpenAIProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	var _ core.StreamProvider = p
}

func TestAzureOpenAIProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gpt-4o",
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
	if chunks[2].Choices[0].Delta.Content != " world" {
		t.Errorf("delta content = %q, want ' world'", chunks[2].Choices[0].Delta.Content)
	}
}

// TestAzureOpenAIProvider_Complete_Endpoint asserts the chat path targets the
// configured deployment: /openai/deployments/<deployment>/chat/completions with
// the api-version query and api-key header. The chat path is pinned to the
// configured deployment (not req.Model), so a distinct deployment name proves
// endpoint() is what builds the URL.
func TestAzureOpenAIProvider_Complete_Endpoint(t *testing.T) {
	var gotPath, gotQuery, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "my-chat-deploy", "2024-10-21")
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if gotPath != "/openai/deployments/my-chat-deploy/chat/completions" {
		t.Errorf("path = %q, want /openai/deployments/my-chat-deploy/chat/completions", gotPath)
	}
	if !strings.Contains(gotQuery, "api-version=2024-10-21") {
		t.Errorf("query = %q, want api-version=2024-10-21", gotQuery)
	}
	if gotAPIKey != "test-key" {
		t.Errorf("api-key header = %q, want test-key", gotAPIKey)
	}
}

// TestAzureOpenAIProvider_CompleteStream_Endpoint asserts the streaming chat path
// uses the same configured-deployment endpoint with the api-version query.
func TestAzureOpenAIProvider_CompleteStream_Endpoint(t *testing.T) {
	var gotPath, gotQuery, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "my-chat-deploy", "2024-10-21")
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}
	for range ch { //nolint:revive // drain the stream to completion
	}

	if gotPath != "/openai/deployments/my-chat-deploy/chat/completions" {
		t.Errorf("path = %q, want /openai/deployments/my-chat-deploy/chat/completions", gotPath)
	}
	if !strings.Contains(gotQuery, "api-version=2024-10-21") {
		t.Errorf("query = %q, want api-version=2024-10-21", gotQuery)
	}
	if gotAPIKey != "test-key" {
		t.Errorf("api-key header = %q, want test-key", gotAPIKey)
	}
}

func TestAzureOpenAIProvider_opEndpoint_PathEscapesDeployment(t *testing.T) {
	p, _ := New("test-key", "https://myresource.openai.azure.com", "gpt-4o", "2024-10-21")

	cases := []struct {
		name       string
		deployment string
		wantSeg    string
	}{
		{"space", "my deploy", "/deployments/my%20deploy/"},
		{"slash", "a/b", "/deployments/a%2Fb/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.opEndpoint(tc.deployment, "embeddings")
			if !strings.Contains(got, tc.wantSeg) {
				t.Errorf("opEndpoint(%q) = %q, want it to contain %q", tc.deployment, got, tc.wantSeg)
			}
			// The raw (unescaped) deployment must not leak into the URL path.
			if strings.Contains(got, "/deployments/"+tc.deployment+"/") {
				t.Errorf("opEndpoint(%q) = %q, deployment was not escaped", tc.deployment, got)
			}
		})
	}
}

func TestAzureOpenAIProvider_Embed(t *testing.T) {
	var gotPath, gotQuery, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","model":"text-embedding-3-small","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}

	if gotPath != "/openai/deployments/text-embedding-3-small/embeddings" {
		t.Errorf("path = %q, want /openai/deployments/text-embedding-3-small/embeddings", gotPath)
	}
	if !strings.Contains(gotQuery, "api-version=2024-10-21") {
		t.Errorf("query = %q, want api-version=2024-10-21", gotQuery)
	}
	if gotAPIKey != "test-key" {
		t.Errorf("api-key header = %q, want test-key", gotAPIKey)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Embedding) != 3 {
		t.Fatalf("unexpected embedding data: %+v", resp.Data)
	}
	if resp.Data[0].Embedding[0] != 0.1 {
		t.Errorf("embedding[0] = %v, want 0.1", resp.Data[0].Embedding[0])
	}
	if resp.Usage.TotalTokens != 4 {
		t.Errorf("usage.TotalTokens = %d, want 4", resp.Usage.TotalTokens)
	}
}

func TestAzureOpenAIProvider_Embed_FallbackDeployment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[],"usage":{}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "configured-deployment", "2024-10-21")
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{Input: "x"}); err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if gotPath != "/openai/deployments/configured-deployment/embeddings" {
		t.Errorf("path = %q, want fallback to configured-deployment", gotPath)
	}
}

func TestAzureOpenAIProvider_Embed_InputValidation(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p, _ := New("test-key", srv.URL, "gpt-4o", "2024-10-21")

	cases := []struct {
		name string
		req  core.EmbeddingRequest
	}{
		{"nil input", core.EmbeddingRequest{Input: nil}},
		{"empty string", core.EmbeddingRequest{Input: ""}},
		{"empty slice", core.EmbeddingRequest{Input: []string{}}},
		{"non-string element", core.EmbeddingRequest{Input: []any{1}}},
		{"bad encoding_format", core.EmbeddingRequest{Input: "x", EncodingFormat: "binary"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.Embed(context.Background(), tc.req); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
	if called {
		t.Error("expected no HTTP call on validation failure")
	}
}

func TestAzureOpenAIProvider_Embed_Base64EncodingAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[],"usage":{}}`))
	}))
	defer srv.Close()
	p, _ := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{Input: "x", EncodingFormat: "base64"}); err != nil {
		t.Fatalf("Embed() with base64 error: %v", err)
	}
}

func TestAzureOpenAIProvider_Embed_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad deployment","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()
	p, _ := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{Input: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad deployment") || !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, want wrapped 400 + message", err.Error())
	}
}

func TestAzureOpenAIProvider_GenerateImage(t *testing.T) {
	var gotPath, gotQuery, gotAPIKey string
	var sentModel bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("api-key")
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		_, sentModel = m["model"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1700000000,"data":[{"b64_json":"aGVsbG8=","revised_prompt":"a cat"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:          "dall-e-3",
		Prompt:         "a cat",
		ResponseFormat: "b64_json",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}

	if gotPath != "/openai/deployments/dall-e-3/images/generations" {
		t.Errorf("path = %q, want /openai/deployments/dall-e-3/images/generations", gotPath)
	}
	if !strings.Contains(gotQuery, "api-version=2024-10-21") {
		t.Errorf("query = %q, want api-version=2024-10-21", gotQuery)
	}
	if gotAPIKey != "test-key" {
		t.Errorf("api-key header = %q, want test-key", gotAPIKey)
	}
	if sentModel {
		t.Error("image request body must not contain a model field")
	}
	if resp.Created != 1700000000 {
		t.Errorf("created = %d, want 1700000000", resp.Created)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGVsbG8=" {
		t.Fatalf("unexpected image data: %+v", resp.Data)
	}
	if resp.Data[0].RevisedPrompt != "a cat" {
		t.Errorf("revised_prompt = %q, want 'a cat'", resp.Data[0].RevisedPrompt)
	}
}

func TestAzureOpenAIProvider_GenerateImage_FallbackDeployment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "img-deployment", "2024-10-21")
	if _, err := p.GenerateImage(context.Background(), core.ImageRequest{Prompt: "x"}); err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if gotPath != "/openai/deployments/img-deployment/images/generations" {
		t.Errorf("path = %q, want fallback to img-deployment", gotPath)
	}
}

func TestAzureOpenAIProvider_GenerateImage_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"content policy violation","type":"server_error"}}`))
	}))
	defer srv.Close()
	p, _ := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "content policy violation") || !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want wrapped 500 + message", err.Error())
	}
}

// TestComplete_DecodesResponseWithProvider verifies the chat response is decoded
// (id/model/content/usage) and now carries the provider name (previously
// dropped).
func TestComplete_DecodesResponseWithProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != Name {
		t.Errorf("Provider = %q, want %q", resp.Provider, Name)
	}
	if resp.ID != "chatcmpl-1" || resp.Model != "gpt-4o" {
		t.Errorf("id/model = %q/%q", resp.ID, resp.Model)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("choices = %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestComplete_ErrorPathReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want status + message", err)
	}
}

func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad", "dep", ""); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
