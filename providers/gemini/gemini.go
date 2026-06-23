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
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// sanitizeRequestErr strips the request URL (which carries the ?key= API key)
// from *url.Error so the key never reaches logs or client-facing error bodies.
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
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Google Gemini provider.
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
// Gemini authenticates via the ?key= query parameter (added by the proxy
// director), so no Authorization header is required here.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"x-goog-api-key": p.apiKey}
}

// SupportedModels returns the static list of known Gemini models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"gemini-2.0-flash",
		"gemini-2.0-flash-lite",
		"gemini-1.5-pro",
		"gemini-1.5-flash",
		"gemini-embedding-001",
		"text-embedding-004",
		"embedding-001",
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
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
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

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

type geminiErrorDetail struct {
	Message string `json:"message"`
	Status  string `json:"status"`
}

type geminiErrorResponse struct {
	Error geminiErrorDetail `json:"error"`
}

type geminiEmbeddingContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiBatchEmbedContentRequest struct {
	Model                string                 `json:"model"`
	Content              geminiEmbeddingContent `json:"content"`
	OutputDimensionality *int                   `json:"outputDimensionality,omitempty"`
}

type geminiBatchEmbedRequest struct {
	Requests []geminiBatchEmbedContentRequest `json:"requests"`
}

type geminiBatchEmbedResponse struct {
	Embeddings []struct {
		Values []float64 `json:"values"`
	} `json:"embeddings"`
	UsageMetadata struct {
		PromptTokenCount int `json:"promptTokenCount"`
		TotalTokenCount  int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

type geminiStreamResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason,omitempty"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
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
	if msg.Content != "" {
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

// mapFinishReason maps Gemini finish reasons to OpenAI-style reasons.
func mapFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return core.FinishReasonStop
	case "MAX_TOKENS":
		return core.FinishReasonLength
	case "SAFETY":
		return core.FinishReasonContentFilter
	default:
		return reason
	}
}

func geminiFinishReason(reason string, toolCalls []core.ToolCall) string {
	if len(toolCalls) > 0 {
		return core.FinishReasonToolCalls
	}
	return mapFinishReason(reason)
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

func embeddingInputs(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		texts := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
			}
			texts = append(texts, s)
		}
		return texts, nil
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}

func geminiModelResource(model string) string {
	model = strings.TrimPrefix(model, "models/")
	return "models/" + model
}

// Embed sends a text embedding request to Gemini's batchEmbedContents endpoint.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if req.EncodingFormat != "" && req.EncodingFormat != "float" {
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; valid value is \"float\"", req.EncodingFormat)
	}
	texts, err := embeddingInputs(req.Input)
	if err != nil {
		return nil, err
	}

	model := strings.TrimPrefix(req.Model, "models/")
	modelResource := geminiModelResource(req.Model)
	geminiReq := geminiBatchEmbedRequest{
		Requests: make([]geminiBatchEmbedContentRequest, 0, len(texts)),
	}
	for _, text := range texts {
		geminiReq.Requests = append(geminiReq.Requests, geminiBatchEmbedContentRequest{
			Model:                modelResource,
			Content:              geminiEmbeddingContent{Parts: []geminiPart{{Text: text}}},
			OutputDimensionality: req.Dimensions,
		})
	}

	bodyReader, _, release, err := core.JSONBodyReader(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}
	defer release()

	url := fmt.Sprintf("%s/v1beta/models/%s:batchEmbedContents?key=%s", p.baseURL, url.PathEscape(model), p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create embed request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", sanitizeRequestErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embed response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp geminiErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("gemini embed API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("gemini embed API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var geminiResp geminiBatchEmbedResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embed response: %w", err)
	}
	if len(geminiResp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("gemini embed API returned %d embeddings for %d inputs", len(geminiResp.Embeddings), len(texts))
	}

	data := make([]core.Embedding, len(geminiResp.Embeddings))
	for i, embedding := range geminiResp.Embeddings {
		data[i] = core.Embedding{
			Object:    "embedding",
			Embedding: embedding.Values,
			Index:     i,
		}
	}

	totalTokens := geminiResp.UsageMetadata.TotalTokenCount
	if totalTokens == 0 {
		totalTokens = geminiResp.UsageMetadata.PromptTokenCount
	}
	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: geminiResp.UsageMetadata.PromptTokenCount,
			TotalTokens:  totalTokens,
		},
	}, nil
}

// Complete sends a chat completion request to Gemini.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, geminiSupportedParams...)

	geminiReq := buildRequest(req)

	bodyReader, _, release, err := core.JSONBodyReader(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.baseURL, url.PathEscape(req.Model), p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", sanitizeRequestErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp geminiErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("gemini API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("gemini API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var choices []core.Choice
	for i, candidate := range geminiResp.Candidates {
		var text string
		var toolCalls []core.ToolCall
		for _, part := range candidate.Content.Parts {
			text += part.Text
			if part.FunctionCall != nil {
				args := string(part.FunctionCall.Args)
				if args == "" {
					args = "{}"
				}
				id := part.FunctionCall.ID
				if id == "" {
					id = fmt.Sprintf("call_%d_%d", i, len(toolCalls))
				}
				toolCalls = append(toolCalls, core.ToolCall{
					ID:   id,
					Type: "function",
					Function: core.FunctionCall{
						Name:      part.FunctionCall.Name,
						Arguments: args,
					},
				})
			}
		}
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

	return &core.Response{
		ID:      req.Model,
		Model:   req.Model,
		Choices: choices,
		Usage: core.Usage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
		},
	}, nil
}

// CompleteStream sends a streaming chat completion request to Gemini.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, geminiSupportedParams...)

	geminiReq := buildRequest(req)

	bodyReader, _, release, err := core.JSONBodyReader(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?key=%s&alt=sse", p.baseURL, url.PathEscape(req.Model), p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", sanitizeRequestErr(err))
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp geminiErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("gemini API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("gemini API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		scanner := core.NewSSEScanner(httpResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var chunk geminiStreamResponse
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}

			sc := core.StreamChunk{
				ID:    req.Model,
				Model: req.Model,
			}
			for i, candidate := range chunk.Candidates {
				var text string
				var toolCalls []core.ToolCall
				for _, part := range candidate.Content.Parts {
					text += part.Text
					if part.FunctionCall != nil {
						toolCallIndex := len(toolCalls)
						toolCallIndexPtr := toolCallIndex
						args := string(part.FunctionCall.Args)
						if args == "" {
							args = "{}"
						}
						id := part.FunctionCall.ID
						if id == "" {
							id = fmt.Sprintf("call_%d_%d", i, len(toolCalls))
						}
						toolCalls = append(toolCalls, core.ToolCall{
							Index: &toolCallIndexPtr,
							ID:    id,
							Type:  "function",
							Function: core.FunctionCall{
								Name:      part.FunctionCall.Name,
								Arguments: args,
							},
						})
					}
				}
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
			ch <- sc
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
