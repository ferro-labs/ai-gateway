package vertexai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"golang.org/x/oauth2"
)

const testAPIKey = "test-key"

func TestNewVertexAI_APIKeyMode(t *testing.T) {
	p, err := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    testAPIKey,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "vertex-ai" {
		t.Errorf("Name() = %q, want vertex-ai", p.Name())
	}
	if p.BaseURL() == "" {
		t.Error("BaseURL() should not be empty")
	}
}

func TestNewVertexAI_RequiresProjectID(t *testing.T) {
	_, err := New(Options{Region: "us-central1", APIKey: testAPIKey})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestNewVertexAI_RequiresRegion(t *testing.T) {
	_, err := New(Options{ProjectID: "demo-project", APIKey: testAPIKey})
	if err == nil {
		t.Fatal("expected error for missing region")
	}
}

func TestNewVertexAI_RequiresAuth(t *testing.T) {
	// Force Application Default Credentials discovery to fail deterministically
	// (bogus GOOGLE_APPLICATION_CREDENTIALS path) so the "no auth available"
	// error path is exercised regardless of the host's ambient gcloud/metadata
	// credentials.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "does-not-exist.json"))
	_, err := New(Options{ProjectID: "demo-project", Region: "us-central1"})
	if err == nil {
		t.Fatal("expected error when no api key, service account JSON, or ADC is available")
	}
}

func TestNewVertexAI_ServiceAccountInvalidJSON(t *testing.T) {
	_, err := New(Options{
		ProjectID:          "demo-project",
		Region:             "us-central1",
		ServiceAccountJSON: "{invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid service account JSON")
	}
}

func TestVertexAIProvider_AuthHeaders_APIKey(t *testing.T) {
	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    testAPIKey,
	})
	headers := p.AuthHeaders()
	if headers["x-goog-api-key"] != testAPIKey {
		t.Errorf("x-goog-api-key = %q, want %s", headers["x-goog-api-key"], testAPIKey)
	}
}

func TestVertexAIProvider_SupportedModels(t *testing.T) {
	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    testAPIKey,
	})
	models := p.SupportedModels()
	found := map[string]bool{}
	for _, model := range models {
		found[model] = true
	}
	for _, want := range []string{"gemini-2.5-flash", "text-embedding-005", "textembedding-gecko@003", "gemini-embedding-001"} {
		if !found[want] {
			t.Errorf("SupportedModels() missing %q", want)
		}
		if !p.SupportsModel(want) {
			t.Errorf("SupportsModel(%q) = false, want true", want)
		}
	}
}

func TestVertexAIProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    testAPIKey,
	})
	var _ core.StreamProvider = p
}

func TestVertexAIProvider_Embed_Interface(_ *testing.T) {
	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    testAPIKey,
	})
	var _ core.EmbeddingProvider = p
}

func TestVertexAIProvider_Embed_BatchSuccess_APIKey(t *testing.T) {
	dimensions := 256
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1/projects/demo-project/locations/us-central1/publishers/google/models/text-embedding-005:predict"
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %s", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("x-goog-api-key"); got != testAPIKey {
			t.Errorf("x-goog-api-key = %q, want %s", got, testAPIKey)
		}
		var body struct {
			Instances []struct {
				Content string `json:"content"`
			} `json:"instances"`
			Parameters struct {
				OutputDimensionality *int `json:"outputDimensionality"`
			} `json:"parameters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Instances) != 2 || body.Instances[0].Content != "first" || body.Instances[1].Content != "second" {
			t.Fatalf("instances = %+v, want first/second", body.Instances)
		}
		if body.Parameters.OutputDimensionality == nil || *body.Parameters.OutputDimensionality != dimensions {
			t.Errorf("outputDimensionality = %v, want %d", body.Parameters.OutputDimensionality, dimensions)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"token_count":2}}},{"embeddings":{"values":[0.3,0.4],"statistics":{"token_count":3}}}]}`))
	}))
	defer srv.Close()

	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	p.SetBaseURL(srv.URL + "/v1/projects/demo-project/locations/us-central1/endpoints/openapi")

	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:      "text-embedding-005",
		Input:      []string{"first", "second"},
		Dimensions: &dimensions,
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" || resp.Model != "text-embedding-005" {
		t.Errorf("response metadata = (%q, %q)", resp.Object, resp.Model)
	}
	if len(resp.Data) != 2 || resp.Data[0].Index != 0 || resp.Data[1].Index != 1 {
		t.Fatalf("response data = %+v", resp.Data)
	}
	if resp.Data[0].Embedding[0] != 0.1 || resp.Data[1].Embedding[1] != 0.4 {
		t.Errorf("embeddings = %+v", resp.Data)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v, want 5 prompt/total", resp.Usage)
	}
}

func TestVertexAIProvider_Embed_StringInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/publishers/google/models/textembedding-gecko@003:predict" {
			t.Errorf("request path = %q, want /publishers/google/models/textembedding-gecko@003:predict", r.URL.Path)
		}
		var body struct {
			Instances []struct {
				Content string `json:"content"`
			} `json:"instances"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Instances) != 1 || body.Instances[0].Content != "hello" {
			t.Fatalf("instances = %+v, want hello", body.Instances)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"embeddings":{"values":[1,2,3],"statistics":{"tokenCount":4}}}]}`))
	}))
	defer srv.Close()

	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	p.SetBaseURL(srv.URL)

	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "textembedding-gecko@003",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Embedding[2] != 3 {
		t.Errorf("response data = %+v", resp.Data)
	}
	if resp.Usage.TotalTokens != 4 {
		t.Errorf("usage = %+v, want total_tokens 4", resp.Usage)
	}
}

func TestVertexAIProvider_Embed_InvalidInput(t *testing.T) {
	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "text-embedding-005",
		Input: []any{"ok", 123},
	})
	if err == nil || !strings.Contains(err.Error(), "Input[1]") {
		t.Fatalf("Embed() error = %v, want invalid input error", err)
	}
}

func TestVertexAIProvider_Embed_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad predict request"}}`))
	}))
	defer srv.Close()

	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	p.SetBaseURL(srv.URL)

	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "text-embedding-005",
		Input: "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "bad predict request") {
		t.Fatalf("Embed() error = %v, want upstream error", err)
	}
}

func TestVertexAIProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"gemini-2.5-flash","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	p.SetBaseURL(srv.URL)

	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Provider != "vertex-ai" {
		t.Errorf("Response.Provider = %q, want vertex-ai", resp.Provider)
	}
}

func TestVertexAIProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gemini-2.5-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    "test-key",
	})
	p.SetBaseURL(srv.URL)

	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
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

func TestPredictionEndpointPathEscapesModel(t *testing.T) {
	p, err := New(Options{ProjectID: "proj", Region: "us-central1", APIKey: testAPIKey})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	got := p.predictionEndpoint("imagen/../etc passwd@001")
	if strings.Contains(got, " ") {
		t.Errorf("predictionEndpoint left raw space in URL path: %q", got)
	}
	if !strings.HasSuffix(got, ":predict") {
		t.Errorf("predictionEndpoint dropped :predict suffix: %q", got)
	}
	if !strings.Contains(got, "/publishers/google/models/") {
		t.Errorf("predictionEndpoint malformed path: %q", got)
	}
}

type fakeTokenSource struct {
	token string
	err   error
}

func (f fakeTokenSource) Token() (*oauth2.Token, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &oauth2.Token{AccessToken: f.token}, nil
}

// TestComplete_UsesBearerTokenFromTokenSource covers the service-account / ADC
// auth mode: with no API key, the OAuth token source supplies an Authorization
// Bearer header.
func TestComplete_UsesBearerTokenFromTokenSource(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	p := &Provider{
		name:        Name,
		baseURL:     srv.URL,
		httpClient:  providerhttp.ForProvider(Name),
		tokenSource: fakeTokenSource{token: "tok-123"},
	}
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if authHeader != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", authHeader)
	}
}

// TestComplete_TokenFetchFailureSurfaces verifies a token-source failure surfaces
// as a clear gateway error rather than an unauthenticated request / opaque 401.
func TestComplete_TokenFetchFailureSurfaces(t *testing.T) {
	p := &Provider{
		name:        Name,
		baseURL:     "https://vertex.example",
		httpClient:  providerhttp.ForProvider(Name),
		tokenSource: fakeTokenSource{err: errors.New("metadata unavailable")},
	}
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected token-fetch error")
	}
	if !strings.Contains(err.Error(), "token fetch failed") || !strings.Contains(err.Error(), "metadata unavailable") {
		t.Fatalf("error = %v, want wrapped token-fetch failure", err)
	}
}

// vertexChatModel runs one Complete against a stub and returns the "model" field
// of the request body the provider sent.
func vertexChatModel(t *testing.T, reqModel string) string {
	t.Helper()
	var body struct {
		Model string `json:"model"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.SetBaseURL(srv.URL)
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    reqModel,
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body.Model
}

// TestComplete_PrefixesGooglePublisher verifies a first-party model is sent with
// the required "google/" publisher prefix, and an existing publisher prefix is
// left intact.
func TestComplete_PrefixesGooglePublisher(t *testing.T) {
	if got := vertexChatModel(t, "gemini-2.5-flash"); got != "google/gemini-2.5-flash" {
		t.Errorf("model = %q, want google/gemini-2.5-flash", got)
	}
	if got := vertexChatModel(t, "google/gemini-2.5-flash"); got != "google/gemini-2.5-flash" {
		t.Errorf("already-prefixed model = %q, want unchanged", got)
	}
	if got := vertexChatModel(t, "meta/llama-3.1-405b"); got != "meta/llama-3.1-405b" {
		t.Errorf("non-Google publisher model = %q, want unchanged", got)
	}
}

// TestComplete_ErrorPathReturnsAPIError verifies a non-2xx chat response surfaces
// via core.APIError.
func TestComplete_ErrorPathReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	defer srv.Close()

	p, err := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.SetBaseURL(srv.URL)
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if got := err.Error(); !strings.Contains(got, "quota exceeded") || !strings.Contains(got, "429") {
		t.Fatalf("error = %v, want status + upstream message", err)
	}
}

func TestBuildImagenRequest_MapsSizeToAspectRatio(t *testing.T) {
	cases := map[string]string{
		"1024x1024": "1:1",
		"1792x1024": "16:9",
		"1024x1792": "9:16",
		"640x480":   "",
	}
	for size, want := range cases {
		got := buildImagenRequest(core.ImageRequest{Prompt: "x", Model: "imagen-4.0-generate-001", Size: size})
		var ar string
		if got.Parameters != nil {
			ar = got.Parameters.AspectRatio
		}
		if ar != want {
			t.Errorf("size %q -> aspectRatio %q, want %q", size, ar, want)
		}
	}
}
