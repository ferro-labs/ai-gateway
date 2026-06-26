// Package anthropic provides a client for the Anthropic API (Claude models).
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/anthropicwire"
	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

func validateBaseURL(name, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s: invalid base URL %q: must be http or https with a host", name, rawURL)
	}
	return nil
}

// Name is the canonical provider identifier.
const Name = "anthropic"

const defaultBaseURL = "https://api.anthropic.com"

// anthropicVersion is the API version sent on every request via the
// anthropic-version header.
const anthropicVersion = "2023-06-01"

// Provider implements the Anthropic API client.
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
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Anthropic provider.
func New(apiKey, baseURL string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := validateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}
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
	return map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": anthropicVersion,
	}
}

// DiscoverModels fetches the live model list from the Anthropic /v1/models
// endpoint, which uses x-api-key + anthropic-version headers rather than Bearer.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	headers := map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": anthropicVersion,
	}
	return discovery.DiscoverModelsWithHeaders(ctx, p.httpClient, p.baseURL+"/v1/models", headers, p.name)
}

// SupportedModels returns the list of models supported by this provider.
func (p *Provider) SupportedModels() []string {
	return []string{
		"claude-sonnet-4-20250514",
		"claude-3-5-sonnet-20241022",
		"claude-3-haiku-20240307",
		"claude-3-opus-20240229",
	}
}

// SupportsModel returns true if the model matches the Anthropic prefix.
func (p *Provider) SupportsModel(model string) bool {
	return strings.HasPrefix(model, "claude-")
}

// Models returns model information for all supported models.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type anthropicMessage struct {
	Role string `json:"role"`
	// Content is either a plain string (text-only turns) or a []anthropicReqBlock
	// (multimodal turns, tool_use on assistant turns, tool_result on user turns).
	Content any `json:"content"`
}

// anthropicReqBlock is a single content block in an outbound message. Only the
// fields relevant to Type are populated; omitempty keeps each block minimal.
type anthropicReqBlock struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=image
	Source *anthropicImageSource `json:"source,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// anthropicImageSource carries an image for an "image" content block. Anthropic
// accepts either inlined base64 data or a remote URL.
type anthropicImageSource struct {
	Type      string `json:"type"`                 // "base64" | "url"
	MediaType string `json:"media_type,omitempty"` // base64 only
	Data      string `json:"data,omitempty"`       // base64 only
	URL       string `json:"url,omitempty"`        // url only
}

type anthropicRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int                  `json:"max_tokens"`
	System        string               `json:"system,omitempty"`
	Messages      []anthropicMessage   `json:"messages"`
	Tools         []anthropicwire.Tool `json:"tools,omitempty"`
	ToolChoice    any                  `json:"tool_choice,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
}

// blockTypeToolUse is the Anthropic content-block type for a tool call.
const blockTypeToolUse = "tool_use"

// anthropicSupportedParams lists the OpenAI parameters the Anthropic Messages
// API can express. Anything else the caller sets is warn-and-dropped (#140).
var anthropicSupportedParams = []string{"temperature", "top_p", "max_tokens", "stop", "tools", "tool_choice"}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicErrorResponse struct {
	Type  string         `json:"type"`
	Error anthropicError `json:"error"`
}

func buildMessages(req core.Request) ([]anthropicMessage, string) {
	var systemParts []string
	var messages []anthropicMessage

	for _, msg := range req.Messages {
		switch msg.Role {
		case core.RoleSystem:
			systemParts = append(systemParts, msg.Content)

		case core.RoleTool:
			// Anthropic requires tool results as tool_result blocks inside a
			// user turn. Merge consecutive results into the preceding user turn
			// so parallel tool calls land in a single message.
			block := anthropicReqBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}
			if n := len(messages); n > 0 && messages[n-1].Role == core.RoleUser {
				if blocks, ok := messages[n-1].Content.([]anthropicReqBlock); ok {
					blocks = append(blocks, block)
					messages[n-1].Content = blocks
					continue
				}
			}
			messages = append(messages, anthropicMessage{
				Role:    core.RoleUser,
				Content: []anthropicReqBlock{block},
			})

		default:
			messages = append(messages, anthropicMessage{
				Role:    msg.Role,
				Content: buildContent(msg),
			})
		}
	}
	return messages, strings.Join(systemParts, "\n")
}

// buildContent renders a non-system message's content for the Anthropic API.
// Plain text turns stay a JSON string (the common path); multimodal turns and
// assistant tool calls become an array of content blocks.
func buildContent(msg core.Message) any {
	var blocks []anthropicReqBlock

	if len(msg.ContentParts) > 0 {
		for _, part := range msg.ContentParts {
			switch part.Type {
			case core.ContentTypeText:
				blocks = append(blocks, anthropicReqBlock{Type: "text", Text: part.Text})
			case "image_url":
				if part.ImageURL != nil {
					blocks = append(blocks, imageBlock(part.ImageURL.URL))
				}
			}
		}
	} else if msg.Content != "" {
		blocks = append(blocks, anthropicReqBlock{Type: "text", Text: msg.Content})
	}

	for _, tc := range msg.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, anthropicReqBlock{
			Type:  blockTypeToolUse,
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	// Plain single-text turn: keep the lightweight string form so the common
	// path is byte-for-byte unchanged.
	if len(msg.ContentParts) == 0 && len(msg.ToolCalls) == 0 {
		return msg.Content
	}
	return blocks
}

func anthropicToolChoice(choice any, tools []core.Tool) any {
	// tool_choice is only valid alongside tools; Anthropic rejects it otherwise.
	if len(tools) == 0 {
		return nil
	}
	switch kind, name := core.NormalizeToolChoice(choice); kind {
	case core.ToolChoiceAuto:
		return map[string]string{"type": "auto"}
	case core.ToolChoiceNone:
		return map[string]string{"type": "none"}
	case core.ToolChoiceRequired:
		return map[string]string{"type": "any"}
	case core.ToolChoiceFunction:
		return map[string]string{"type": "tool", "name": name}
	default:
		return nil
	}
}

// imageBlock maps an OpenAI image_url (data URI or remote URL) to an Anthropic
// image content block.
func imageBlock(url string) anthropicReqBlock {
	if mediaType, data, ok := parseDataURI(url); ok {
		return anthropicReqBlock{
			Type: "image",
			Source: &anthropicImageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      data,
			},
		}
	}
	return anthropicReqBlock{
		Type:   "image",
		Source: &anthropicImageSource{Type: "url", URL: url},
	}
}

// parseDataURI splits a data URI of the form "data:<media-type>;base64,<data>"
// into its media type and payload. ok is false for any non-base64 data URI or
// a plain remote URL.
func parseDataURI(uri string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	meta, payload, found := strings.Cut(uri[len(prefix):], ",")
	if !found {
		return "", "", false
	}
	mediaType, encoding, _ := strings.Cut(meta, ";")
	if encoding != "base64" {
		return "", "", false
	}
	return mediaType, payload, true
}

// Complete sends a chat completion request to Anthropic.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	messages, system := buildMessages(req)

	maxTokens := 1024
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, anthropicSupportedParams...)

	aReq := anthropicRequest{
		Model:         req.Model,
		MaxTokens:     maxTokens,
		Messages:      messages,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
		System:        system,
		Tools:         anthropicwire.MapTools(req.Tools),
		ToolChoice:    anthropicToolChoice(req.ToolChoice, req.Tools),
	}

	bodyReader, _, release, err := core.JSONBodyReader(aReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bodyReader) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		// Error branch keeps ReadAll: the raw body is needed verbatim for the
		// fallback error message when it is not valid Anthropic error JSON.
		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		var errResp anthropicErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	// Success path streams the decode straight off the response body, avoiding
	// the extra full-body copy that io.ReadAll + Unmarshal incurs per request.
	var aResp anthropicResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&aResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var content strings.Builder
	var toolCalls []core.ToolCall
	for _, block := range aResp.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
			continue
		}
		if block.Type == blockTypeToolUse {
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, core.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: core.FunctionCall{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}

	totalTokens := aResp.Usage.InputTokens + aResp.Usage.OutputTokens

	return &core.Response{
		ID:    aResp.ID,
		Model: aResp.Model,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.Message{
					Role:      aResp.Role,
					Content:   content.String(),
					ToolCalls: toolCalls,
				},
				FinishReason: core.NormalizeFinishReason(aResp.StopReason),
			},
		},
		Usage: core.Usage{
			PromptTokens:     aResp.Usage.InputTokens,
			CompletionTokens: aResp.Usage.OutputTokens,
			TotalTokens:      totalTokens,
			CacheReadTokens:  aResp.Usage.CacheReadInputTokens,
			CacheWriteTokens: aResp.Usage.CacheCreationInputTokens,
		},
	}, nil
}

type anthropicStreamMessageStart struct {
	Type    string `json:"type"`
	Message struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Role  string         `json:"role"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
}

type anthropicStreamContentDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

type anthropicStreamContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
}

type anthropicStreamMessageDelta struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
}

// CompleteStream sends a streaming chat completion request to Anthropic.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	messages, system := buildMessages(req)

	maxTokens := 1024
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, anthropicSupportedParams...)

	aReq := anthropicRequest{
		Model:         req.Model,
		MaxTokens:     maxTokens,
		Messages:      messages,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
		System:        system,
		Tools:         anthropicwire.MapTools(req.Tools),
		ToolChoice:    anthropicToolChoice(req.ToolChoice, req.Tools),
		Stream:        true,
	}

	bodyReader, _, release, err := core.JSONBodyReader(aReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bodyReader) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp anthropicErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		var msgID, model string
		var promptTokens, cacheReadTokens, cacheWriteTokens int
		toolCallIndexes := make(map[int]int)
		toolArgsSeen := make(map[int]bool) // tool-call index -> received any input_json_delta
		nextToolCallIndex := 0
		scanner := core.NewSSEScanner(httpResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var raw map[string]any
			if json.Unmarshal([]byte(data), &raw) != nil {
				continue
			}

			eventType, _ := raw["type"].(string)
			switch eventType {
			case "message_start":
				var evt anthropicStreamMessageStart
				if json.Unmarshal([]byte(data), &evt) == nil {
					msgID = evt.Message.ID
					model = evt.Message.Model
					// Anthropic reports prompt + cache tokens once, on
					// message_start; output_tokens arrive later on message_delta.
					promptTokens = evt.Message.Usage.InputTokens
					cacheReadTokens = evt.Message.Usage.CacheReadInputTokens
					cacheWriteTokens = evt.Message.Usage.CacheCreationInputTokens
				}
			case "content_block_start":
				var evt anthropicStreamContentBlockStart
				if json.Unmarshal([]byte(data), &evt) == nil && evt.ContentBlock.Type == blockTypeToolUse {
					toolCallIndex := nextToolCallIndex
					toolCallIndexes[evt.Index] = toolCallIndex
					nextToolCallIndex++
					ch <- core.StreamChunk{
						ID:    msgID,
						Model: model,
						Choices: []core.StreamChoice{
							{
								Index: 0,
								Delta: core.MessageDelta{
									ToolCalls: []core.ToolCall{
										{
											Index: core.Ptr(toolCallIndex),
											ID:    evt.ContentBlock.ID,
											Type:  "function",
											Function: core.FunctionCall{
												Name: evt.ContentBlock.Name,
											},
										},
									},
								},
							},
						},
					}
				}
			case "content_block_delta":
				var evt anthropicStreamContentDelta
				if json.Unmarshal([]byte(data), &evt) == nil {
					if evt.Delta.Type == "input_json_delta" {
						toolCallIndex, ok := toolCallIndexes[evt.Index]
						if !ok {
							toolCallIndex = evt.Index
						}
						toolArgsSeen[toolCallIndex] = true
						ch <- core.StreamChunk{
							ID:    msgID,
							Model: model,
							Choices: []core.StreamChoice{
								{
									Index: 0,
									Delta: core.MessageDelta{
										ToolCalls: []core.ToolCall{
											{
												Index: core.Ptr(toolCallIndex),
												Type:  "function",
												Function: core.FunctionCall{
													Arguments: evt.Delta.PartialJSON,
												},
											},
										},
									},
								},
							},
						}
						continue
					}
					if evt.Delta.Type != "text_delta" {
						continue
					}
					ch <- core.StreamChunk{
						ID:    msgID,
						Model: model,
						Choices: []core.StreamChoice{
							{
								// Single completion: the OpenAI choice index is
								// always 0 (evt.Index is Anthropic's content-block
								// index, not a choice index).
								Index: 0,
								Delta: core.MessageDelta{
									Content: evt.Delta.Text,
								},
							},
						},
					}
				}
			case "message_delta":
				// Emit "{}" arguments for any tool call that produced no
				// input_json_delta (zero-argument tools), so clients that
				// JSON.parse the arguments don't choke on an empty string.
				for _, toolCallIndex := range toolCallIndexes {
					if toolArgsSeen[toolCallIndex] {
						continue
					}
					ch <- core.StreamChunk{
						ID:    msgID,
						Model: model,
						Choices: []core.StreamChoice{{
							Index: 0,
							Delta: core.MessageDelta{
								ToolCalls: []core.ToolCall{{
									Index:    core.Ptr(toolCallIndex),
									Type:     "function",
									Function: core.FunctionCall{Arguments: "{}"},
								}},
							},
						}},
					}
				}
				var evt anthropicStreamMessageDelta
				_ = json.Unmarshal([]byte(data), &evt)
				completionTokens := evt.Usage.OutputTokens
				ch <- core.StreamChunk{
					ID:    msgID,
					Model: model,
					Choices: []core.StreamChoice{
						{
							Index:        0,
							FinishReason: core.NormalizeFinishReason(evt.Delta.StopReason),
						},
					},
					Usage: &core.Usage{
						PromptTokens:     promptTokens,
						CompletionTokens: completionTokens,
						TotalTokens:      promptTokens + completionTokens,
						CacheReadTokens:  cacheReadTokens,
						CacheWriteTokens: cacheWriteTokens,
					},
				}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
