package providers

import coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"

// CohereProvider is the Cohere provider implementation.
type CohereProvider = coherepkg.Provider

// NewCohere creates a new Cohere provider.
//
// Deprecated: Import providers/cohere and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewCohere(apiKey, baseURL string) (*CohereProvider, error) {
	return coherepkg.New(apiKey, baseURL)
}
