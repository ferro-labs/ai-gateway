package providers

import deepseekpkg "github.com/ferro-labs/ai-gateway/providers/deepseek"

// DeepSeekProvider is the DeepSeek provider implementation.
// The concrete implementation lives in providers/deepseek;
// this alias preserves the package-level type name for backwards compatibility.
type DeepSeekProvider = deepseekpkg.Provider

// NewDeepSeek creates a new DeepSeek provider.
// The signature and return type are unchanged from the previous API.
func NewDeepSeek(apiKey, baseURL string) (*DeepSeekProvider, error) {
	return deepseekpkg.New(apiKey, baseURL)
}
