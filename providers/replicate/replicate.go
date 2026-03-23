// Package replicate provides a client for the Replicate API.
package replicate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "replicate"

const defaultBaseURL = "https://api.replicate.com/v1"

// Provider implements the Replicate API client.
// It supports text generation models via chat completion and image generation
// models via the ImageProvider interface.
//
// Replicate uses an async prediction model: requests are submitted and the
// client polls until the prediction completes.
type Provider struct {
	name        string
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	textModels  []string
	imageModels []string
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Replicate provider.
// textModels and imageModels should be "owner/name" or "owner/name:version" paths.
func New(apiToken, baseURL string, textModels, imageModels []string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
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

	return &Provider{
		name:        Name,
		apiKey:      apiToken,
		baseURL:     baseURL,
		httpClient:  providerhttp.ForProvider(Name),
		textModels:  textModels,
		imageModels: imageModels,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Token " + p.apiKey}
}

// SupportedModels returns all configured models.
func (p *Provider) SupportedModels() []string {
	all := make([]string, 0, len(p.textModels)+len(p.imageModels))
	all = append(all, p.textModels...)
	all = append(all, p.imageModels...)
	return all
}

// SupportsModel returns true if the model is in the configured model lists.
func (p *Provider) SupportsModel(model string) bool {
	for _, m := range p.textModels {
		if ModelBaseName(m) == ModelBaseName(model) {
			return true
		}
	}
	for _, m := range p.imageModels {
		if ModelBaseName(m) == ModelBaseName(model) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// ModelBaseName strips the version suffix (:sha) from a model path.
func ModelBaseName(path string) string {
	if idx := strings.Index(path, ":"); idx != -1 {
		return path[:idx]
	}
	return path
}

// ModelVersion returns the version suffix after ":" in a model path, or empty
// string if no version is specified.
func ModelVersion(path string) string {
	if idx := strings.Index(path, ":"); idx != -1 {
		return path[idx+1:]
	}
	return ""
}

// Prediction represents a Replicate API prediction result.
type Prediction struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Output interface{} `json:"output"`
	Error  string      `json:"error,omitempty"`
}

type replicatePredictionInput struct {
	Prompt      string  `json:"prompt"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

type replicatePredictionRequest struct {
	Version string                   `json:"version,omitempty"`
	Input   replicatePredictionInput `json:"input"`
}

type replicateImageInput struct {
	Prompt    string `json:"prompt"`
	NumImages int    `json:"num_outputs,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

type replicateImageRequest struct {
	Version string              `json:"version,omitempty"`
	Input   replicateImageInput `json:"input"`
}

// Complete sends a chat completion request to Replicate and polls until done.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
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

	modelPath := req.Model
	for _, m := range p.textModels {
		if ModelBaseName(m) == ModelBaseName(req.Model) {
			modelPath = m
			break
		}
	}

	var url string
	if v := ModelVersion(modelPath); v != "" {
		predReq.Version = v
		url = fmt.Sprintf("%s/predictions", p.baseURL)
	} else {
		url = fmt.Sprintf("%s/models/%s/predictions", p.baseURL, ModelBaseName(modelPath))
	}

	bodyReader, _, release, err := core.JSONBodyReader(predReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	pred, err := p.submitAndPoll(ctx, url, bodyReader)
	if err != nil {
		return nil, err
	}

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

	return &core.Response{
		ID:       pred.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: text},
			FinishReason: "stop",
		}},
	}, nil
}

// GenerateImage submits an image generation prediction and polls until done.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	input := replicateImageInput{Prompt: req.Prompt}
	if req.N != nil {
		input.NumImages = *req.N
	}
	if req.Size != "" {
		var w, h int
		if n, _ := fmt.Sscanf(req.Size, "%dx%d", &w, &h); n != 2 || w <= 0 || h <= 0 {
			return nil, fmt.Errorf("invalid size %q: expected WxH format with positive integers (e.g. \"1024x1024\")", req.Size)
		}
		input.Width = w
		input.Height = h
	}

	imgReq := replicateImageRequest{Input: input}

	modelPath := req.Model
	for _, m := range p.imageModels {
		if ModelBaseName(m) == ModelBaseName(req.Model) {
			modelPath = m
			break
		}
	}

	var url string
	if v := ModelVersion(modelPath); v != "" {
		imgReq.Version = v
		url = fmt.Sprintf("%s/predictions", p.baseURL)
	} else {
		url = fmt.Sprintf("%s/models/%s/predictions", p.baseURL, ModelBaseName(modelPath))
	}

	bodyReader, _, release, err := core.JSONBodyReader(imgReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	pred, err := p.submitAndPoll(ctx, url, bodyReader)
	if err != nil {
		return nil, err
	}

	var images []core.GeneratedImage
	switch v := pred.Output.(type) {
	case string:
		images = append(images, core.GeneratedImage{URL: v})
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				images = append(images, core.GeneratedImage{URL: s})
			}
		}
	}

	return &core.ImageResponse{
		Created: time.Now().Unix(),
		Data:    images,
	}, nil
}

// submitAndPoll submits a prediction and polls until it completes.
func (p *Provider) submitAndPoll(ctx context.Context, url string, body io.Reader) (*Prediction, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Token "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Prefer", "wait")

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

	var pred Prediction
	if err := json.Unmarshal(respBody, &pred); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prediction: %w", err)
	}

	if pred.Status == "succeeded" {
		return &pred, nil
	}
	if pred.Status == "failed" || pred.Status == "canceled" {
		return nil, fmt.Errorf("replicate prediction %s: %s", pred.Status, pred.Error)
	}

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
			pollBody, readErr := io.ReadAll(pollResp.Body)
			_ = pollResp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("failed to read poll response body: %w", readErr)
			}
			if pollResp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("replicate poll error (%d): %s", pollResp.StatusCode, string(pollBody))
			}
			if err := json.Unmarshal(pollBody, &pred); err != nil {
				return nil, fmt.Errorf("failed to unmarshal poll response: %w", err)
			}

			switch pred.Status {
			case "succeeded":
				return &pred, nil
			case "failed", "canceled":
				return nil, fmt.Errorf("replicate prediction %s: %s", pred.Status, pred.Error)
			}
		}
	}
}
