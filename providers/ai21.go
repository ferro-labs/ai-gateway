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

// AI21Provider implements the Provider interface for AI21 Labs.
// Jamba models use the OpenAI-compatible chat completions endpoint.
// Jurassic models use the AI21-specific /complete endpoint.
type AI21Provider struct {
	Base
	httpClient *http.Client
}

// NewAI21 creates a new AI21 provider.
func NewAI21(apiKey, baseURL string) (*AI21Provider, error) {
	if baseURL == "" {
		baseURL = "https://api.ai21.com/studio/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &AI21Provider{
		Base:       Base{name: "ai21", apiKey: apiKey, baseURL: baseURL},
		httpClient: &http.Client{},
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *AI21Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known AI21 models.
func (p *AI21Provider) SupportedModels() []string {
	return []string{
		"jamba-1.5-large",
		"jamba-1.5-mini",
		"jamba-instruct",
		"j2-ultra",
		"j2-mid",
		"j2-light",
	}
}

// SupportsModel returns true for any model — AI21 validates model names.
func (p *AI21Provider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata.
func (p *AI21Provider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

// isJambaModel returns true for Jamba models that use the OpenAI-compatible endpoint.
func isJambaModel(model string) bool {
	return strings.HasPrefix(model, "jamba")
}

// ── Jamba models (OpenAI-compatible) ─────────────────────────────────────────

type ai21ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type ai21ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// ── Jurassic models (AI21-native) ────────────────────────────────────────────

type ai21CompleteRequest struct {
	Prompt        string   `json:"prompt"`
	MaxTokens     *int     `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type ai21CompleteResponse struct {
	ID          string `json:"id"`
	Completions []struct {
		Data struct {
			Text   string `json:"text"`
			Tokens []struct {
				GeneratedToken struct {
					Token   string  `json:"token"`
					Logprob float64 `json:"logprob"`
				} `json:"generatedToken"`
			} `json:"tokens"`
		} `json:"data"`
		FinishReason struct {
			Reason string `json:"reason"`
		} `json:"finishReason"`
	} `json:"completions"`
}

type ai21Error struct {
	Detail string `json:"detail"`
}

// Complete sends a chat completion request to AI21.
func (p *AI21Provider) Complete(ctx context.Context, req Request) (*Response, error) {
	if isJambaModel(req.Model) {
		return p.completeJamba(ctx, req)
	}
	return p.completeJurassic(ctx, req)
}

func (p *AI21Provider) completeJamba(ctx context.Context, req Request) (*Response, error) {
	chatReq := ai21ChatRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
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
		var errResp ai21Error
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Detail != "" {
			return nil, fmt.Errorf("ai21 API error (%d): %s", httpResp.StatusCode, errResp.Detail)
		}
		return nil, fmt.Errorf("ai21 API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var chatResp ai21ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:       chatResp.ID,
		Model:    chatResp.Model,
		Provider: p.name,
		Choices:  chatResp.Choices,
		Usage:    chatResp.Usage,
	}, nil
}

func (p *AI21Provider) completeJurassic(ctx context.Context, req Request) (*Response, error) {
	// Build a prompt from the last user message.
	prompt := ""
	for _, msg := range req.Messages {
		if msg.Role == RoleUser {
			prompt = msg.Content
		}
	}

	completeReq := ai21CompleteRequest{
		Prompt:        prompt,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
	}

	body, err := json.Marshal(completeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/%s/complete", p.baseURL, req.Model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
		var errResp ai21Error
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Detail != "" {
			return nil, fmt.Errorf("ai21 API error (%d): %s", httpResp.StatusCode, errResp.Detail)
		}
		return nil, fmt.Errorf("ai21 API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var completeResp ai21CompleteResponse
	if err := json.Unmarshal(respBody, &completeResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Translate AI21 native response to normalized Response.
	var choices []Choice
	for i, c := range completeResp.Completions {
		choices = append(choices, Choice{
			Index: i,
			Message: Message{
				Role:    RoleAssistant,
				Content: c.Data.Text,
			},
			FinishReason: c.FinishReason.Reason,
		})
	}

	return &Response{
		ID:       completeResp.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices:  choices,
	}, nil
}

type ai21StreamResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
}

// CompleteStream sends a streaming chat completion request to AI21.
// Only Jamba models support streaming via the OpenAI-compatible endpoint.
func (p *AI21Provider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	if !isJambaModel(req.Model) {
		return nil, fmt.Errorf("streaming is only supported for Jamba models; use a jamba-* model for streaming")
	}

	chatReq := ai21ChatRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
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
		var errResp ai21Error
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Detail != "" {
			return nil, fmt.Errorf("ai21 API error (%d): %s", httpResp.StatusCode, errResp.Detail)
		}
		return nil, fmt.Errorf("ai21 API error (%d): %s", httpResp.StatusCode, string(respBody))
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
			if data == SSEDone {
				return
			}

			var chunk ai21StreamResponse
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}

			sc := StreamChunk{ID: chunk.ID, Model: chunk.Model}
			for _, c := range chunk.Choices {
				sc.Choices = append(sc.Choices, StreamChoice{
					Index: c.Index,
					Delta: MessageDelta{
						Role:    c.Delta.Role,
						Content: c.Delta.Content,
					},
					FinishReason: c.FinishReason,
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
