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

// defaultMaxTokens is the max_tokens value used when the caller does not set one.
const defaultMaxTokens = 1024

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

type anthropicRequest struct {
	Model         string                  `json:"model"`
	MaxTokens     int                     `json:"max_tokens"`
	System        string                  `json:"system,omitempty"`
	Messages      []anthropicwire.Message `json:"messages"`
	Tools         []anthropicwire.Tool    `json:"tools,omitempty"`
	ToolChoice    any                     `json:"tool_choice,omitempty"`
	Temperature   *float64                `json:"temperature,omitempty"`
	TopP          *float64                `json:"top_p,omitempty"`
	StopSequences []string                `json:"stop_sequences,omitempty"`
	Stream        bool                    `json:"stream,omitempty"`
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

// buildContent renders a non-system message's content for the Anthropic API.
// Plain text turns stay a JSON string (the common path); multimodal turns and
// assistant tool calls become an array of content blocks. It is passed to
// anthropicwire.BuildMessages as the per-message content callback, so it MUST
// return []anthropicwire.Block (not another slice type) when it emits blocks.
func buildContent(msg core.Message) any {
	var blocks []anthropicwire.Block

	if len(msg.ContentParts) > 0 {
		for _, part := range msg.ContentParts {
			switch part.Type {
			case core.ContentTypeText:
				blocks = append(blocks, anthropicwire.Block{Type: "text", Text: part.Text})
			case "image_url":
				if part.ImageURL != nil {
					blocks = append(blocks, imageBlock(part.ImageURL.URL))
				}
			}
		}
	} else if msg.Content != "" {
		blocks = append(blocks, anthropicwire.Block{Type: "text", Text: msg.Content})
	}

	for _, tc := range msg.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, anthropicwire.Block{
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

// imageBlock maps an OpenAI image_url (data URI or remote URL) to an Anthropic
// image content block.
func imageBlock(url string) anthropicwire.Block {
	if mediaType, data, ok := parseDataURI(url); ok {
		return anthropicwire.Block{
			Type: "image",
			Source: &anthropicwire.ImageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      data,
			},
		}
	}
	return anthropicwire.Block{
		Type:   "image",
		Source: &anthropicwire.ImageSource{Type: "url", URL: url},
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

// buildAnthropicRequest maps a core.Request to an Anthropic Messages API request
// body. stream toggles server-sent event streaming.
func buildAnthropicRequest(req core.Request, stream bool) anthropicRequest {
	messages, system := anthropicwire.BuildMessages(req, buildContent)

	maxTokens := defaultMaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	return anthropicRequest{
		Model:         req.Model,
		MaxTokens:     maxTokens,
		Messages:      messages,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
		System:        system,
		Tools:         anthropicwire.MapTools(req.Tools),
		ToolChoice:    anthropicwire.MapToolChoice(req.ToolChoice, req.Tools),
		Stream:        stream,
	}
}

// newMessagesRequest sends a POST to the Anthropic /v1/messages endpoint with the
// standard authentication and version headers. The returned release frees the
// pooled request body and must be called by the caller.
func (p *Provider) newMessagesRequest(ctx context.Context, aReq anthropicRequest) (*http.Response, func(), error) {
	bodyReader, _, release, err := core.JSONBodyReader(aReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bodyReader) //nolint:gosec // baseURL validated in New()
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq) //nolint:gosec // baseURL validated in New()
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	return httpResp, release, nil
}

// Complete sends a chat completion request to Anthropic.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, anthropicSupportedParams...)

	aReq := buildAnthropicRequest(req, false)

	httpResp, release, err := p.newMessagesRequest(ctx, aReq)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		// Error branch keeps ReadAll: the raw body is needed verbatim for the
		// fallback error message when it is not valid Anthropic error JSON.
		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		return nil, core.APIError("anthropic", httpResp.StatusCode, respBody)
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
