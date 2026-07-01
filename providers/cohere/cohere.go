// Package cohere provides a client for the Cohere API.
package cohere

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "cohere"

const defaultBaseURL = "https://api.cohere.com"

// Provider implements the Cohere API client.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
)

// New creates a new Cohere provider.
func New(apiKey, baseURL string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the static list of known Cohere models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"command-r-plus",
		"command-r",
		"command-light",
		"command",
		"embed-v4.0",
		"embed-english-v3.0",
		"embed-multilingual-v3.0",
		"embed-english-light-v3.0",
		"embed-multilingual-light-v3.0",
		"embed-english-v2.0",
		"embed-multilingual-v2.0",
	}
}

// SupportsModel returns true if the model matches a known Cohere prefix.
func (p *Provider) SupportsModel(model string) bool {
	for _, prefix := range []string{"command", "embed-", "rerank-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata for the /v1/models endpoint.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type cohereRequest struct {
	Model            string                 `json:"model"`
	Messages         []cohereRequestMessage `json:"messages"`
	Tools            []core.Tool            `json:"tools,omitempty"`
	ToolChoice       string                 `json:"tool_choice,omitempty"`
	Temperature      *float64               `json:"temperature,omitempty"`
	MaxTokens        *int                   `json:"max_tokens,omitempty"`
	P                *float64               `json:"p,omitempty"`
	Seed             *int64                 `json:"seed,omitempty"`
	PresencePenalty  *float64               `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64               `json:"frequency_penalty,omitempty"`
	StopSequences    []string               `json:"stop_sequences,omitempty"`
	Stream           bool                   `json:"stream,omitempty"`
}

// cohereSupportedParams lists the OpenAI parameters mappable onto the Cohere v2
// chat API (including native tool calling). Anything else the caller sets is
// warn-and-dropped (#140).
var cohereSupportedParams = []string{
	"temperature", "top_p", "max_tokens", "stop",
	"seed", "presence_penalty", "frequency_penalty", "tools", "tool_choice",
}

type cohereRequestMessage struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	ToolCalls  []core.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type cohereToolResultBlock struct {
	Type     string                   `json:"type"`
	Document cohereToolResultDocument `json:"document"`
}

type cohereToolResultDocument struct {
	Data string `json:"data"`
}

type cohereContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cohereMessage struct {
	Role      string               `json:"role"`
	Content   []cohereContentBlock `json:"content"`
	ToolCalls []core.ToolCall      `json:"tool_calls,omitempty"`
}

type cohereTokenCounts struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type cohereUsage struct {
	BilledUnits cohereTokenCounts `json:"billed_units"`
	Tokens      cohereTokenCounts `json:"tokens"`
}

type cohereResponse struct {
	ID           string        `json:"id"`
	Message      cohereMessage `json:"message"`
	Usage        cohereUsage   `json:"usage"`
	FinishReason string        `json:"finish_reason"`
}

type cohereErrorResponse struct {
	Message string `json:"message"`
}

// Cohere v2 tool_choice values.
const (
	cohereToolChoiceRequired = "REQUIRED"
	cohereToolChoiceNone     = "NONE"
)

func cohereToolChoice(choice any) string {
	// Cohere v2 only supports REQUIRED/NONE; a named-function choice has no
	// per-function selection, so it is approximated with REQUIRED.
	switch kind, _ := core.NormalizeToolChoice(choice); kind {
	case core.ToolChoiceRequired, core.ToolChoiceFunction:
		return cohereToolChoiceRequired
	case core.ToolChoiceNone:
		return cohereToolChoiceNone
	default:
		return ""
	}
}

func cohereMessages(messages []core.Message) []cohereRequestMessage {
	out := make([]cohereRequestMessage, 0, len(messages))
	for _, msg := range messages {
		cohMsg := cohereRequestMessage{
			Role:       msg.Role,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
		}
		if msg.Role == core.RoleTool {
			cohMsg.Content = []cohereToolResultBlock{{
				Type: "document",
				Document: cohereToolResultDocument{
					Data: msg.Content,
				},
			}}
		} else if msg.Content != "" {
			// Only set content when non-empty. Content is `any` with omitempty,
			// which does not drop an empty string, so an assistant tool-call
			// turn would otherwise emit content:"" — which Cohere v2 rejects.
			cohMsg.Content = msg.Content
		}
		out = append(out, cohMsg)
	}
	return out
}

// Complete sends a chat completion request to Cohere.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, cohereSupportedParams...)

	cohReq := cohereRequest{
		Model:            req.Model,
		Messages:         cohereMessages(req.Messages),
		Tools:            req.Tools,
		ToolChoice:       cohereToolChoice(req.ToolChoice),
		Temperature:      req.Temperature,
		MaxTokens:        req.MaxTokens,
		P:                req.TopP,
		Seed:             req.Seed,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		StopSequences:    req.Stop,
	}

	bodyReader, _, release, err := core.JSONBodyReader(cohReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/chat", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp cohereErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var cohResp cohereResponse
	if err := json.Unmarshal(respBody, &cohResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var contentParts []string
	for _, block := range cohResp.Message.Content {
		if block.Type == "text" {
			contentParts = append(contentParts, block.Text)
		}
	}

	tokens := cohResp.Usage.Tokens
	return &core.Response{
		ID:    cohResp.ID,
		Model: req.Model,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.Message{
					Role:      cohResp.Message.Role,
					Content:   strings.Join(contentParts, ""),
					ToolCalls: cohResp.Message.ToolCalls,
				},
				FinishReason: core.NormalizeFinishReason(cohResp.FinishReason),
			},
		},
		Usage: core.Usage{
			PromptTokens:     tokens.InputTokens,
			CompletionTokens: tokens.OutputTokens,
			TotalTokens:      tokens.InputTokens + tokens.OutputTokens,
		},
	}, nil
}

type cohereStreamEvent struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Index int             `json:"index,omitempty"`
	Delta json.RawMessage `json:"delta"`
}

type cohereContentDelta struct {
	Message struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

type cohereMessageEndDelta struct {
	FinishReason string      `json:"finish_reason"`
	Usage        cohereUsage `json:"usage"`
}

type cohereToolCallStartDelta struct {
	Message struct {
		ToolCalls json.RawMessage `json:"tool_calls"`
	} `json:"message"`
}

type cohereToolCallDelta struct {
	Message struct {
		ToolCalls json.RawMessage `json:"tool_calls"`
	} `json:"message"`
}

type cohereToolCallDeltaPayload struct {
	Function struct {
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func cohereStreamToolCallStart(raw json.RawMessage, index int) (core.ToolCall, bool) {
	var calls []core.ToolCall
	if err := json.Unmarshal(raw, &calls); err == nil && len(calls) > 0 {
		calls[0].Index = core.Ptr(index)
		return calls[0], true
	}
	var call core.ToolCall
	if err := json.Unmarshal(raw, &call); err == nil && (call.ID != "" || call.Function.Name != "") {
		call.Index = core.Ptr(index)
		return call, true
	}
	return core.ToolCall{}, false
}

func cohereStreamToolCallDelta(raw json.RawMessage, index int) (core.ToolCall, bool) {
	var payload cohereToolCallDeltaPayload
	if err := json.Unmarshal(raw, &payload); err == nil && payload.Function.Arguments != "" {
		return core.ToolCall{
			Index: core.Ptr(index),
			Type:  "function",
			Function: core.FunctionCall{
				Arguments: payload.Function.Arguments,
			},
		}, true
	}
	var payloads []cohereToolCallDeltaPayload
	if err := json.Unmarshal(raw, &payloads); err == nil && len(payloads) > 0 && payloads[0].Function.Arguments != "" {
		return core.ToolCall{
			Index: core.Ptr(index),
			Type:  "function",
			Function: core.FunctionCall{
				Arguments: payloads[0].Function.Arguments,
			},
		}, true
	}
	return core.ToolCall{}, false
}

// CompleteStream sends a streaming chat completion request to Cohere.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, cohereSupportedParams...)

	cohReq := cohereRequest{
		Model:            req.Model,
		Messages:         cohereMessages(req.Messages),
		Tools:            req.Tools,
		ToolChoice:       cohereToolChoice(req.ToolChoice),
		Temperature:      req.Temperature,
		MaxTokens:        req.MaxTokens,
		P:                req.TopP,
		Seed:             req.Seed,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		StopSequences:    req.Stop,
		Stream:           true,
	}

	bodyReader, _, release, err := core.JSONBodyReader(cohReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/chat", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp cohereErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		lines, scanErr := core.SSEDataLines(httpResp.Body)
		for data := range lines {

			var event cohereStreamEvent
			if json.Unmarshal([]byte(data), &event) != nil {
				continue
			}

			switch event.Type {
			case "content-delta":
				var delta cohereContentDelta
				if json.Unmarshal(event.Delta, &delta) != nil {
					continue
				}
				ch <- core.StreamChunk{
					Choices: []core.StreamChoice{
						{
							Index: 0,
							Delta: core.MessageDelta{
								Content: delta.Message.Content.Text,
							},
						},
					},
				}
			case "tool-call-start":
				var delta cohereToolCallStartDelta
				if json.Unmarshal(event.Delta, &delta) != nil {
					continue
				}
				tc, ok := cohereStreamToolCallStart(delta.Message.ToolCalls, event.Index)
				if !ok {
					continue
				}
				ch <- core.StreamChunk{
					ID: event.ID,
					Choices: []core.StreamChoice{
						{
							Index: 0,
							Delta: core.MessageDelta{
								ToolCalls: []core.ToolCall{tc},
							},
						},
					},
				}
			case "tool-call-delta":
				var delta cohereToolCallDelta
				if json.Unmarshal(event.Delta, &delta) != nil {
					continue
				}
				tc, ok := cohereStreamToolCallDelta(delta.Message.ToolCalls, event.Index)
				if !ok {
					continue
				}
				ch <- core.StreamChunk{
					ID: event.ID,
					Choices: []core.StreamChoice{
						{
							Index: 0,
							Delta: core.MessageDelta{
								ToolCalls: []core.ToolCall{tc},
							},
						},
					},
				}
			case "message-end":
				var delta cohereMessageEndDelta
				if json.Unmarshal(event.Delta, &delta) != nil {
					continue
				}
				ch <- core.StreamChunk{
					Choices: []core.StreamChoice{
						{
							Index:        0,
							FinishReason: core.NormalizeFinishReason(delta.FinishReason),
						},
					},
				}
				return
			}
		}
		if err := scanErr(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}

type cohereEmbedRequest struct {
	Texts     []string `json:"texts"`
	Model     string   `json:"model"`
	InputType string   `json:"input_type"`
}

// defaultEmbedInputType is Cohere's document-indexing distribution, used when
// the caller does not specify an input_type.
const defaultEmbedInputType = "search_document"

// cohereTextInputTypes are the input_type values Cohere accepts for text
// embeddings. "image" is excluded — this path embeds texts.
var cohereTextInputTypes = map[string]bool{
	defaultEmbedInputType: true,
	"search_query":        true,
	"classification":      true,
	"clustering":          true,
}

// resolveInputType validates a caller-supplied Cohere input_type, defaulting to
// "search_document" (document-indexing distribution) when unset. Cohere requires
// query embeddings to use "search_query", so honoring the override is what lets
// retrieval work correctly.
func resolveInputType(requested string) (string, error) {
	if requested == "" {
		return defaultEmbedInputType, nil
	}
	if !cohereTextInputTypes[requested] {
		return "", fmt.Errorf("embed: unsupported input_type %q; want one of search_document, search_query, classification, clustering", requested)
	}
	return requested, nil
}

type cohereEmbedResponse struct {
	ID         string      `json:"id"`
	Embeddings [][]float64 `json:"embeddings"`
	Texts      []string    `json:"texts"`
	Meta       struct {
		BilledUnits struct {
			InputTokens int `json:"input_tokens"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// Embed sends an embedding request to Cohere's /v1/embed endpoint.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	var texts []string
	switch v := req.Input.(type) {
	case string:
		texts = []string{v}
	case []string:
		texts = v
	case []any:
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported input type at input[%d]: %T; expected string", i, item)
			}
			texts = append(texts, s)
		}
	default:
		return nil, fmt.Errorf("unsupported input type: %T", req.Input)
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("embedding input must contain at least one text")
	}
	switch req.EncodingFormat {
	case "", "float":
	default:
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; Cohere embeddings return float vectors", req.EncodingFormat)
	}
	if req.Dimensions != nil {
		return nil, fmt.Errorf("embed: dimensions are not supported by Cohere embeddings")
	}
	if req.User != "" {
		return nil, fmt.Errorf("embed: user is not supported by Cohere embeddings")
	}
	inputType, err := resolveInputType(req.InputType)
	if err != nil {
		return nil, err
	}

	cohReq := cohereEmbedRequest{
		Texts:     texts,
		Model:     req.Model,
		InputType: inputType,
	}

	bodyReader, _, release, err := core.JSONBodyReader(cohReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embed", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create embed request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embed response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp cohereErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("cohere embed API error (%d): %s", httpResp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("cohere embed API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var cohResp cohereEmbedResponse
	if err := json.Unmarshal(respBody, &cohResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embed response: %w", err)
	}

	data := make([]core.Embedding, len(cohResp.Embeddings))
	for i, emb := range cohResp.Embeddings {
		data[i] = core.Embedding{
			Object:    "embedding",
			Embedding: emb,
			Index:     i,
		}
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: cohResp.Meta.BilledUnits.InputTokens,
			TotalTokens:  cohResp.Meta.BilledUnits.InputTokens,
		},
	}, nil
}
