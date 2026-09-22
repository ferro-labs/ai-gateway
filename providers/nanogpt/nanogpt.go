// Package nanogpt provides a client for the NanoGPT API.
package nanogpt

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
	// Name is the canonical identifier for the NanoGPT provider.
	// Re-exported as providers.NameNanoGPT in providers/names.go.
	Name           = "nanogpt"
	defaultBaseURL = "https://nano-gpt.com/api/v1"
)

// Provider implements the core.Provider interface for NanoGPT.
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
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new NanoGPT provider.
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

// SupportedModels returns a static list of known NanoGPT models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"anthropic/claude-opus-4.7",
		"anthropic/claude-opus-latest",
		"anthropic/claude-sonnet-latest",
		"anthropic/claude-haiku-latest",
		"anthropic/claude-opus-4.6",
		"anthropic/claude-sonnet-4.6",
		"openai/gpt-5.5",
		"openai/gpt-chat-latest",
		"openai/gpt-latest",
		"x-ai/grok-4.20",
		"x-ai/grok-4.3",
		"x-ai/grok-latest",
		"deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-latest",
		"moonshotai/kimi-k2.6",
		"moonshotai/kimi-latest",
		"google/gemini-flash-latest",
		"google/gemini-pro-latest",
		"google/gemini-3.5-flash",
		"google/gemini-3-flash-preview",
		"mistral/mistral-medium-3.5",
	}
}

// SupportsModel returns true for any NanoGPT model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the NanoGPT /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discov.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to NanoGPT.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "nanogpt",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to NanoGPT.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "nanogpt",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}
