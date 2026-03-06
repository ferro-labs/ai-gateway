package providers

import fireworkspkg "github.com/ferro-labs/ai-gateway/providers/fireworks"

// FireworksProvider is the Fireworks AI provider implementation.
// The concrete implementation lives in providers/fireworks;
// this alias preserves the package-level type name for backwards compatibility.
type FireworksProvider = fireworkspkg.Provider

// NewFireworks creates a new Fireworks AI provider.
// The signature and return type are unchanged from the previous API.
func NewFireworks(apiKey, baseURL string) (*FireworksProvider, error) {
	return fireworkspkg.New(apiKey, baseURL)
}
