// Package gemini provides a client for the Google Gemini API.
package gemini

import (
	"bufio"
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
	}
}

// SupportsModel returns true if the model is a known Gemini chat or embedding model.
func (p *Provider) SupportsModel(model string) bool {
	model = strings.TrimPrefix(model, "models/")
	if strings.HasPrefix(model, "gemini-") {
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
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
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

// convertToGemini converts gateway Messages to Gemini contents format.
// System messages are prepended to the first user message.
func convertToGemini(messages []core.Message) []geminiContent {
	var systemText string
	var contents []geminiContent

	for _, msg := range messages {
		if msg.Role == core.RoleSystem {
			if systemText != "" {
				systemText += "\n"
			}
			systemText += msg.Content
			continue
		}

		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		content := msg.Content
		if role == "user" && systemText != "" {
			content = systemText + "\n" + content
			systemText = ""
		}

		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: content}},
		})
	}

	return contents
}

// mapFinishReason maps Gemini finish reasons to OpenAI-style reasons.
func mapFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		return reason
	}
}

func buildRequest(req core.Request) (geminiRequest, error) {
	r := geminiRequest{
		Contents: convertToGemini(req.Messages),
	}
	if req.Temperature != nil || req.MaxTokens != nil {
		r.GenerationConfig = &geminiGenerationConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		}
	}
	return r, nil
}

func embeddingInputs(input interface{}) ([]string, error) {
	switch v := input.(type) {
	case string:
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		return v, nil
	case []interface{}:
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

	url := fmt.Sprintf("%s/v1beta/models/%s:batchEmbedContents?key=%s", p.baseURL, model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create embed request: %w", err)
	}
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
	geminiReq, _ := buildRequest(req)

	bodyReader, _, release, err := core.JSONBodyReader(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
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
		for _, part := range candidate.Content.Parts {
			text += part.Text
		}
		choices = append(choices, core.Choice{
			Index: i,
			Message: core.Message{
				Role:    "assistant",
				Content: text,
			},
			FinishReason: mapFinishReason(candidate.FinishReason),
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
	geminiReq, _ := buildRequest(req)

	bodyReader, _, release, err := core.JSONBodyReader(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?key=%s&alt=sse", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
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

		scanner := bufio.NewScanner(httpResp.Body)
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
				for _, part := range candidate.Content.Parts {
					text += part.Text
				}
				sc.Choices = append(sc.Choices, core.StreamChoice{
					Index: i,
					Delta: core.MessageDelta{
						Role:    "assistant",
						Content: text,
					},
					FinishReason: mapFinishReason(candidate.FinishReason),
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
