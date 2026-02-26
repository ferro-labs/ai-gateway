package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GeminiProvider implements the Provider interface for Google Gemini.
type GeminiProvider struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	name       string
}

// NewGemini creates a new Google Gemini provider.
func NewGemini(apiKey string, baseURL string) (*GeminiProvider, error) {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &GeminiProvider{
		httpClient: &http.Client{},
		apiKey:     apiKey,
		baseURL:    baseURL,
		name:       "gemini",
	}, nil
}

// Name returns the provider identifier.
func (p *GeminiProvider) Name() string { return p.name }

// BaseURL implements ProxiableProvider.
func (p *GeminiProvider) BaseURL() string { return p.baseURL }

// AuthHeaders implements ProxiableProvider.
// Gemini authenticates via the ?key= query parameter (added by the proxy
// director), so no Authorization header is required here.
func (p *GeminiProvider) AuthHeaders() map[string]string {
	return map[string]string{"x-goog-api-key": p.apiKey}
}

// SupportedModels returns the static list of known models for the /v1/models endpoint.
func (p *GeminiProvider) SupportedModels() []string {
	return []string{
		"gemini-2.0-flash",
		"gemini-2.0-flash-lite",
		"gemini-1.5-pro",
		"gemini-1.5-flash",
	}
}

// SupportsModel returns true if the model matches the Gemini prefix.
func (p *GeminiProvider) SupportsModel(model string) bool {
	return strings.HasPrefix(model, "gemini-")
}

// Models returns structured model metadata for the /v1/models endpoint.
func (p *GeminiProvider) Models() []ModelInfo {
	supported := p.SupportedModels()
	models := make([]ModelInfo, len(supported))
	for i, id := range supported {
		models[i] = ModelInfo{
			ID:      id,
			Object:  "model",
			OwnedBy: p.name,
		}
	}
	return models
}

// geminiPart represents a content part in Gemini API.
type geminiPart struct {
	Text string `json:"text"`
}

// geminiContent represents a content entry in Gemini API.
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

// geminiGenerationConfig holds generation parameters for Gemini API.
type geminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

// geminiRequest is the Gemini API request format.
type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

// geminiResponse is the Gemini API response format.
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

// convertMessages converts provider Messages to Gemini contents format.
// System messages are prepended to the first user message.
func convertMessagesToGemini(messages []Message) []geminiContent {
	var systemText string
	var contents []geminiContent

	for _, msg := range messages {
		if msg.Role == RoleSystem {
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

// mapGeminiFinishReason maps Gemini finish reasons to OpenAI-style reasons.
func mapGeminiFinishReason(reason string) string {
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

// Complete sends a chat completion request and returns the full response.
func (p *GeminiProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	geminiReq := geminiRequest{
		Contents: convertMessagesToGemini(req.Messages),
	}

	if req.Temperature != nil || req.MaxTokens != nil {
		geminiReq.GenerationConfig = &geminiGenerationConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		}
	}

	body, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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

	var choices []Choice
	for i, candidate := range geminiResp.Candidates {
		var text string
		for _, part := range candidate.Content.Parts {
			text += part.Text
		}
		choices = append(choices, Choice{
			Index: i,
			Message: Message{
				Role:    "assistant",
				Content: text,
			},
			FinishReason: mapGeminiFinishReason(candidate.FinishReason),
		})
	}

	return &Response{
		ID:      req.Model,
		Model:   req.Model,
		Choices: choices,
		Usage: Usage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
		},
	}, nil
}

// geminiStreamResponse is the streaming response format for Gemini.
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

// CompleteStream sends a streaming chat completion request to Gemini.
func (p *GeminiProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	geminiReq := geminiRequest{
		Contents: convertMessagesToGemini(req.Messages),
	}

	if req.Temperature != nil || req.MaxTokens != nil {
		geminiReq.GenerationConfig = &geminiGenerationConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		}
	}

	body, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?key=%s&alt=sse", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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

	ch := make(chan StreamChunk)
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

			sc := StreamChunk{
				ID:    req.Model,
				Model: req.Model,
			}
			for i, candidate := range chunk.Candidates {
				var text string
				for _, part := range candidate.Content.Parts {
					text += part.Text
				}
				sc.Choices = append(sc.Choices, StreamChoice{
					Index: i,
					Delta: MessageDelta{
						Role:    "assistant",
						Content: text,
					},
					FinishReason: mapGeminiFinishReason(candidate.FinishReason),
				})
			}
			ch <- sc
		}
		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
