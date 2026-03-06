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

// HuggingFaceProvider implements Provider and optional capability interfaces
// for Hugging Face Inference APIs.
type HuggingFaceProvider struct {
	Base
	httpClient *http.Client
}

// NewHuggingFace creates a new Hugging Face provider.
// If baseURL is empty, the shared Inference API is used.
func NewHuggingFace(apiKey string, baseURL string) (*HuggingFaceProvider, error) {
	if baseURL == "" {
		baseURL = "https://api-inference.huggingface.co/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &HuggingFaceProvider{
		Base:       Base{name: "hugging-face", apiKey: apiKey, baseURL: baseURL},
		httpClient: &http.Client{},
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *HuggingFaceProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known Hugging Face model examples.
func (p *HuggingFaceProvider) SupportedModels() []string {
	return []string{
		"meta-llama/Meta-Llama-3.1-8B-Instruct",
		"mistralai/Mistral-7B-Instruct-v0.3",
		"Qwen/Qwen2.5-72B-Instruct",
	}
}

// SupportsModel returns true for any model — Hugging Face validates model IDs.
func (p *HuggingFaceProvider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata.
func (p *HuggingFaceProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Hugging Face /models endpoint.
func (p *HuggingFaceProvider) DiscoverModels(ctx context.Context) ([]ModelInfo, error) {
	return discoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

type huggingFaceRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type huggingFaceResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type huggingFaceError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete sends a chat completion request to Hugging Face.
func (p *HuggingFaceProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	hfReq := huggingFaceRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(hfReq)
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
		var errResp huggingFaceError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("hugging face API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("hugging face API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var hfResp huggingFaceResponse
	if err := json.Unmarshal(respBody, &hfResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:       hfResp.ID,
		Model:    hfResp.Model,
		Provider: p.name,
		Choices:  hfResp.Choices,
		Usage:    hfResp.Usage,
	}, nil
}

type huggingFaceStreamResponse struct {
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

// CompleteStream sends a streaming chat completion request to Hugging Face.
func (p *HuggingFaceProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	hfReq := huggingFaceRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(hfReq)
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
		var errResp huggingFaceError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("hugging face API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("hugging face API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var chunk huggingFaceStreamResponse
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

// Embed sends an embedding request to Hugging Face.
func (p *HuggingFaceProvider) Embed(ctx context.Context, req EmbeddingRequest) (*EmbeddingResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
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
		return nil, fmt.Errorf("hugging face API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var resp EmbeddingResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return &resp, nil
}

// GenerateImage sends an image generation request to Hugging Face.
func (p *HuggingFaceProvider) GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal image request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/images/generations", bytes.NewReader(body))
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
		return nil, fmt.Errorf("hugging face API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var resp ImageResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return &resp, nil
}
