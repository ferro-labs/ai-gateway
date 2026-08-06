package core

// EmbeddingRequest mirrors the OpenAI /v1/embeddings request schema, plus a
// small set of provider extensions accepted as pass-through fields.
type EmbeddingRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"` // string or []string
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
	// InputType is a non-OpenAI extension for providers that distinguish
	// embedding intent — "search_document", "search_query", "classification",
	// "clustering". Forwarded by the shared embeddings body (Cohere, NVIDIA NIM,
	// OpenRouter, …). Empty lets the provider pick a default.
	InputType string `json:"input_type,omitempty"`
}

// NormalizeEncodingFormat resolves the caller's encoding_format to the one value
// this gateway can carry, so no provider ever sees a format the response type
// cannot hold. It mirrors how EffectiveMaxTokens resolves a request's completion
// ceiling once, at the entry point, rather than per provider.
//
// "base64" is ACCEPTED and served as float. Refusing it broke the reference SDK
// outright: openai-python sets encoding_format="base64" by DEFAULT, as a
// bandwidth optimisation the caller never asked for and mostly cannot see, so
// every default client.embeddings.create(...) answered 400 against every
// provider. That is a worse answer than a correct vector in the other encoding.
//
// Serving float back is not a silent substitution of something the caller cannot
// use. openai-python decodes base64 only when the value it receives IS a string
// (resources/embeddings.py), so a float array passes through its parser
// untouched and reaches the caller as the floats it wanted either way. What is
// genuinely lost is the bandwidth saving, which is why the value is normalised
// here and not forwarded: Embedding.Embedding is []float64 and the handler JSON
// encodes it directly, so a base64 vector could never have survived this
// gateway whatever the upstream returned.
//
// Anything else is left as written for ValidateEmbeddingEncodingFormat to
// refuse, which keeps one rejection point rather than two.
func NormalizeEncodingFormat(format string) string {
	if format == "base64" {
		return "float"
	}
	return format
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
