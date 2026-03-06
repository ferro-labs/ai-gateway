package providers

import xaipkg "github.com/ferro-labs/ai-gateway/providers/xai"

// XAIProvider is the xAI provider implementation.
// The concrete implementation lives in providers/xai;
// this alias preserves the package-level type name for backwards compatibility.
type XAIProvider = xaipkg.Provider

// NewXAI creates a new xAI provider.
// The signature and return type are unchanged from the previous API.
//
// Deprecated: Import providers/xai and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewXAI(apiKey, baseURL string) (*XAIProvider, error) {
	return xaipkg.New(apiKey, baseURL)
}
