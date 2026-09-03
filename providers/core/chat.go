package core

import (
	"encoding/json"
	"errors"
)

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
	Index    *int         `json:"index,omitempty"` // present on streaming deltas
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the name and arguments of a model-generated function call.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // JSON-encoded argument object
}

// ResponseFormat instructs the model how to format its output.
type ResponseFormat struct {
	Type       string          `json:"type"`                  // "text" | "json_object" | "json_schema"
	JSONSchema json.RawMessage `json:"json_schema,omitempty"` // required when type="json_schema"
}

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
	// ReasoningContent is the model's chain-of-thought, surfaced by reasoning
	// models (e.g. deepseek-reasoner). Empty for models that don't emit it.
	ReasoningContent string `json:"-"`
}

// messageWire is the JSON shape of a Message, shared by MarshalJSON and
// UnmarshalJSON so the two never drift. omitempty only affects encoding, so the
// same tags decode correctly.
type messageWire struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	// Reasoning is the same field under the name Ollama's OpenAI-compatible
	// endpoint uses. Decode-only: the gateway always emits reasoning_content.
	Reasoning string `json:"reasoning,omitempty"`
}

// reasoningText picks whichever of the two spellings the upstream sent.
// reasoning_content wins when both are present, since it is the canonical name.
func (w messageWire) reasoningText() string {
	if w.ReasoningContent != "" {
		return w.ReasoningContent
	}
	return w.Reasoning
}

// MarshalJSON encodes a Message to JSON.  Content is written as a string unless
// ContentParts is set, in which case it is encoded as an array.
func (m Message) MarshalJSON() ([]byte, error) {
	w := messageWire{
		Role:             m.Role,
		Name:             m.Name,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		ReasoningContent: m.ReasoningContent,
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
	var w messageWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	m.Role = w.Role
	m.Name = w.Name
	m.ToolCalls = w.ToolCalls
	m.ToolCallID = w.ToolCallID
	m.ReasoningContent = w.reasoningText()

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
	Tools      []Tool `json:"tools,omitempty"`
	ToolChoice any    `json:"tool_choice,omitempty"`

	// Structured output
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`

	// Log probabilities
	LogProbs    bool `json:"logprobs,omitempty"`
	TopLogProbs *int `json:"top_logprobs,omitempty"`

	// Streaming
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`

	// ClientStreamOptions is the client's stream_options exactly as sent on
	// the incoming request, decoded by internal/handler. It is kept separate
	// from StreamOptions above (which providers/core/openaicompat
	// forwards verbatim upstream for ~20 OpenAI-compatible providers) so that
	// a client's explicit include_usage:false can never leak into that
	// verbatim forward and silently disable a provider's usage reporting —
	// doing so would zero out cost/token accounting for the request without
	// the gateway ever finding out. The only readers are
	// providers/openai.Provider.CompleteStream (to decide whether to strip
	// the client-facing usage chunk one layer up) and callers of
	// internal/streamwrap.Meter (to set MeterMeta.SuppressUsageForClient).
	// Never marshaled (json:"-"): it exists purely for gateway-internal
	// bookkeeping, never for any provider wire body.
	ClientStreamOptions *StreamOptions `json:"-"`

	// Tools
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty"`

	// Misc
	User      string             `json:"user,omitempty"`
	LogitBias map[string]float64 `json:"logit_bias,omitempty"`

	// RoutingMetadata is the caller's routing hints, read by the HTTP layer
	// from the one allow-listed request header (X-Gateway-Metadata, a small
	// JSON object of scalar values) and consulted only by conditional
	// routing's `metadata` predicate. It never travels to a provider.
	RoutingMetadata map[string]string `json:"-"`
}

// StreamOptions carries the OpenAI stream_options object. IncludeUsage requests a
// terminal usage chunk on the stream so cost and metrics tracking work.
//
// No omitempty on IncludeUsage: an explicit false must round-trip distinctly
// from an omitted object wherever this type is marshaled, or a client's
// opt-out becomes indistinguishable from never having asked at all.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// NormalizeCompletionTokenLimits resolves the two completion-length fields to
// the one ceiling EffectiveMaxTokens reports, carried by both, so every
// consumer downstream — guardrails, cache keys, every provider — reads the same
// number.
//
// Reconciling matters because which field reaches the provider is a
// per-provider decision: providers on the OpenAI API surface forward
// max_completion_tokens and clear max_tokens (PreferCompletionTokens), while
// others forward max_tokens. While the two could disagree, the ceiling a
// request actually imposed depended on which provider it landed on, and a
// guardrail reading one field could be handed the other — a request carrying
// max_tokens 5 and max_completion_tokens 500000 passed a cap of 10 and then had
// 500000 forwarded upstream.
//
// A caller who sets only max_tokens keeps it, and max_completion_tokens stays
// absent rather than being invented on the wire.
func (r *Request) NormalizeCompletionTokenLimits() {
	if r.MaxCompletionTokens == nil {
		return
	}
	limit, _ := r.EffectiveMaxTokens()
	// Separate copies rather than one shared pointer, so a plugin that clamps
	// one in place cannot silently move the other.
	maxTokens, maxCompletionTokens := limit, limit
	r.MaxTokens, r.MaxCompletionTokens = &maxTokens, &maxCompletionTokens
}

// EffectiveMaxTokens reports the completion length this request asks for, and
// whether it asks for one at all.
//
// max_completion_tokens supersedes max_tokens when both are present. That is
// the precedence the OpenAI API itself applies — max_tokens is deprecated in
// its favour, and the o-series accepts only the newer field — so a request the
// gateway forwards behaves the way the same request sent directly upstream
// would. Taking the smaller of the two instead would silently hand a caller a
// far shorter completion than the API they are coded against would return.
//
// It is the single definition of "how long may this answer be", used both to
// reconcile the fields and by the max-token guardrail, so a value a guardrail
// approves is exactly the value that travels.
func (r Request) EffectiveMaxTokens() (int, bool) {
	switch {
	case r.MaxCompletionTokens != nil:
		return *r.MaxCompletionTokens, true
	case r.MaxTokens != nil:
		return *r.MaxTokens, true
	default:
		return 0, false
	}
}

// PreferCompletionTokens makes a request carry only the modern
// max_completion_tokens field. It is used by providers on the OpenAI API surface
// (OpenAI, Azure OpenAI, Groq, Cerebras) whose o-series / GPT-5 reasoning models
// reject max_tokens with a 400 — and max_completion_tokens is accepted by every
// chat model on those surfaces, so promoting is safe for the non-reasoning
// models too.
//
// A caller that sets only max_tokens (the field most OpenAI SDKs still default
// to) is the case that matters: without promotion its request 400s the moment it
// is routed to a reasoning model. The legacy value is moved onto
// max_completion_tokens rather than dropped, so the ceiling the caller asked for
// is preserved. When neither field is set, none is invented. This only rewrites
// the provider's outbound body; the global request the guardrails and cache key
// read is reconciled separately by NormalizeCompletionTokenLimits.
func (r *Request) PreferCompletionTokens() {
	if r.MaxCompletionTokens == nil {
		r.MaxCompletionTokens = r.MaxTokens
	}
	r.MaxTokens = nil
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

// Response represents a chat completion response normalised across providers.
type Response struct {
	ID       string   `json:"id"`
	Object   string   `json:"object,omitempty"`
	Created  int64    `json:"created,omitempty"`
	Model    string   `json:"model"`
	Provider string   `json:"provider,omitempty"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`

	// Metadata carries provider-specific top-level response fields (e.g.
	// Perplexity's citations/search_results) captured on request via
	// ChatParams.ExtraResponseFields. Serialized under "provider_metadata" (not
	// "metadata", which OpenAI reserves for client-supplied tags). Nil unless
	// requested.
	Metadata map[string]any `json:"provider_metadata,omitempty"`

	// OverheadMs is the gateway processing overhead in milliseconds
	// (total latency minus provider call duration). Excluded from JSON
	// responses; exposed via the X-Gateway-Overhead-Ms response header.
	OverheadMs float64 `json:"-"`
}

// Choice represents a single completion choice in the response.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage carries token consumption statistics.
//
// PromptTokens is INCLUSIVE of CacheReadTokens on every provider — the OpenAI
// convention, which every provider must normalise to before returning a Usage.
// Providers whose upstream reports the cache-read count exclusively fold it in
// at their decode boundary (see providers/core/anthropicwire). One convention
// is what lets a token count and a cost mean the same thing whichever provider
// served the request: cost accounting prices the prompt on
// PromptTokens - CacheReadTokens and the cached subset at its own rate, so a
// provider that left the two disjoint would have its cached tokens billed twice.
//
// CacheWriteTokens is NOT part of PromptTokens: it bills at its own write rate
// on top of the prompt, and the OpenAI schema has no equivalent field.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// Extended fields — populated by providers that surface them.
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// PromptTokensDetails is the OpenAI breakdown of the prompt token count.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// CompletionTokensDetails is the OpenAI breakdown of the completion token count.
type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// usageAlias strips the JSON methods from Usage so the marshal/unmarshal
// helpers below can embed it without recursing into themselves.
type usageAlias Usage

// UnmarshalJSON decodes the OpenAI usage object, folding the nested
// prompt_tokens_details.cached_tokens and completion_tokens_details.reasoning_tokens,
// and DeepSeek's flat prompt_cache_hit_tokens, into the flat
// CacheReadTokens/ReasoningTokens fields so providers that report usage in
// these alternate forms (OpenRouter, xAI, DeepSeek's streaming path, …)
// surface it consistently. A nonzero flat field takes precedence.
func (u *Usage) UnmarshalJSON(data []byte) error {
	var raw struct {
		usageAlias
		PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details"`
		CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details"`
		PromptCacheHitTokens    int                      `json:"prompt_cache_hit_tokens"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*u = Usage(raw.usageAlias)
	if u.CacheReadTokens == 0 && raw.PromptTokensDetails != nil {
		u.CacheReadTokens = raw.PromptTokensDetails.CachedTokens
	}
	if u.CacheReadTokens == 0 && raw.PromptCacheHitTokens != 0 {
		u.CacheReadTokens = raw.PromptCacheHitTokens
	}
	if u.ReasoningTokens == 0 && raw.CompletionTokensDetails != nil {
		u.ReasoningTokens = raw.CompletionTokensDetails.ReasoningTokens
	}
	return nil
}

// MarshalJSON re-emits the nested prompt_tokens_details and
// completion_tokens_details objects that UnmarshalJSON folded into the flat
// fields, so a client reading usage.completion_tokens_details.reasoning_tokens
// — the name the OpenAI schema defines and its SDKs expose — finds it.
//
// The flat reasoning_tokens / cache_read_tokens / cache_write_tokens keys are
// still written alongside them. They are the only place a cache *write* count
// can go (the OpenAI schema has no field for it), and removing them would break
// every client already reading them.
func (u Usage) MarshalJSON() ([]byte, error) {
	out := struct {
		usageAlias
		PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
		CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	}{usageAlias: usageAlias(u)}
	if u.CacheReadTokens != 0 {
		out.PromptTokensDetails = &PromptTokensDetails{CachedTokens: u.CacheReadTokens}
	}
	if u.ReasoningTokens != 0 {
		out.CompletionTokensDetails = &CompletionTokensDetails{ReasoningTokens: u.ReasoningTokens}
	}
	return json.Marshal(out)
}
