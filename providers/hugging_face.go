package providers

import huggingfacepkg "github.com/ferro-labs/ai-gateway/providers/hugging_face"

// HuggingFaceProvider is the Hugging Face provider implementation.
type HuggingFaceProvider = huggingfacepkg.Provider

// NewHuggingFace creates a new Hugging Face provider.
//
// Deprecated: Import providers/hugging_face and call New directly.
// This compatibility wrapper will be removed in a future major version.
func NewHuggingFace(apiKey, baseURL string) (*HuggingFaceProvider, error) {
	return huggingfacepkg.New(apiKey, baseURL)
}
