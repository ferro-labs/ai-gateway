package providers

import ollamapkg "github.com/ferro-labs/ai-gateway/providers/ollama"

// OllamaProvider is the Ollama local LLM server provider.
type OllamaProvider = ollamapkg.Provider

// NewOllama creates a new Ollama provider.
func NewOllama(baseURL string, models []string) (*OllamaProvider, error) {
return ollamapkg.New(baseURL, models)
}
