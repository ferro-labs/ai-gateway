package providers

import perplexitypkg "github.com/ferro-labs/ai-gateway/providers/perplexity"

// PerplexityProvider is the Perplexity provider implementation.
// The concrete implementation lives in providers/perplexity;
// this alias preserves the package-level type name for backwards compatibility.
type PerplexityProvider = perplexitypkg.Provider

// NewPerplexity creates a new Perplexity provider.
// The signature and return type are unchanged from the previous API.
func NewPerplexity(apiKey, baseURL string) (*PerplexityProvider, error) {
	return perplexitypkg.New(apiKey, baseURL)
}
