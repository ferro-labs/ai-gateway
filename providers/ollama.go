package providers

import ollamapkg "github.com/ferro-labs/ai-gateway/providers/ollama"

// OllamaProvider is the Ollama local LLM server provider.
type OllamaProvider = ollamapkg.Provider

// NewOllama creates a new Ollama provider.
//
// Deprecated: Import providers/ollama and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewOllama(baseURL string, models []string) (*OllamaProvider, error) {
	return ollamapkg.New(baseURL, models)
}
