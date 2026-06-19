package core

// EmbeddingRequest mirrors the OpenAI /v1/embeddings request schema, plus a
// small set of provider extensions accepted as pass-through fields.
type EmbeddingRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"` // string or []string
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
	// InputType is a non-OpenAI extension used by providers (e.g. Cohere) that
	// distinguish embedding intent — "search_document", "search_query",
	// "classification", "clustering". Empty lets the provider pick a default.
	InputType string `json:"input_type,omitempty"`
}

// EmbeddingResponse mirrors the OpenAI /v1/embeddings response schema.
type EmbeddingResponse struct {
	Object string         `json:"object"`
	Data   []Embedding    `json:"data"`
	Model  string         `json:"model"`
	Usage  EmbeddingUsage `json:"usage"`
}

// Embedding holds a single embedding vector and its index.
type Embedding struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// EmbeddingUsage carries token consumption for an embedding request.
type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
