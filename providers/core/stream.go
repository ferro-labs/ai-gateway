package core

// StreamChunk represents a single SSE chunk in a streaming response.
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	// Usage is populated in the final chunk by providers that support streaming
	// usage reporting (e.g. OpenAI with stream_options.include_usage=true);
	// non-final chunks leave this nil so it is omitted from SSE payloads.
	Usage *Usage `json:"usage,omitempty"`
	Error error  `json:"-"` // non-nil signals a stream failure
}

// StreamChoice is a single choice in a streaming chunk.
type StreamChoice struct {
	Index        int          `json:"index"`
	Delta        MessageDelta `json:"delta"`
	FinishReason string       `json:"finish_reason,omitempty"`
}

// MessageDelta carries incremental content in a streaming response.
type MessageDelta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}
