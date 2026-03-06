package providers

import huggingfacepkg "github.com/ferro-labs/ai-gateway/providers/hugging_face"

// HuggingFaceProvider is the Hugging Face provider implementation.
type HuggingFaceProvider = huggingfacepkg.Provider

// NewHuggingFace creates a new Hugging Face provider.
func NewHuggingFace(apiKey, baseURL string) (*HuggingFaceProvider, error) {
	return huggingfacepkg.New(apiKey, baseURL)
}
