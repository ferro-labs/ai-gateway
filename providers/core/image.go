package core

// ImageRequest mirrors the OpenAI /v1/images/generations request schema.
type ImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"` // "url" | "b64_json"
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
	User           string `json:"user,omitempty"`
}

// ImageResponse mirrors the OpenAI /v1/images/generations response schema.
type ImageResponse struct {
	Created int64            `json:"created"`
	Data    []GeneratedImage `json:"data"`
}

// GeneratedImage holds the result of a single image generation.
type GeneratedImage struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}
