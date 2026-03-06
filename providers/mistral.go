package providers

import mistralpkg "github.com/ferro-labs/ai-gateway/providers/mistral"

// MistralProvider is the Mistral provider implementation.
// The concrete implementation lives in providers/mistral;
// this alias preserves the package-level type name for backwards compatibility.
type MistralProvider = mistralpkg.Provider

// NewMistral creates a new Mistral provider.
// The signature and return type are unchanged from the previous API.
//
// Deprecated: Import providers/mistral and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewMistral(apiKey, baseURL string) (*MistralProvider, error) {
	return mistralpkg.New(apiKey, baseURL)
}
