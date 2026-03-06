package providers

import openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"

// OpenAIProvider is the OpenAI provider implementation.
type OpenAIProvider = openaipkg.Provider

// NewOpenAI creates a new OpenAI provider.
//
// Deprecated: Import providers/openai and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewOpenAI(apiKey, baseURL string) (*OpenAIProvider, error) {
	return openaipkg.New(apiKey, baseURL)
}
