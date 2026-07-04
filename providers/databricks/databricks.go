// Package databricks provides a client for the Databricks model serving API.
package databricks

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

const (
	// Name is the canonical identifier for the Databricks provider.
	// Re-exported as providers.NameDatabricks in providers/names.go.
	Name = "databricks"
)

// Provider implements the core.Provider interface for Databricks.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Databricks provider.
//
// baseURL should point at the Databricks host or the OpenAI-compatible serving
// path. If a plain workspace host is provided, /serving-endpoints is appended.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("databricks: baseURL is required")
	}
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}
	baseURL = normalizeBaseURL(baseURL)
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
	}, nil
}

func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	baseURL = strings.TrimSuffix(baseURL, "/embeddings")
	baseURL = strings.TrimSuffix(baseURL, "/models")
	if !strings.Contains(baseURL, "/serving-endpoints") {
		baseURL += "/serving-endpoints"
	}
	return baseURL
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known Databricks-hosted foundation model examples.
func (p *Provider) SupportedModels() []string {
	return []string{
		"databricks-claude-sonnet-4-5",
		"databricks-gemini-2-5-pro",
		"databricks-gpt-oss-120b",
		"databricks-llama-4-maverick",
		"databricks-bge-large-en",
		"databricks-gte-large-en",
	}
}

// SupportsModel returns true for any Databricks serving endpoint name.
func (p *Provider) SupportsModel(model string) bool { return strings.TrimSpace(model) != "" }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "databricks",
		Headers: map[string]string{
			"Authorization": "Bearer " + p.apiKey,
			"Content-Type":  "application/json",
		},
	}
}

// Complete sends a chat completion request to Databricks.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// Embed sends an OpenAI-compatible embedding request to Databricks model serving.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	input, err := normalizeEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	req.Input = input
	return openaicompat.PostEmbeddings(ctx, openaicompat.EmbeddingParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/embeddings",
		Label:      "databricks",
		Headers: map[string]string{
			"Authorization": "Bearer " + p.apiKey,
			"Content-Type":  "application/json",
		},
	}, req)
}

// normalizeEmbeddingInput is kept local rather than using
// core.NormalizeEmbeddingInput because it additionally rejects empty or
// whitespace-only strings (per element) — intentional extra strictness.
func normalizeEmbeddingInput(input any) (any, error) {
	switch v := input.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("embed: Input must not be empty")
		}
		return v, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		for i, s := range v {
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("embed: Input[%d] must not be empty", i)
			}
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		strs := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
			}
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("embed: Input[%d] must not be empty", i)
			}
			strs = append(strs, s)
		}
		return strs, nil
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}

// CompleteStream sends a streaming chat completion request to Databricks.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
