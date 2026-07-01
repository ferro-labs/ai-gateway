// Package fireworks provides a client for the Fireworks AI API.
package fireworks

import (
	"context"
	"net/http"
	"strings"

	discov "github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical identifier for the Fireworks AI provider.
	// Re-exported as providers.NameFireworks in providers/names.go.
	Name           = "fireworks"
	defaultBaseURL = "https://api.fireworks.ai/inference"
)

// Provider implements the core.Provider interface for Fireworks AI.
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
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Fireworks AI provider.
func New(apiKey, baseURL string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the static list of known Fireworks AI models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"accounts/fireworks/models/llama-v3p1-8b-instruct",
		"accounts/fireworks/models/llama-v3p1-70b-instruct",
		"accounts/fireworks/models/llama-v3p1-405b-instruct",
		"accounts/fireworks/models/llama-v3p2-3b-instruct",
		"accounts/fireworks/models/llama-v3p2-11b-vision-instruct",
		"accounts/fireworks/models/mixtral-8x7b-instruct",
		"accounts/fireworks/models/mixtral-8x22b-instruct",
		"accounts/fireworks/models/firefunction-v2",
		"accounts/fireworks/models/qwen2p5-72b-instruct",
		"accounts/fireworks/models/deepseek-v3",
		"accounts/fireworks/models/qwen3-embedding-0p6b",
		"accounts/fireworks/models/qwen3-embedding-4b",
		"fireworks_ai/nomic-ai/nomic-embed-text-v1",
		"fireworks_ai/nomic-ai/nomic-embed-text-v1.5",
		"fireworks_ai/WhereIsAI/UAE-Large-V1",
		"fireworks_ai/thenlper/gte-base",
		"fireworks_ai/thenlper/gte-large",
	}
}

// SupportsModel returns true if the model is supported by Fireworks AI.
func (p *Provider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Fireworks AI /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discov.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/v1/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to Fireworks AI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "fireworks",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to Fireworks AI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "fireworks",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}
