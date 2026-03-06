package providers

import geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"

// GeminiProvider is the Google Gemini provider.
type GeminiProvider = geminipkg.Provider

// NewGemini creates a new Google Gemini provider.
func NewGemini(apiKey, baseURL string) (*GeminiProvider, error) {
return geminipkg.New(apiKey, baseURL)
}
