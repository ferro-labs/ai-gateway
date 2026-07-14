// Package moonshot provides a client for the Moonshot AI OpenAI-compatible API.
package moonshot

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
	// Name is the canonical identifier for the Moonshot provider.
	// Re-exported as providers.NameMoonshot in providers/names.go.
	Name           = "moonshot"
	defaultBaseURL = "https://api.moonshot.ai/v1"
)

// Provider implements the core.Provider interface for Moonshot.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Moonshot provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	// Moonshot's default base URL already carries the /v1 prefix, so unlike
	// deepseek we do not trim a trailing /v1 — native paths append directly to it.
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

// SupportedModels returns a static list of known Moonshot models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"kimi-k2.5",
		"kimi-latest",
		"kimi-thinking-preview",
		"kimi-k2-thinking",
		"kimi-k2-turbo-preview",
	}
}

// SupportsModel returns true for any model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Moonshot /v1/models
// endpoint. The base URL already carries the /v1 prefix, so the models path is
// baseURL+"/models" (not baseURL+"/v1/models").
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to Moonshot. Cache-hit tokens
// reported in the standard nested form (usage.prompt_tokens_details.cached_tokens)
// are folded into CacheReadTokens by the shared core.Usage decode.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "moonshot",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}
}

// CompleteStream sends a streaming chat completion request to Moonshot.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
