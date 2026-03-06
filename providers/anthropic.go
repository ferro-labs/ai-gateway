package providers

import anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"

// AnthropicProvider is the Anthropic provider implementation.
type AnthropicProvider = anthropicpkg.Provider

// NewAnthropic creates a new Anthropic provider.
//
// Deprecated: Import providers/anthropic and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewAnthropic(apiKey, baseURL string) (*AnthropicProvider, error) {
	return anthropicpkg.New(apiKey, baseURL)
}
