// Package nanogpt provides a client for the NanoGPT API.
package nanogpt

import (
	"context"

	"github.com/ferro-labs/ai-gateway/internal/compat"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
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
	*compat.Client
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
	return &Provider{
		Client: compat.New(Name, apiKey, baseURL, defaultBaseURL, providerhttp.ForProvider(Name)),
	}, nil
}

// SupportedModels returns a static list of known NanoGPT models.
func (p *Provider) SupportedModels() []string {
	return []string{
		// Anthropic
		"anthropic/claude-opus-4.7",
		"anthropic/claude-opus-latest",
		"anthropic/claude-sonnet-latest",
		"anthropic/claude-haiku-latest",
		"anthropic/claude-opus-4.6",
		"anthropic/claude-sonnet-4.6",
		// OpenAI
		"openai/gpt-5.5",
		"openai/gpt-chat-latest",
		"openai/gpt-latest",
		// xAI
		"x-ai/grok-4.20",
		"x-ai/grok-4.3",
		"x-ai/grok-latest",
		// DeepSeek
		"deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-latest",
		// Moonshot
		"moonshotai/kimi-k2.6",
		"moonshotai/kimi-latest",
		// Google
		"google/gemini-flash-latest",
		"google/gemini-pro-latest",
		"google/gemini-3.5-flash",
		"google/gemini-3-flash-preview",
		// Mistral
		"mistral/mistral-medium-3.5",
	}
}

// SupportsModel returns true for any NanoGPT model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.Name(), p.SupportedModels())
}

// DiscoverModels fetches the live model list from the NanoGPT /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return p.Client.DiscoverModels(ctx)
}
