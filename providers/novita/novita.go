// Package novita provides a client for the Novita OpenAI-compatible API.
package novita

import (
	"context"
	"net/http"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

const (
	// Name is the canonical identifier for the Novita provider.
	// Re-exported as providers.NameNovita in providers/names.go.
	Name           = "novita"
	defaultBaseURL = "https://api.novita.ai/openai/v1"
)

// Provider implements the core.Provider interface for Novita.
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
	_ core.DiscoveryProvider = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.BatchProvider     = (*Provider)(nil)
)

// New creates a new Novita provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL, err := core.ResolveAPIRoot(Name, baseURL, defaultBaseURL)
	if err != nil {
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

// SupportsModel returns true for any model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// DiscoverModels fetches the live model list from the Novita /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return core.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// headers returns the auth + content-type headers for Novita requests.
func (p *Provider) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"}
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "novita",
		Headers:    p.headers(),
	}
}

// Complete sends a chat completion request to Novita.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// CompleteStream sends a streaming chat completion request to Novita.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
