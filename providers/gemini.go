package providers

import geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"

// GeminiProvider is the Google Gemini provider.
type GeminiProvider = geminipkg.Provider

// NewGemini creates a new Google Gemini provider.
//
// Deprecated: Import providers/gemini and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewGemini(apiKey, baseURL string) (*GeminiProvider, error) {
return geminipkg.New(apiKey, baseURL)
}
