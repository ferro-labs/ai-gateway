package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ReplicateProvider implements the Provider interface for Replicate.
// It supports text generation models via chat completion and image generation
// models via the ImageProvider interface.
//
// Replicate uses an async prediction model: requests are submitted and the
// client polls until the prediction completes.
type ReplicateProvider struct {
	Base
	httpClient *http.Client
	// textModels lists model paths (owner/name) that support text generation.
	textModels []string
	// imageModels lists model paths (owner/name) that support image generation.
	imageModels []string
}

// NewReplicate creates a new Replicate provider.
// textModels and imageModels should be "owner/name" or "owner/name:version" paths.
func NewReplicate(apiToken, baseURL string, textModels, imageModels []string) (*ReplicateProvider, error) {
	if baseURL == "" {
		baseURL = "https://api.replicate.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	if len(textModels) == 0 {
		textModels = []string{
			"meta/meta-llama-3.1-405b-instruct",
			"meta/meta-llama-3.1-70b-instruct",
			"meta/meta-llama-3.1-8b-instruct",
		}
	}
	if len(imageModels) == 0 {
		imageModels = []string{
			"black-forest-labs/flux-schnell",
			"black-forest-labs/flux-dev",
			"stability-ai/sdxl",
		}
	}

	return &ReplicateProvider{
		Base:        Base{name: "replicate", apiKey: apiToken, baseURL: baseURL},
		httpClient:  &http.Client{},
		textModels:  textModels,
		imageModels: imageModels,
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *ReplicateProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Token " + p.apiKey}
}

// SupportedModels returns all configured text models.
func (p *ReplicateProvider) SupportedModels() []string {
	all := make([]string, 0, len(p.textModels)+len(p.imageModels))
	all = append(all, p.textModels...)
	all = append(all, p.imageModels...)
	return all
}

// SupportsModel returns true if the model is in the configured model lists.
func (p *ReplicateProvider) SupportsModel(model string) bool {
	for _, m := range p.textModels {
		if modelBaseName(m) == modelBaseName(model) {
			return true
		}
	}
	for _, m := range p.imageModels {
		if modelBaseName(m) == modelBaseName(model) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata.
func (p *ReplicateProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

// modelBaseName strips the version suffix (:sha) from a model path.
func modelBaseName(path string) string {
	if idx := strings.Index(path, ":"); idx != -1 {
		return path[:idx]
	}
	return path
}

// ── Replicate API types ───────────────────────────────────────────────────────

type replicatePredictionInput struct {
	Prompt      string  `json:"prompt"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

type replicatePredictionRequest struct {
	Input replicatePredictionInput `json:"input"`
}

type replicatePrediction struct {
	ID     string      `json:"id"`
	Status string      `json:"status"` // starting, processing, succeeded, failed, canceled
	Output interface{} `json:"output"` // string for text, []string for images
	Error  string      `json:"error,omitempty"`
}

type replicateImageInput struct {
	Prompt    string `json:"prompt"`
	NumImages int    `json:"num_outputs,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

type replicateImageRequest struct {
	Input replicateImageInput `json:"input"`
}

// Complete sends a chat completion request to Replicate and polls until done.
func (p *ReplicateProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	// Build prompt from messages.
	var sb strings.Builder
	for _, msg := range req.Messages {
		sb.WriteString(msg.Role)
		sb.WriteString(": ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	sb.WriteString("assistant: ")

	input := replicatePredictionInput{Prompt: sb.String()}
	if req.MaxTokens != nil {
		input.MaxTokens = *req.MaxTokens
	}
	if req.Temperature != nil {
		input.Temperature = *req.Temperature
	}

	predReq := replicatePredictionRequest{Input: input}
	body, err := json.Marshal(predReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Resolve model path (strip version for URL path).
	modelPath := req.Model
	for _, m := range p.textModels {
		if modelBaseName(m) == modelBaseName(req.Model) {
			modelPath = m
			break
		}
	}

	url := fmt.Sprintf("%s/models/%s/predictions", p.baseURL, modelBaseName(modelPath))
	pred, err := p.submitAndPoll(ctx, url, body)
	if err != nil {
		return nil, err
	}

	// Output is a list of strings (tokens); join them.
	text := ""
	switch v := pred.Output.(type) {
	case string:
		text = v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		text = strings.Join(parts, "")
	}

	return &Response{
		ID:       pred.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: RoleAssistant, Content: text},
			FinishReason: "stop",
		}},
	}, nil
}

// GenerateImage submits an image generation prediction and polls until done.
func (p *ReplicateProvider) GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error) {
	input := replicateImageInput{Prompt: req.Prompt}
	if req.N != nil {
		input.NumImages = *req.N
	}
	// Parse size (e.g. "1024x1024") into width/height.
	if req.Size != "" {
		var w, h int
		fmt.Sscanf(req.Size, "%dx%d", &w, &h) //nolint:errcheck
		input.Width = w
		input.Height = h
	}

	imgReq := replicateImageRequest{Input: input}
	body, err := json.Marshal(imgReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Resolve model path.
	modelPath := req.Model
	for _, m := range p.imageModels {
		if modelBaseName(m) == modelBaseName(req.Model) {
			modelPath = m
			break
		}
	}

	url := fmt.Sprintf("%s/models/%s/predictions", p.baseURL, modelBaseName(modelPath))
	pred, err := p.submitAndPoll(ctx, url, body)
	if err != nil {
		return nil, err
	}

	var images []GeneratedImage
	switch v := pred.Output.(type) {
	case string:
		images = append(images, GeneratedImage{URL: v})
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				images = append(images, GeneratedImage{URL: s})
			}
		}
	}

	return &ImageResponse{
		Created: time.Now().Unix(),
		Data:    images,
	}, nil
}

// submitAndPoll submits a prediction and polls until it completes.
func (p *ReplicateProvider) submitAndPoll(ctx context.Context, url string, body []byte) (*replicatePrediction, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Token "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Prefer", "wait") // hint to API to wait synchronously when possible

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusCreated && httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("replicate API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var pred replicatePrediction
	if err := json.Unmarshal(respBody, &pred); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prediction: %w", err)
	}

	// If already succeeded (Prefer: wait), return immediately.
	if pred.Status == "succeeded" {
		return &pred, nil
	}
	if pred.Status == "failed" || pred.Status == "canceled" {
		return nil, fmt.Errorf("replicate prediction %s: %s", pred.Status, pred.Error)
	}

	// Poll until done.
	pollURL := fmt.Sprintf("%s/predictions/%s", p.baseURL, pred.ID)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create poll request: %w", err)
			}
			pollReq.Header.Set("Authorization", "Token "+p.apiKey)

			pollResp, err := p.httpClient.Do(pollReq)
			if err != nil {
				return nil, fmt.Errorf("poll request failed: %w", err)
			}
			pollBody, _ := io.ReadAll(pollResp.Body)
			_ = pollResp.Body.Close()

			if err := json.Unmarshal(pollBody, &pred); err != nil {
				return nil, fmt.Errorf("failed to unmarshal poll response: %w", err)
			}

			switch pred.Status {
			case "succeeded":
				return &pred, nil
			case "failed", "canceled":
				return nil, fmt.Errorf("replicate prediction %s: %s", pred.Status, pred.Error)
			}
			// Continue polling for "starting" or "processing".
		}
	}
}
