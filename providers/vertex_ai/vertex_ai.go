// Package vertexai provides a client for Google Vertex AI.
package vertexai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

// Name is the canonical provider identifier.
const Name = "vertex-ai"

// Options configures Vertex AI provider initialization.
type Options struct {
	ProjectID          string
	Region             string
	APIKey             string
	ServiceAccountJSON string
}

// Provider implements the Vertex AI API client.
type Provider struct {
	name        string
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	tokenSource oauth2.TokenSource
}

// Compile-time interface assertions.
var (
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.EmbeddingProvider     = (*Provider)(nil)
	_ core.ImageProvider         = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.NonOpenAIWireProvider = (*Provider)(nil)
)

// New creates a new Vertex AI provider.
// Supports API key mode and service-account JSON mode.
func New(opts Options) (*Provider, error) {
	projectID := strings.TrimSpace(opts.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required for vertex-ai provider")
	}
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		return nil, fmt.Errorf("region is required for vertex-ai provider")
	}

	apiKey := strings.TrimSpace(opts.APIKey)
	serviceAccountJSON := strings.TrimSpace(opts.ServiceAccountJSON)

	// context.Background() below is intentional: the token source lives for the
	// whole lifetime of the provider and refreshes OAuth tokens on demand across
	// many requests. It is a construction-time/lifetime construct, not
	// request-scoped, so binding it to any single request's context would
	// wrongly cancel token refresh when that request completes.
	var tokenSource oauth2.TokenSource
	switch {
	case serviceAccountJSON != "":
		cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("invalid Vertex AI service account JSON: %w", err)
		}
		tokenSource = cfg.TokenSource(context.Background())
	case apiKey == "":
		// No API key or service-account JSON: fall back to Application Default
		// Credentials (GOOGLE_APPLICATION_CREDENTIALS, gcloud, workload identity,
		// or the GCE/GKE metadata server) so managed environments authenticate
		// without an explicit key.
		creds, err := google.FindDefaultCredentials(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("vertex-ai requires an API key, service account JSON, or application default credentials: %w", err)
		}
		tokenSource = creds.TokenSource
	}

	baseURL := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/endpoints/openapi", region, projectID, region)
	return &Provider{
		name:        Name,
		apiKey:      apiKey,
		baseURL:     baseURL,
		httpClient:  providerhttp.ForProvider(Name),
		tokenSource: tokenSource,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// SetBaseURL overrides the base URL (used in tests to point to a mock server).
func (p *Provider) SetBaseURL(url string) { p.baseURL = url }

// NonOpenAIWire marks Vertex AI as ineligible for transparent OpenAI-wire proxy
// pass-through: its upstream uses Vertex-native paths and auth (publisher-
// prefixed models, project/location-scoped endpoints), not directly forwardable.
// It remains fully usable via its native translated endpoints. See
// core.NonOpenAIWireProvider.
func (*Provider) NonOpenAIWire() {}

// authHeader returns the single auth header for Vertex AI — the api-key header
// when an API key is configured, otherwise a Bearer token from the token source
// (service-account JSON or Application Default Credentials). It returns a clear
// error when no auth is configured or a token cannot be fetched. All three call
// sites (AuthHeaders, authorizeRequest, chatAuthHeaders) derive from this so the
// precedence logic lives in one place.
func (p *Provider) authHeader() (name, value string, err error) {
	if p.apiKey != "" {
		return "x-goog-api-key", p.apiKey, nil
	}
	if p.tokenSource == nil {
		return "", "", fmt.Errorf("vertex-ai authorization is not configured")
	}
	tok, err := p.tokenSource.Token()
	if err != nil {
		return "", "", fmt.Errorf("vertex-ai token fetch failed: %w", err)
	}
	return "Authorization", "Bearer " + tok.AccessToken, nil
}

// AuthHeaders implements core.ProxiableProvider. It returns an empty map when
// auth cannot be resolved (the proxy seam has no error channel).
func (p *Provider) AuthHeaders() map[string]string {
	name, value, err := p.authHeader()
	if err != nil {
		return map[string]string{}
	}
	return map[string]string{name: value}
}

// SupportedModels returns known Vertex AI model examples.
func (p *Provider) SupportedModels() []string {
	return []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-embedding-001",
		"text-embedding-005",
		"text-embedding-004",
		"text-multilingual-embedding-002",
		"textembedding-gecko@003",
		"textembedding-gecko-multilingual@001",
		"imagen-4.0-generate-001",
		"imagen-4.0-ultra-generate-001",
		"imagen-4.0-fast-generate-001",
		"imagen-3.0-generate-002",
	}
}

// SupportsModel returns true for known Vertex AI chat, text embedding, and image model families.
func (p *Provider) SupportsModel(model string) bool {
	model = vertexAIModelID(model)
	return strings.HasPrefix(model, "gemini-") ||
		strings.HasPrefix(model, "text-embedding-") ||
		strings.HasPrefix(model, "textembedding-gecko") ||
		strings.HasPrefix(model, "text-multilingual-embedding-") ||
		strings.HasPrefix(model, "imagen-")
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type vertexAIEmbeddingRequest struct {
	Instances  []vertexAIEmbeddingInstance  `json:"instances"`
	Parameters *vertexAIEmbeddingParameters `json:"parameters,omitempty"`
}

type vertexAIEmbeddingInstance struct {
	Content string `json:"content"`
}

type vertexAIEmbeddingParameters struct {
	OutputDimensionality *int `json:"outputDimensionality,omitempty"`
}

type vertexAIEmbeddingPrediction struct {
	Embeddings struct {
		Values     []float64 `json:"values"`
		Statistics struct {
			TokenCount      int `json:"token_count"`
			TokenCountCamel int `json:"tokenCount"`
		} `json:"statistics"`
	} `json:"embeddings"`
	Values    []float64 `json:"values"`
	Embedding []float64 `json:"embedding"`
}

type vertexAIEmbeddingResponse struct {
	Predictions []vertexAIEmbeddingPrediction `json:"predictions"`
	Metadata    struct {
		TokenMetadata struct {
			InputTokenCount struct {
				TotalTokens int `json:"totalTokens"`
			} `json:"inputTokenCount"`
		} `json:"tokenMetadata"`
	} `json:"metadata"`
}

func (p *Provider) endpoint() string {
	return p.baseURL + "/chat/completions"
}

func (p *Provider) predictionEndpoint(model string) string {
	baseURL := strings.TrimRight(p.baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/endpoints/openapi")
	return fmt.Sprintf("%s/publishers/google/models/%s:predict", baseURL, url.PathEscape(vertexAIModelID(model)))
}

func (p *Provider) authorizeRequest(req *http.Request) error {
	name, value, err := p.authHeader()
	if err != nil {
		return err
	}
	req.Header.Set(name, value)
	return nil
}

func vertexAIModelID(model string) string {
	model = strings.TrimPrefix(model, "publishers/google/models/")
	model = strings.TrimPrefix(model, "models/")
	return model
}

// vertexAIChatModelID prefixes a first-party model id with the required
// "google/" publisher prefix for the OpenAI-compatible chat endpoint, unless the
// caller already supplied a publisher prefix (Model Garden also proxies
// non-Google publishers).
func vertexAIChatModelID(model string) string {
	id := vertexAIModelID(model)
	if strings.Contains(id, "/") {
		return id
	}
	return "google/" + id
}

// chatAuthHeaders builds the headers for the OpenAI-compatible chat endpoint,
// returning a clear error when an OAuth token cannot be fetched instead of
// letting a missing Authorization header surface as an opaque upstream 401.
func (p *Provider) chatAuthHeaders() (map[string]string, error) {
	name, value, err := p.authHeader()
	if err != nil {
		return nil, err
	}
	return map[string]string{"Content-Type": "application/json", name: value}, nil
}

func isVertexAITextEmbeddingModel(model string) bool {
	model = vertexAIModelID(model)
	return model == "gemini-embedding-001" ||
		strings.HasPrefix(model, "text-embedding-") ||
		strings.HasPrefix(model, "textembedding-gecko") ||
		strings.HasPrefix(model, "text-multilingual-embedding-")
}

func vertexAIEmbeddingValues(prediction vertexAIEmbeddingPrediction) ([]float64, int) {
	values := prediction.Embeddings.Values
	if values == nil {
		values = prediction.Values
	}
	if values == nil {
		values = prediction.Embedding
	}
	tokenCount := prediction.Embeddings.Statistics.TokenCount
	if tokenCount == 0 {
		tokenCount = prediction.Embeddings.Statistics.TokenCountCamel
	}
	return values, tokenCount
}

// doPredict posts body to the model's :predict endpoint and returns the raw
// response body, applying auth and mapping a non-200 status to core.APIError.
// label ("embed"/"image") is woven into error messages.
func (p *Provider) doPredict(ctx context.Context, model string, body any, label string) ([]byte, error) {
	bodyReader, _, release, err := core.JSONBodyReader(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s request: %w", label, err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.predictionEndpoint(model), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s request: %w", label, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := p.authorizeRequest(httpReq); err != nil {
		return nil, err
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", label, err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s response: %w", label, err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("vertex ai "+label, httpResp.StatusCode, respBody)
	}
	return respBody, nil
}

// Embed sends a text embedding request to Vertex AI's publisher model predict endpoint.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if !isVertexAITextEmbeddingModel(req.Model) {
		return nil, fmt.Errorf("embed: unsupported Vertex AI text embedding model %q", req.Model)
	}
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	texts, err := core.CoerceEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}

	vertexReq := vertexAIEmbeddingRequest{
		Instances: make([]vertexAIEmbeddingInstance, 0, len(texts)),
	}
	for _, text := range texts {
		vertexReq.Instances = append(vertexReq.Instances, vertexAIEmbeddingInstance{Content: text})
	}
	if req.Dimensions != nil {
		vertexReq.Parameters = &vertexAIEmbeddingParameters{OutputDimensionality: req.Dimensions}
	}

	respBody, err := p.doPredict(ctx, req.Model, vertexReq, "embed")
	if err != nil {
		return nil, err
	}

	var vertexResp vertexAIEmbeddingResponse
	if err := json.Unmarshal(respBody, &vertexResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embed response: %w", err)
	}
	if len(vertexResp.Predictions) != len(texts) {
		return nil, fmt.Errorf("vertex ai embed API returned %d embeddings for %d inputs", len(vertexResp.Predictions), len(texts))
	}

	data := make([]core.Embedding, len(vertexResp.Predictions))
	promptTokens := vertexResp.Metadata.TokenMetadata.InputTokenCount.TotalTokens
	statisticsTokens := 0
	for i, prediction := range vertexResp.Predictions {
		values, tokenCount := vertexAIEmbeddingValues(prediction)
		data[i] = core.Embedding{
			Object:    "embedding",
			Embedding: values,
			Index:     i,
		}
		statisticsTokens += tokenCount
	}
	if promptTokens == 0 {
		promptTokens = statisticsTokens
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: promptTokens,
			TotalTokens:  promptTokens,
		},
	}, nil
}

// Complete sends a chat completion request to Vertex AI's OpenAI-compatible
// endpoint. The model id is normalized to the required "google/<id>" publisher
// form before forwarding.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	req.Model = vertexAIChatModelID(req.Model)
	headers, err := p.chatAuthHeaders()
	if err != nil {
		return nil, err
	}
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.endpoint(),
		Headers:    headers,
		Provider:   p.name,
		Label:      "vertex ai",
	}, req)
}

// CompleteStream sends a streaming chat completion request to Vertex AI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	req.Model = vertexAIChatModelID(req.Model)
	headers, err := p.chatAuthHeaders()
	if err != nil {
		return nil, err
	}
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.endpoint(),
		Headers:    headers,
		Provider:   p.name,
		Label:      "vertex ai",
	}, req)
}
