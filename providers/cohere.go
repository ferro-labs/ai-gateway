package providers

import coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"

// CohereProvider is the Cohere provider implementation.
type CohereProvider = coherepkg.Provider

// NewCohere creates a new Cohere provider.
func NewCohere(apiKey, baseURL string) (*CohereProvider, error) {
	return coherepkg.New(apiKey, baseURL)
}
