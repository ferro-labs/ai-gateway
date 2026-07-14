// Package deepinfra provides a client for the DeepInfra OpenAI-compatible API.
package deepinfra

import (
	"context"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

const (
	// Name is the canonical identifier for the DeepInfra provider.
	// Re-exported as providers.NameDeepInfra in providers/names.go.
	Name           = "deepinfra"
	defaultBaseURL = "https://api.deepinfra.com/v1/openai"
)

// Provider implements the core.Provider interface for DeepInfra.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new DeepInfra provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}
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

// SupportedModels returns a static list of known DeepInfra models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"deepseek-ai/DeepSeek-R1",
		"meta-llama/Meta-Llama-3.1-8B-Instruct",
		"moonshotai/Kimi-K2-Instruct",
		"openai/gpt-oss-120b",
		"Qwen/Qwen3-32B",
	}
}

// SupportsModel returns true for any model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the DeepInfra /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to DeepInfra.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "deepinfra",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to DeepInfra.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "deepinfra",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}
