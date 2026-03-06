package providers

import groqpkg "github.com/ferro-labs/ai-gateway/providers/groq"

// GroqProvider is the Groq provider implementation.
// The concrete implementation lives in providers/groq;
// this alias preserves the package-level type name for backwards compatibility.
type GroqProvider = groqpkg.Provider

// NewGroq creates a new Groq provider.
// The signature and return type are unchanged from the previous API.
//
// Deprecated: Import providers/groq and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewGroq(apiKey, baseURL string) (*GroqProvider, error) {
	return groqpkg.New(apiKey, baseURL)
}
