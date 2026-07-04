// Package gemini provides a client for the Google Gemini API.
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// sanitizeRequestErr strips the request URL from *url.Error as defense-in-depth
// so no request URL or query params reach logs or client-facing error bodies.
func sanitizeRequestErr(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s %s: %w", urlErr.Op, "[redacted]", urlErr.Err)
	}
	return err
}

// Name is the canonical provider identifier.
const Name = "gemini"

const defaultBaseURL = "https://generativelanguage.googleapis.com"

// Provider implements the Google Gemini API client.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.EmbeddingProvider     = (*Provider)(nil)
	_ core.ImageProvider         = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.NonOpenAIWireProvider = (*Provider)(nil)
)

// New creates a new Google Gemini provider.
func New(apiKey, baseURL string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	} else if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
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

// NonOpenAIWire marks Gemini as ineligible for transparent OpenAI-wire proxy
// pass-through: its upstream is the Gemini generateContent API, not
// OpenAI-shaped. It remains fully usable via its native translated endpoints.
// See core.NonOpenAIWireProvider.
func (*Provider) NonOpenAIWire() {}

// AuthHeaders implements core.ProxiableProvider.
// Gemini authenticates via the x-goog-api-key header, applied to native calls
// in doJSONRequest and injected on the proxy path by the director. The key is
// never placed in the request URL, so it cannot leak into spans or access logs.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"x-goog-api-key": p.apiKey}
}

// SupportedModels returns the static list of known Gemini models.
func (p *Provider) SupportedModels() []string {
	return []string{
		// Current GA tier
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		// Embeddings
		"gemini-embedding-001",
		"text-embedding-004",
		"embedding-001",
		// Image generation (Imagen)
		"imagen-4.0-generate-001",
		"imagen-4.0-ultra-generate-001",
		"imagen-4.0-fast-generate-001",
	}
}

// SupportsModel returns true if the model is a known Gemini chat, embedding, or image model.
func (p *Provider) SupportsModel(model string) bool {
	model = strings.TrimPrefix(model, "models/")
	if strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "imagen-") {
		return true
	}
	switch model {
	case "text-embedding-004", "embedding-001":
		return true
	default:
		return false
	}
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FileData         *geminiFileData         `json:"fileData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

// geminiInlineData carries an inline base64-encoded image, mapped from an OpenAI
// image_url data URI.
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// geminiFileData references image bytes by URI, mapped from a remote (non-data)
// image_url.
type geminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"topP,omitempty"`
	CandidateCount   *int     `json:"candidateCount,omitempty"`
	Seed             *int64   `json:"seed,omitempty"`
	MaxOutputTokens  *int     `json:"maxOutputTokens,omitempty"`
	PresencePenalty  *float64 `json:"presencePenalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequencyPenalty,omitempty"`
	StopSequences    []string `json:"stopSequences,omitempty"`
	ResponseMimeType string   `json:"responseMimeType,omitempty"`
}

// geminiSupportedParams lists the OpenAI parameters mappable onto Gemini's
// generationConfig (plus native tool calling). Anything else the caller sets is
// warn-and-dropped (#140).
var geminiSupportedParams = []string{
	"temperature", "top_p", "n", "seed", "max_tokens",
	"presence_penalty", "frequency_penalty", "stop", "response_format",
	"tools", "tool_choice",
}

type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig       `json:"toolConfig,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig *geminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type geminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// geminiUsageMetadata is Gemini's token accounting, including the cached-content
// and thinking (reasoning) token counts documented on the generateContent
// response.
type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

type geminiResponse struct {
	ResponseID string `json:"responseId"`
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
}

type geminiStreamResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason,omitempty"`
	} `json:"candidates"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
}

// convertToGemini converts gateway Messages to Gemini contents format. System
// messages are collected separately and returned as systemText so the caller can
// route them through Gemini's dedicated systemInstruction field (Gemini 1.5+)
// rather than smuggling them into a user turn. Multiple system messages are
// joined with newlines and preserved regardless of turn order (#144).
func convertToGemini(messages []core.Message) (contents []geminiContent, systemText string) {
	toolCallNames := make(map[string]string)
	for _, msg := range messages {
		if msg.Role == core.RoleSystem {
			if systemText != "" {
				systemText += "\n"
			}
			systemText += msg.Content
			continue
		}

		role := msg.Role
		switch role {
		case "assistant":
			role = "model"
		case core.RoleTool:
			role = core.RoleUser
		}

		parts := geminiParts(msg, toolCallNames)
		// Coalesce consecutive same-role turns into one content. Gemini expects
		// strict user/model alternation, so parallel tool results (each arriving
		// as its own role="tool" → user message) must share a single user turn.
		if n := len(contents); n > 0 && contents[n-1].Role == role {
			contents[n-1].Parts = append(contents[n-1].Parts, parts...)
		} else {
			contents = append(contents, geminiContent{Role: role, Parts: parts})
		}

		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				toolCallNames[tc.ID] = tc.Function.Name
			}
		}
	}

	return contents, systemText
}

func geminiParts(msg core.Message, toolCallNames map[string]string) []geminiPart {
	if msg.Role == core.RoleTool {
		trimmedContent := strings.TrimSpace(msg.Content)
		response := json.RawMessage(trimmedContent)
		if len(response) == 0 || !json.Valid(response) {
			response, _ = json.Marshal(map[string]string{"result": msg.Content})
		} else if !strings.HasPrefix(trimmedContent, "{") {
			response, _ = json.Marshal(map[string]json.RawMessage{"result": response})
		}
		return []geminiPart{{FunctionResponse: &geminiFunctionResponse{
			ID:       msg.ToolCallID,
			Name:     toolCallNames[msg.ToolCallID],
			Response: response,
		}}}
	}
	var parts []geminiPart
	if len(msg.ContentParts) > 0 {
		for _, part := range msg.ContentParts {
			switch part.Type {
			case core.ContentTypeText:
				parts = append(parts, geminiPart{Text: part.Text})
			case "image_url":
				if part.ImageURL != nil {
					parts = append(parts, geminiImagePart(part.ImageURL.URL))
				}
			}
		}
	} else if msg.Content != "" {
		parts = append(parts, geminiPart{Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if len(args) == 0 || !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		}})
	}
	if len(parts) == 0 {
		return []geminiPart{{Text: ""}}
	}
	return parts
}

// geminiImagePart maps an OpenAI image_url to a Gemini part: an inline base64
// image for a data URI, or a fileData URI reference for a remote image. This
// keeps multimodal image content in the request instead of dropping it.
func geminiImagePart(imageURL string) geminiPart {
	if mimeType, data, ok := parseImageDataURI(imageURL); ok {
		return geminiPart{InlineData: &geminiInlineData{MimeType: mimeType, Data: data}}
	}
	return geminiPart{FileData: &geminiFileData{FileURI: imageURL}}
}

// parseImageDataURI splits a "data:<mime>[;param]...;base64,<data>" URI into its
// MIME type and base64 payload. ok is false for a non-base64 data URI or a
// remote URL. The "base64" token may follow other parameters (e.g.
// "data:image/png;charset=utf-8;base64,...").
func parseImageDataURI(uri string) (mimeType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	meta, payload, found := strings.Cut(uri[len(prefix):], ",")
	if !found {
		return "", "", false
	}
	params := strings.Split(meta, ";")
	if slices.Contains(params[1:], "base64") {
		return params[0], payload, true
	}
	return "", "", false
}

// geminiFinishReason maps a Gemini candidate finishReason to the canonical
// OpenAI vocabulary. Gemini has no dedicated tool-call reason, so tool calls are
// inferred from the decoded parts; everything else routes through the shared
// normalizer (which covers Gemini's RECITATION/SAFETY-family reasons).
func geminiFinishReason(reason string, toolCalls []core.ToolCall) string {
	if len(toolCalls) > 0 {
		return core.FinishReasonToolCalls
	}
	return core.NormalizeFinishReason(reason)
}

func buildRequest(req core.Request) geminiRequest {
	contents, systemText := convertToGemini(req.Messages)
	r := geminiRequest{
		Contents: contents,
	}
	if systemText != "" {
		r.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: systemText}},
		}
	}
	cfg := geminiGenerationConfig{
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		CandidateCount:   req.N,
		Seed:             req.Seed,
		MaxOutputTokens:  req.MaxTokens,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		StopSequences:    req.Stop,
	}
	// Map OpenAI response_format JSON modes to Gemini's responseMimeType. The
	// schema itself is not forwarded (Gemini uses a restricted schema dialect),
	// so structured-output enforcement degrades to plain JSON mode.
	if rf := req.ResponseFormat; rf != nil && (rf.Type == "json_object" || rf.Type == "json_schema") {
		cfg.ResponseMimeType = "application/json"
	}
	hasConfig := cfg.Temperature != nil || cfg.TopP != nil || cfg.CandidateCount != nil ||
		cfg.Seed != nil || cfg.MaxOutputTokens != nil || cfg.PresencePenalty != nil ||
		cfg.FrequencyPenalty != nil || len(cfg.StopSequences) > 0 || cfg.ResponseMimeType != ""
	if hasConfig {
		r.GenerationConfig = &cfg
	}
	if tools := geminiTools(req.Tools); len(tools) > 0 {
		r.Tools = tools
		// toolConfig is only meaningful alongside tools; sending it without a
		// functionDeclarations set makes Gemini reject the request.
		if tc := geminiToolConfigFor(req.ToolChoice); tc != nil {
			r.ToolConfig = tc
		}
	}
	return r
}

func geminiTools(tools []core.Tool) []geminiTool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]geminiFunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		params := t.Function.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		decls = append(decls, geminiFunctionDeclaration{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  sanitizeGeminiSchema(params),
		})
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}

// geminiUnsupportedSchemaKeys are JSON-schema keywords Gemini's OpenAPI-3.0
// subset rejects. OpenAI strict-mode tools always emit "additionalProperties",
// so forwarding it verbatim would 400 on gemini-1.5 models.
var geminiUnsupportedSchemaKeys = map[string]bool{
	"$schema":              true,
	"$id":                  true,
	"$ref":                 true,
	"$defs":                true,
	"$comment":             true,
	"definitions":          true,
	"additionalProperties": true,
}

// sanitizeGeminiSchema recursively strips JSON-schema keywords Gemini rejects.
// Input that isn't a JSON object is forwarded unchanged.
func sanitizeGeminiSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	cleaned, err := json.Marshal(stripSchemaKeys(v))
	if err != nil {
		return raw
	}
	return cleaned
}

func stripSchemaKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			if geminiUnsupportedSchemaKeys[k] {
				continue
			}
			m[k] = stripSchemaKeys(val)
		}
		return m
	case []any:
		for i := range t {
			t[i] = stripSchemaKeys(t[i])
		}
		return t
	default:
		return v
	}
}

func geminiToolConfigFor(choice any) *geminiToolConfig {
	mode := func(m string) *geminiToolConfig {
		return &geminiToolConfig{FunctionCallingConfig: &geminiFunctionCallingConfig{Mode: m}}
	}
	switch kind, name := core.NormalizeToolChoice(choice); kind {
	case core.ToolChoiceAuto:
		return mode("AUTO")
	case core.ToolChoiceNone:
		return mode("NONE")
	case core.ToolChoiceRequired:
		return mode("ANY")
	case core.ToolChoiceFunction:
		return &geminiToolConfig{FunctionCallingConfig: &geminiFunctionCallingConfig{
			Mode:                 "ANY",
			AllowedFunctionNames: []string{name},
		}}
	default:
		return nil
	}
}

// doJSONRequest marshals body to JSON and performs an HTTP request against the
// Gemini API. It returns the live response plus a release func the caller must
// defer to return the pooled request buffer. The label is woven into error
// messages so callers can distinguish operations.
func (p *Provider) doJSONRequest(ctx context.Context, method, reqURL, label string, body any) (*http.Response, func(), error) {
	bodyReader, _, release, err := core.JSONBodyReader(body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal %srequest: %w", label, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("failed to create %srequest: %w", label, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range p.AuthHeaders() {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("%srequest failed: %w", label, sanitizeRequestErr(err))
	}
	return httpResp, release, nil
}

// parseCandidateParts accumulates text and tool calls from one candidate's
// parts. When withIndex is set, each tool call carries its position index
// (required for streaming deltas). candidateIndex seeds synthetic tool-call IDs
// when the provider omits them. toolCallCounter, when non-nil, tracks the
// running tool-call count for this candidate across the whole stream: Gemini
// delivers parallel tool calls as separate, cumulative SSE chunks, so a fresh
// per-chunk counter would restart at 0 and misalign indices/IDs across chunks.
// Pass nil for single-shot (non-streaming) parsing, where a fresh count is
// correct.
func parseCandidateParts(parts []geminiPart, candidateIndex int, withIndex bool, toolCallCounter *int) (string, []core.ToolCall) {
	var text string
	var toolCalls []core.ToolCall
	for _, part := range parts {
		text += part.Text
		if part.FunctionCall != nil {
			args := string(part.FunctionCall.Args)
			if args == "" {
				args = "{}"
			}
			n := len(toolCalls)
			if toolCallCounter != nil {
				n = *toolCallCounter
			}
			id := part.FunctionCall.ID
			if id == "" {
				id = fmt.Sprintf("call_%d_%d", candidateIndex, n)
			}
			tc := core.ToolCall{
				ID:   id,
				Type: "function",
				Function: core.FunctionCall{
					Name:      part.FunctionCall.Name,
					Arguments: args,
				},
			}
			if withIndex {
				idx := n
				tc.Index = &idx
			}
			if toolCallCounter != nil {
				*toolCallCounter++
			}
			toolCalls = append(toolCalls, tc)
		}
	}
	return text, toolCalls
}

// Complete sends a chat completion request to Gemini.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, geminiSupportedParams...)

	geminiReq := buildRequest(req)

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", p.baseURL, url.PathEscape(req.Model))
	httpResp, release, err := p.doJSONRequest(ctx, http.MethodPost, url, "", geminiReq)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("gemini", httpResp.StatusCode, respBody)
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var choices []core.Choice
	for i, candidate := range geminiResp.Candidates {
		text, toolCalls := parseCandidateParts(candidate.Content.Parts, i, false, nil)
		choices = append(choices, core.Choice{
			Index: i,
			Message: core.Message{
				Role:      "assistant",
				Content:   text,
				ToolCalls: toolCalls,
			},
			FinishReason: geminiFinishReason(candidate.FinishReason, toolCalls),
		})
	}

	responseID := geminiResp.ResponseID
	if responseID == "" {
		responseID = req.Model
	}
	return &core.Response{
		ID:       responseID,
		Model:    req.Model,
		Provider: p.name,
		Choices:  choices,
		Usage: core.Usage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
			ReasoningTokens:  geminiResp.UsageMetadata.ThoughtsTokenCount,
			CacheReadTokens:  geminiResp.UsageMetadata.CachedContentTokenCount,
		},
	}, nil
}

// CompleteStream sends a streaming chat completion request to Gemini.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, geminiSupportedParams...)

	geminiReq := buildRequest(req)

	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse", p.baseURL, url.PathEscape(req.Model))
	httpResp, release, err := p.doJSONRequest(ctx, http.MethodPost, url, "", geminiReq)
	if err != nil {
		return nil, err
	}
	defer release()

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, core.APIError("gemini", httpResp.StatusCode, respBody)
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		lines, scanErr := core.SSEDataLines(httpResp.Body)
		// toolCallCounters tracks each candidate's running tool-call count
		// across the entire stream, since Gemini can split parallel tool
		// calls across multiple SSE chunks.
		toolCallCounters := make(map[int]int)
		for data := range lines {

			var chunk geminiStreamResponse
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}

			sc := core.StreamChunk{
				ID:    req.Model,
				Model: req.Model,
			}
			for i, candidate := range chunk.Candidates {
				counter := toolCallCounters[i]
				text, toolCalls := parseCandidateParts(candidate.Content.Parts, i, true, &counter)
				toolCallCounters[i] = counter
				sc.Choices = append(sc.Choices, core.StreamChoice{
					Index: i,
					Delta: core.MessageDelta{
						Role:      "assistant",
						Content:   text,
						ToolCalls: toolCalls,
					},
					FinishReason: geminiFinishReason(candidate.FinishReason, toolCalls),
				})
			}
			// Gemini reports usage on the final streamed chunk.
			if chunk.UsageMetadata.TotalTokenCount > 0 {
				sc.Usage = &core.Usage{
					PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
					CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
					TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
					ReasoningTokens:  chunk.UsageMetadata.ThoughtsTokenCount,
					CacheReadTokens:  chunk.UsageMetadata.CachedContentTokenCount,
				}
			}
			ch <- sc
		}
		if err := scanErr(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
