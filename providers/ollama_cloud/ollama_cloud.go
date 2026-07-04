// Package ollamacloud provides a client for the Ollama Cloud API.
package ollamacloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical provider identifier.
	Name           = "ollama-cloud"
	defaultBaseURL = "https://ollama.com"
)

var defaultModels = []string{
	"gpt-oss:120b",
	"gpt-oss:20b",
	"qwen3-coder:480b",
	"deepseek-v3.1:671b",
}

// Provider implements the Ollama Cloud API client.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client

	mu         sync.RWMutex
	models     []string
	discovered []string
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Ollama Cloud provider.
func New(apiKey, baseURL string, models []string) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("ollama-cloud: api key is required")
	}

	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}

	models = normalizeModels(models)
	if len(models) == 0 {
		models = append([]string(nil), defaultModels...)
	}

	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
		models:     models,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// SupportedModels returns the configured and discovered models.
func (p *Provider) SupportedModels() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return combineModels(p.models, p.discovered)
}

// SupportsModel returns true if model is configured or has been discovered.
func (p *Provider) SupportsModel(model string) bool {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, Name+"/")
	if model == "" {
		return false
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, supported := range combineModels(p.models, p.discovered) {
		if model == supported {
			return true
		}
	}
	return false
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// Complete sends a non-streaming chat request to Ollama Cloud. Unlike the local
// ollama provider (which uses the OpenAI-compatible /v1 surface), this stays on
// the native /api/chat endpoint: ollama.com's /v1 surface is not first-party
// documented, so a migration is deferred pending live verification.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	apiReq := buildChatRequest(ctx, req, false)
	bodyReader, _, release, err := core.JSONBodyReader(apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

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
		return nil, apiError(httpResp.StatusCode, respBody)
	}

	var apiResp chatResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if apiResp.Error != "" {
		return nil, fmt.Errorf("ollama-cloud API error: %s", apiResp.Error)
	}

	message := apiResp.Message.toCore()
	finishReason := core.NormalizeFinishReason(apiResp.DoneReason)
	if len(message.ToolCalls) > 0 {
		finishReason = core.FinishReasonToolCalls
	}

	return &core.Response{
		Object:   "chat.completion",
		Created:  parseCreatedAt(apiResp.CreatedAt),
		Model:    apiResp.Model,
		Provider: p.name,
		Choices: []core.Choice{
			{
				Index:        0,
				Message:      message,
				FinishReason: finishReason,
			},
		},
		Usage: usageFromCounts(apiResp.PromptEvalCount, apiResp.EvalCount),
	}, nil
}

// CompleteStream sends a streaming chat request to Ollama Cloud.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	apiReq := buildChatRequest(ctx, req, true)
	bodyReader, _, release, err := core.JSONBodyReader(apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, apiError(httpResp.StatusCode, respBody)
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		scanner := core.NewSSEScanner(httpResp.Body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var apiResp chatResponse
			if err := json.Unmarshal([]byte(line), &apiResp); err != nil {
				ch <- core.StreamChunk{Error: fmt.Errorf("failed to unmarshal stream chunk: %w", err)}
				return
			}
			if apiResp.Error != "" {
				ch <- core.StreamChunk{Error: fmt.Errorf("ollama-cloud API error: %s", apiResp.Error)}
				return
			}

			ch <- streamChunkFromResponse(apiResp)
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}

// DiscoverModels fetches the live Ollama Cloud model list.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

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
		return nil, apiError(httpResp.StatusCode, respBody)
	}

	var tags tagsResponse
	if err := json.Unmarshal(respBody, &tags); err != nil {
		return nil, fmt.Errorf("failed to unmarshal models response: %w", err)
	}

	seen := make(map[string]struct{}, len(tags.Models))
	modelIDs := make([]string, 0, len(tags.Models))
	models := make([]core.ModelInfo, 0, len(tags.Models))
	for _, m := range tags.Models {
		id := strings.TrimSpace(m.Name)
		if id == "" {
			id = strings.TrimSpace(m.Model)
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		modelIDs = append(modelIDs, id)
		models = append(models, core.ModelInfo{
			ID:      id,
			Object:  "model",
			Created: parseCreatedAt(m.ModifiedAt),
			OwnedBy: p.name,
		})
	}

	p.mu.Lock()
	p.discovered = modelIDs
	p.mu.Unlock()

	return models, nil
}

func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []core.Message `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  *chatOptions   `json:"options,omitempty"`
	Tools    []core.Tool    `json:"tools,omitempty"`
}

type chatOptions struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	NumPredict       *int     `json:"num_predict,omitempty"`
	Stop             []string `json:"stop,omitempty"`
	Seed             *int64   `json:"seed,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
}

type chatResponse struct {
	Model           string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         nativeMessage `json:"message"`
	Done            bool          `json:"done"`
	DoneReason      string        `json:"done_reason"`
	PromptEvalCount *int          `json:"prompt_eval_count"`
	EvalCount       *int          `json:"eval_count"`
	Error           string        `json:"error"`
}

type nativeMessage struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []nativeToolCall `json:"tool_calls,omitempty"`
}

type nativeToolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function nativeFunctionCall `json:"function"`
}

type nativeFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type tagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		Model      string `json:"model"`
		ModifiedAt string `json:"modified_at"`
	} `json:"models"`
}

func buildChatRequest(ctx context.Context, req core.Request, stream bool) chatRequest {
	var maxTokens *int
	if req.MaxTokens != nil {
		maxTokens = req.MaxTokens
	} else {
		maxTokens = req.MaxCompletionTokens
	}

	var options *chatOptions
	if req.Temperature != nil || req.TopP != nil || maxTokens != nil ||
		len(req.Stop) > 0 || req.Seed != nil ||
		req.PresencePenalty != nil || req.FrequencyPenalty != nil {
		options = &chatOptions{
			Temperature:      req.Temperature,
			TopP:             req.TopP,
			NumPredict:       maxTokens,
			Stop:             req.Stop,
			Seed:             req.Seed,
			PresencePenalty:  req.PresencePenalty,
			FrequencyPenalty: req.FrequencyPenalty,
		}
	}

	// Observability for silently dropped params (issue #140): the native
	// /api/chat surface forwards only the options below plus tools.
	core.WarnUnsupportedParams(ctx, Name, req.Model, req,
		"temperature", "top_p", "max_tokens", "max_completion_tokens",
		"stop", "seed", "presence_penalty", "frequency_penalty", "tools")

	return chatRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   stream,
		Options:  options,
		Tools:    req.Tools,
	}
}

func streamChunkFromResponse(apiResp chatResponse) core.StreamChunk {
	chunk := core.StreamChunk{
		Object:  "chat.completion.chunk",
		Created: parseCreatedAt(apiResp.CreatedAt),
		Model:   apiResp.Model,
	}
	toolCalls := apiResp.Message.toCore().ToolCalls
	finishReason := core.NormalizeFinishReason(apiResp.DoneReason)
	if len(toolCalls) > 0 {
		finishReason = core.FinishReasonToolCalls
	}
	choice := core.StreamChoice{
		Index: 0,
		Delta: core.MessageDelta{
			Role:      apiResp.Message.Role,
			Content:   apiResp.Message.Content,
			ToolCalls: toolCalls,
		},
		FinishReason: finishReason,
	}
	if apiResp.Message.Role != "" || apiResp.Message.Content != "" || len(apiResp.Message.ToolCalls) > 0 || apiResp.Done || apiResp.DoneReason != "" {
		chunk.Choices = append(chunk.Choices, choice)
	}
	if apiResp.PromptEvalCount != nil || apiResp.EvalCount != nil {
		usage := usageFromCounts(apiResp.PromptEvalCount, apiResp.EvalCount)
		chunk.Usage = &usage
	}
	return chunk
}

func (m nativeMessage) toCore() core.Message {
	msg := core.Message{
		Role:    m.Role,
		Content: m.Content,
	}
	if len(m.ToolCalls) == 0 {
		return msg
	}

	msg.ToolCalls = make([]core.ToolCall, 0, len(m.ToolCalls))
	for idx, tc := range m.ToolCalls {
		callType := tc.Type
		if callType == "" {
			callType = "function"
		}
		// The native /api/chat schema omits tool-call IDs; synthesize one per
		// message so downstream tool-result correlation works. Ollama returns
		// parallel tool calls bundled in a single message, so message-scoped
		// indices stay unique.
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", idx)
		}
		msg.ToolCalls = append(msg.ToolCalls, core.ToolCall{
			ID:   id,
			Type: callType,
			Function: core.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: rawArgumentsString(tc.Function.Arguments),
			},
		})
	}
	return msg
}

func usageFromCounts(promptEvalCount, evalCount *int) core.Usage {
	usage := core.Usage{}
	if promptEvalCount != nil {
		usage.PromptTokens = *promptEvalCount
	}
	if evalCount != nil {
		usage.CompletionTokens = *evalCount
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return usage
}

func parseCreatedAt(value string) int64 {
	if value == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func rawArgumentsString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var compacted bytes.Buffer
	if err := json.Compact(&compacted, raw); err == nil {
		return compacted.String()
	}
	return string(raw)
}

func apiError(statusCode int, body []byte) error {
	msg := parseErrorMessage(body)
	if msg == "" {
		msg = http.StatusText(statusCode)
	}
	if msg == "" {
		msg = "unexpected response"
	}
	return fmt.Errorf("ollama-cloud API error (%d): %s", statusCode, msg)
}

func parseErrorMessage(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}

	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			var errString string
			if err := json.Unmarshal(envelope.Error, &errString); err == nil {
				return errString
			}
			var errObject struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			}
			if err := json.Unmarshal(envelope.Error, &errObject); err == nil {
				if errObject.Message != "" {
					return errObject.Message
				}
				if errObject.Type != "" {
					return errObject.Type
				}
				if errObject.Code != "" {
					return errObject.Code
				}
			}
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}

	msg := string(body)
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	return msg
}

func normalizeModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func combineModels(primary, secondary []string) []string {
	out := make([]string, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	for _, model := range primary {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	for _, model := range secondary {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}
