package providers

import openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"

// OpenAIProvider is the OpenAI provider implementation.
type OpenAIProvider = openaipkg.Provider

// NewOpenAI creates a new OpenAI provider.
func NewOpenAI(apiKey, baseURL string) (*OpenAIProvider, error) {
	return openaipkg.New(apiKey, baseURL)
}
