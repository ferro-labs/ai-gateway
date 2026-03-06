package providers

import vertexaipkg "github.com/ferro-labs/ai-gateway/providers/vertex_ai"

// VertexAIOptions configures Vertex AI provider initialization.
type VertexAIOptions = vertexaipkg.Options

// VertexAIProvider is the Google Vertex AI provider.
type VertexAIProvider = vertexaipkg.Provider

// NewVertexAI creates a new Vertex AI provider.
func NewVertexAI(opts VertexAIOptions) (*VertexAIProvider, error) {
return vertexaipkg.New(opts)
}
