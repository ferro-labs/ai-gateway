package providers

import anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"

// AnthropicProvider is the Anthropic provider implementation.
type AnthropicProvider = anthropicpkg.Provider

// NewAnthropic creates a new Anthropic provider.
func NewAnthropic(apiKey, baseURL string) (*AnthropicProvider, error) {
	return anthropicpkg.New(apiKey, baseURL)
}
