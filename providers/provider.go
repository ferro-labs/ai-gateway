// Package providers defines the Provider interface and shared data types
// used across all LLM provider implementations.
//
// The Provider interface must be implemented by any backend that integrates
// with the gateway. StreamProvider extends Provider for streaming responses.
//
// Core types: Request, Response, Message, StreamChunk, ModelInfo.
package providers

import (
	"context"
	"encoding/json"
	"errors"
)

// Message role constants used across multiple providers.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"

	// ContentTypeText is the content-part type for plain text (multimodal messages).
	ContentTypeText = "text"

	// SSEDone is the sentinel value that marks the end of a server-sent event stream.
	SSEDone = "[DONE]"
)

// Provider defines the interface that all LLM providers must implement.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
	SupportedModels() []string
	SupportsModel(model string) bool
	Models() []ModelInfo
}

// StreamProvider is an optional interface for providers that support streaming.
type StreamProvider interface {
	Provider
	CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error)
}

// ProxiableProvider is an optional interface for providers that support
// raw HTTP proxy pass-through. The gateway uses this to forward requests
// for endpoints it does not handle natively (e.g. /v1/files, /v1/batches).
type ProxiableProvider interface {
	Provider
	// BaseURL returns the provider's root API URL (no trailing slash).
	BaseURL() string
	// AuthHeaders returns the HTTP headers required to authenticate with the
	// provider (e.g. {"Authorization": "Bearer sk-..."}).
	AuthHeaders() map[string]string
}

// EmbeddingProvider is an optional interface for providers that support
// the /v1/embeddings endpoint.
type EmbeddingProvider interface {
	Provider
	Embed(ctx context.Context, req EmbeddingRequest) (*EmbeddingResponse, error)
}

// ImageProvider is an optional interface for providers that support
// the /v1/images/generations endpoint.
type ImageProvider interface {
	Provider
	GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error)
}

// DiscoveryProvider is an optional interface for providers that can
// enumerate their available models live from the provider API.
type DiscoveryProvider interface {
	Provider
	DiscoverModels(ctx context.Context) ([]ModelInfo, error)
}

// --------------------------------------------------------------- Embeddings --

// EmbeddingRequest mirrors the OpenAI /v1/embeddings request schema.
type EmbeddingRequest struct {
	Model          string      `json:"model"`
	Input          interface{} `json:"input"` // string or []string
	EncodingFormat string      `json:"encoding_format,omitempty"`
	Dimensions     *int        `json:"dimensions,omitempty"`
	User           string      `json:"user,omitempty"`
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

// ---------------------------------------------------------- Image Generation --

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

// ------------------------------------------------------------------ types ---

// ContentPart is a single element of a multipart message content array.
// Used for vision/multimodal requests where content contains text and images.
type ContentPart struct {
	Type     string        `json:"type"`                // "text" or "image_url"
	Text     string        `json:"text,omitempty"`      // for type="text"
	ImageURL *ImageURLPart `json:"image_url,omitempty"` // for type="image_url"
}

// ImageURLPart carries the URL (or base64 data URI) for an image content part.
type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto" | "low" | "high"
}

// Tool describes a function the model may call.
type Tool struct {
	Type     string   `json:"type"` // always "function"
	Function Function `json:"function"`
}

// Function describes the callable function within a Tool.
type Function struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is the JSON Schema for the function arguments.
	Parameters json.RawMessage `json:"parameters,omitempty"`
	Strict     bool            `json:"strict,omitempty"`
}

// ToolCall is a function invocation returned by the model in its response.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the name and arguments of a model-generated function call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded argument object
}

// ResponseFormat instructs the model how to format its output.
type ResponseFormat struct {
	Type       string          `json:"type"`                  // "text" | "json_object" | "json_schema"
	JSONSchema json.RawMessage `json:"json_schema,omitempty"` // required when type="json_schema"
}

// ----------------------------------------------------------------- Message ---

// Message represents a single turn in a conversation.
//
// The Content field holds plain-text content and is always valid for use with
// any provider. ContentParts is populated automatically when the incoming JSON
// encodes content as an array (vision / multimodal requests); providers that
// support images should check ContentParts first.
type Message struct {
	Role         string        `json:"-"` // marshalled by custom JSON methods
	Content      string        `json:"-"` // plain-text content (always set)
	ContentParts []ContentPart `json:"-"` // non-nil when content is multipart
	Name         string        `json:"-"`
	ToolCalls    []ToolCall    `json:"-"` // tool calls issued by the model
	ToolCallID   string        `json:"-"` // for role="tool" result messages
}

// MarshalJSON encodes a Message to JSON.  Content is written as a string unless
// ContentParts is set, in which case it is encoded as an array.
func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content,omitempty"`
		Name       string          `json:"name,omitempty"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	w := wire{
		Role:       m.Role,
		Name:       m.Name,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ContentParts) > 0 {
		b, err := json.Marshal(m.ContentParts)
		if err != nil {
			return nil, err
		}
		w.Content = b
	} else {
		b, err := json.Marshal(m.Content)
		if err != nil {
			return nil, err
		}
		w.Content = b
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes a Message from JSON.  The content field may be a plain
// string or an array of ContentPart objects; both forms are handled.
func (m *Message) UnmarshalJSON(b []byte) error {
	type wire struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		Name       string          `json:"name"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	m.Role = w.Role
	m.Name = w.Name
	m.ToolCalls = w.ToolCalls
	m.ToolCallID = w.ToolCallID

	if len(w.Content) == 0 || string(w.Content) == "null" {
		return nil
	}
	// Try plain string first (common case).
	var s string
	if err := json.Unmarshal(w.Content, &s); err == nil {
		m.Content = s
		return nil
	}
	// Fall back to content-part array (vision / multimodal).
	var parts []ContentPart
	if err := json.Unmarshal(w.Content, &parts); err != nil {
		return err
	}
	m.ContentParts = parts
	// Collapse text parts into Content so existing code paths keep working.
	for _, p := range parts {
		if p.Type == ContentTypeText {
			m.Content += p.Text
		}
	}
	return nil
}

// ----------------------------------------------------------------- Request ---

// Request represents a chat completion request sent to the gateway.
// Fields map 1-to-1 with the OpenAI Chat Completions API so that any
// OpenAI-compatible client works without modification.
type Request struct {
	// Required
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`

	// Sampling
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	N           *int     `json:"n,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`

	// Output limits
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`

	// Penalties
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`

	// Stop sequences
	Stop []string `json:"stop,omitempty"`

	// Tools / function calling
	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice interface{} `json:"tool_choice,omitempty"`

	// Structured output
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`

	// Log probabilities
	LogProbs    bool `json:"logprobs,omitempty"`
	TopLogProbs *int `json:"top_logprobs,omitempty"`

	// Streaming
	Stream bool `json:"stream,omitempty"`

	// Misc
	User      string             `json:"user,omitempty"`
	LogitBias map[string]float64 `json:"logit_bias,omitempty"`
}

// Validate returns an error if the request is missing required fields or
// contains out-of-range parameter values.
func (r Request) Validate() error {
	if r.Model == "" {
		return errors.New("model is required")
	}
	if len(r.Messages) == 0 {
		return errors.New("at least one message is required")
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	if r.TopP != nil && (*r.TopP < 0 || *r.TopP > 1) {
		return errors.New("top_p must be between 0 and 1")
	}
	if r.MaxTokens != nil && *r.MaxTokens <= 0 {
		return errors.New("max_tokens must be positive")
	}
	if r.MaxCompletionTokens != nil && *r.MaxCompletionTokens <= 0 {
		return errors.New("max_completion_tokens must be positive")
	}
	if r.PresencePenalty != nil && (*r.PresencePenalty < -2 || *r.PresencePenalty > 2) {
		return errors.New("presence_penalty must be between -2 and 2")
	}
	if r.FrequencyPenalty != nil && (*r.FrequencyPenalty < -2 || *r.FrequencyPenalty > 2) {
		return errors.New("frequency_penalty must be between -2 and 2")
	}
	return nil
}

// ----------------------------------------------------------------- Response --

// Response represents a chat completion response normalised across providers.
type Response struct {
	ID       string   `json:"id"`
	Object   string   `json:"object,omitempty"`
	Created  int64    `json:"created,omitempty"`
	Model    string   `json:"model"`
	Provider string   `json:"provider,omitempty"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
}

// Choice represents a single completion choice in the response.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// StreamChunk represents a single SSE chunk in a streaming response.
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Error   error          `json:"-"` // non-nil signals a stream failure
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

// Usage carries token consumption statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// Extended fields — populated by providers that surface them.
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// ModelInfo describes a single model offered by a provider.
// Fields match the OpenAI /v1/models response schema.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}
