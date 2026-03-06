package providers

import togetherpkg "github.com/ferro-labs/ai-gateway/providers/together"

// TogetherProvider is the Together AI provider implementation.
// The concrete implementation lives in providers/together;
// this alias preserves the package-level type name for backwards compatibility.
type TogetherProvider = togetherpkg.Provider

// NewTogether creates a new Together AI provider.
// The signature and return type are unchanged from the previous API.
//
// Deprecated: Import providers/together and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewTogether(apiKey, baseURL string) (*TogetherProvider, error) {
	return togetherpkg.New(apiKey, baseURL)
}
