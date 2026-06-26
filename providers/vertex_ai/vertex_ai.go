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
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
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
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
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
	if apiKey == "" && serviceAccountJSON == "" {
		return nil, fmt.Errorf("either api key or service account JSON is required for vertex-ai provider")
	}

	var tokenSource oauth2.TokenSource
	if serviceAccountJSON != "" {
		cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("invalid Vertex AI service account JSON: %w", err)
		}
		// context.Background() is intentional: this token source lives for the
		// whole lifetime of the provider and refreshes OAuth tokens on demand
		// across many requests. It is a construction-time/lifetime construct,
		// not request-scoped, so binding it to any single request's context
		// would wrongly cancel token refresh when that request completes.
		tokenSource = cfg.TokenSource(context.Background())
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

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	if p.apiKey != "" {
		return map[string]string{"x-goog-api-key": p.apiKey}
	}
	if p.tokenSource == nil {
		return map[string]string{}
	}
	tok, err := p.tokenSource.Token()
	if err != nil {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + tok.AccessToken}
}

// SupportedModels returns known Vertex AI model examples.
func (p *Provider) SupportedModels() []string {
	return []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.0-flash",
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

type vertexAIResponse struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

type vertexAIError struct {
	Error struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
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
	if p.apiKey != "" {
		req.Header.Set("x-goog-api-key", p.apiKey)
		return nil
	}
	if p.tokenSource == nil {
		return fmt.Errorf("vertex-ai authorization is not configured")
	}
	tok, err := p.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("vertex-ai token fetch failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return nil
}

func vertexAIEmbeddingInputs(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		texts := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
			}
			texts = append(texts, s)
		}
		return texts, nil
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}

func vertexAIModelID(model string) string {
	model = strings.TrimPrefix(model, "publishers/google/models/")
	model = strings.TrimPrefix(model, "models/")
	return model
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

// Embed sends a text embedding request to Vertex AI's publisher model predict endpoint.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if !isVertexAITextEmbeddingModel(req.Model) {
		return nil, fmt.Errorf("embed: unsupported Vertex AI text embedding model %q", req.Model)
	}
	if req.EncodingFormat != "" && req.EncodingFormat != "float" {
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; valid value is \"float\"", req.EncodingFormat)
	}
	texts, err := vertexAIEmbeddingInputs(req.Input)
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

	bodyReader, _, release, err := core.JSONBodyReader(vertexReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.predictionEndpoint(req.Model), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create embed request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := p.authorizeRequest(httpReq); err != nil {
		return nil, err
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embed response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp vertexAIError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("vertex ai embed API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("vertex ai embed API error (%d): %s", httpResp.StatusCode, string(respBody))
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

// Complete sends a chat completion request to Vertex AI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	bodyReader, _, release, err := openaicompat.BuildBody(req, false)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := p.authorizeRequest(httpReq); err != nil {
		return nil, err
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp vertexAIError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var vertexResp vertexAIResponse
	if err := json.Unmarshal(respBody, &vertexResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &core.Response{
		ID:       vertexResp.ID,
		Model:    vertexResp.Model,
		Provider: p.name,
		Choices:  vertexResp.Choices,
		Usage:    vertexResp.Usage,
	}, nil
}

// CompleteStream sends a streaming chat completion request to Vertex AI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	bodyReader, _, release, err := openaicompat.BuildBody(req, true)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := p.authorizeRequest(httpReq); err != nil {
		return nil, err
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp vertexAIError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	return openaicompat.StreamSSE(httpResp.Body), nil
}
