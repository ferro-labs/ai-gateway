package providers

import ai21pkg "github.com/ferro-labs/ai-gateway/providers/ai21"

// AI21Provider is the AI21 Labs provider implementation.
type AI21Provider = ai21pkg.Provider

// NewAI21 creates a new AI21 Labs provider.
func NewAI21(apiKey, baseURL string) (*AI21Provider, error) {
	return ai21pkg.New(apiKey, baseURL)
}

// isJambaModel is re-exported for test access.
var isJambaModel = ai21pkg.IsJambaModel
