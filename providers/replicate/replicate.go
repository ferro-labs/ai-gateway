// Package replicate provides a client for the Replicate API.
package replicate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "replicate"

const (
	defaultBaseURL = "https://api.replicate.com/v1"

	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCanceled  = "canceled"

	eventMessage = "message"

	// finishReasonStop is the raw stop reason used for a completed Replicate
	// prediction; it is routed through core.NormalizeFinishReason so the gateway
	// emits the canonical OpenAI finish_reason vocabulary.
	finishReasonStop = "stop"
)

// forwardedTextParams lists the OpenAI request parameters buildTextInput
// forwards to Replicate. Any other populated parameter is reported by
// core.WarnUnsupportedParams so the drop is observable instead of silent.
var forwardedTextParams = []string{
	"max_tokens", "temperature", "top_p", "seed", "stop",
	"presence_penalty", "frequency_penalty",
}

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
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Replicate provider.
// textModels and imageModels should be "owner/name" or "owner/name:version" paths.
func New(apiToken, baseURL string, textModels, imageModels []string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}

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

// authHeader returns the Authorization header value. Replicate accepts both the
// legacy "Token" scheme and the documented "Bearer" scheme; Bearer is used.
func (p *Provider) authHeader() string { return "Bearer " + p.apiKey }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": p.authHeader()}
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
	ID        string `json:"id"`
	Status    string `json:"status"`
	Output    any    `json:"output"`
	Error     string `json:"error,omitempty"`
	StreamURL string `json:"stream_url,omitempty"`
	URLs      struct {
		Stream string `json:"stream,omitempty"`
	} `json:"urls,omitempty"`
	// Metrics carries Replicate's token accounting when the model reports it.
	Metrics struct {
		InputTokenCount  int `json:"input_token_count"`
		OutputTokenCount int `json:"output_token_count"`
	} `json:"metrics"`
}

type replicatePredictionInput struct {
	Prompt           string   `json:"prompt"`
	MaxTokens        int      `json:"max_tokens,omitempty"`
	Temperature      float64  `json:"temperature,omitempty"`
	TopP             float64  `json:"top_p,omitempty"`
	Seed             int64    `json:"seed,omitempty"`
	Stop             []string `json:"stop_sequences,omitempty"`
	PresencePenalty  float64  `json:"presence_penalty,omitempty"`
	FrequencyPenalty float64  `json:"frequency_penalty,omitempty"`
}

type replicatePredictionRequest struct {
	Version string                   `json:"version,omitempty"`
	Input   replicatePredictionInput `json:"input"`
	Stream  bool                     `json:"stream,omitempty"`
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

// buildPrompt flattens the OpenAI chat messages into Replicate's single-prompt
// input, appending a trailing "assistant:" turn to cue the completion.
func buildPrompt(req core.Request) string {
	var sb strings.Builder
	for _, msg := range req.Messages {
		sb.WriteString(msg.Role)
		sb.WriteString(": ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	sb.WriteString("assistant: ")
	return sb.String()
}

// buildTextInput translates an OpenAI-style request into Replicate's prediction
// input, forwarding the sampling parameters Replicate language models accept and
// warning (issue #140) about any populated parameter that is dropped.
func buildTextInput(ctx context.Context, req core.Request) replicatePredictionInput {
	core.WarnUnsupportedParams(ctx, Name, req.Model, req, forwardedTextParams...)

	input := replicatePredictionInput{Prompt: buildPrompt(req)}
	if req.MaxTokens != nil {
		input.MaxTokens = *req.MaxTokens
	}
	if req.Temperature != nil {
		input.Temperature = *req.Temperature
	}
	if req.TopP != nil {
		input.TopP = *req.TopP
	}
	if req.Seed != nil {
		input.Seed = *req.Seed
	}
	if len(req.Stop) > 0 {
		input.Stop = req.Stop
	}
	if req.PresencePenalty != nil {
		input.PresencePenalty = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		input.FrequencyPenalty = *req.FrequencyPenalty
	}
	return input
}

// resolveModelURL returns the prediction submission URL and the pinned version
// (empty when the model path carries no ":version" suffix) for a resolved model
// path. A pinned version posts to /predictions with the version in the body; an
// unpinned model posts to /models/{owner}/{name}/predictions.
func (p *Provider) resolveModelURL(modelPath string) (url, version string, err error) {
	if v := ModelVersion(modelPath); v != "" {
		return fmt.Sprintf("%s/predictions", p.baseURL), v, nil
	}
	escaped, err := escapeModelPath(ModelBaseName(modelPath))
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%s/models/%s/predictions", p.baseURL, escaped), "", nil
}

// escapeModelPath validates that a Replicate model base name is exactly
// "owner/name" (two non-empty, non-dot segments) and percent-escapes each
// segment. Any other shape is rejected so a crafted model value cannot alter or
// extend the /models/{owner}/{name} request path (url.PathEscape alone leaves
// "."/".." and extra "/" separators intact).
func escapeModelPath(model string) (string, error) {
	parts := strings.Split(model, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("replicate: model must be in owner/name form, got %q", model)
	}
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("replicate: invalid model path segment %q", part)
		}
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), nil
}

// predictionText coerces a Replicate prediction output (a string or an array of
// string tokens) into a single string.
func predictionText(output any) string {
	switch v := output.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

// usageFromMetrics maps Replicate's per-prediction token metrics into core.Usage.
// The second return is false when the model reported no token counts.
func usageFromMetrics(pred *Prediction) (core.Usage, bool) {
	in, out := pred.Metrics.InputTokenCount, pred.Metrics.OutputTokenCount
	if in == 0 && out == 0 {
		return core.Usage{}, false
	}
	return core.Usage{
		PromptTokens:     in,
		CompletionTokens: out,
		TotalTokens:      in + out,
	}, true
}

// Complete sends a chat completion request to Replicate and polls until done.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	url, version, err := p.resolveModelURL(p.resolveTextModel(req.Model))
	if err != nil {
		return nil, err
	}
	predReq := replicatePredictionRequest{Version: version, Input: buildTextInput(ctx, req)}

	bodyReader, _, release, err := core.JSONBodyReader(predReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	pred, err := p.submitAndPoll(ctx, url, bodyReader)
	if err != nil {
		return nil, err
	}

	resp := &core.Response{
		ID:       pred.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: predictionText(pred.Output)},
			FinishReason: core.NormalizeFinishReason(finishReasonStop),
		}},
	}
	if usage, ok := usageFromMetrics(pred); ok {
		resp.Usage = usage
	}
	return resp, nil
}

// CompleteStream submits a Replicate prediction with streaming enabled and
// translates Replicate output SSE events into OpenAI-compatible stream chunks.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	url, version, err := p.resolveModelURL(p.resolveTextModel(req.Model))
	if err != nil {
		return nil, err
	}
	predReq := replicatePredictionRequest{Version: version, Input: buildTextInput(ctx, req), Stream: true}

	bodyReader, _, release, err := core.JSONBodyReader(predReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	pred, err := p.submitPrediction(ctx, url, bodyReader)
	if err != nil {
		return nil, err
	}
	streamURL := pred.URLs.Stream
	if streamURL == "" {
		streamURL = pred.StreamURL
	}
	if streamURL == "" {
		return nil, fmt.Errorf("replicate prediction %s does not include a stream URL", pred.ID)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream request: %w", err)
	}
	httpReq.Header.Set("Authorization", p.authHeader())
	httpReq.Header.Set("Accept", "text/event-stream")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stream request failed: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, core.APIError(Name, httpResp.StatusCode, respBody)
	}

	ch := make(chan core.StreamChunk)
	go p.readStream(ctx, httpResp.Body, ch, pred.ID, req.Model)
	return ch, nil
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

	url, version, err := p.resolveModelURL(modelPath)
	if err != nil {
		return nil, err
	}
	imgReq.Version = version

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
	case []any:
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

func (p *Provider) resolveTextModel(model string) string {
	for _, m := range p.textModels {
		if ModelBaseName(m) == ModelBaseName(model) {
			return m
		}
	}
	return model
}

// submit POSTs a prediction request and returns the raw response body. When wait
// is true it sends "Prefer: wait" so Replicate holds the connection open until
// the prediction resolves instead of returning immediately.
func (p *Provider) submit(ctx context.Context, url string, body io.Reader, wait bool) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", p.authHeader())
	httpReq.Header.Set("Content-Type", "application/json")
	if wait {
		httpReq.Header.Set("Prefer", "wait")
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusCreated && httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError(Name, httpResp.StatusCode, respBody)
	}
	return respBody, nil
}

// submitPrediction submits a prediction without waiting; used by the streaming
// path, which follows the returned stream URL.
func (p *Provider) submitPrediction(ctx context.Context, url string, body io.Reader) (*Prediction, error) {
	respBody, err := p.submit(ctx, url, body, false)
	if err != nil {
		return nil, err
	}
	var pred Prediction
	if err := json.Unmarshal(respBody, &pred); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prediction: %w", err)
	}
	if pred.Status == statusFailed || pred.Status == statusCanceled {
		return nil, fmt.Errorf("replicate prediction %s: %s", pred.Status, pred.Error)
	}
	return &pred, nil
}

// submitAndPoll submits a prediction (with "Prefer: wait") and polls until it
// completes.
func (p *Provider) submitAndPoll(ctx context.Context, url string, body io.Reader) (*Prediction, error) {
	respBody, err := p.submit(ctx, url, body, true)
	if err != nil {
		return nil, err
	}
	var pred Prediction
	if err := json.Unmarshal(respBody, &pred); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prediction: %w", err)
	}
	switch pred.Status {
	case statusSucceeded:
		return &pred, nil
	case statusFailed, statusCanceled:
		return nil, fmt.Errorf("replicate prediction %s: %s", pred.Status, pred.Error)
	}
	return p.poll(ctx, pred.ID)
}

// poll repeatedly GETs a prediction until it reaches a terminal state or ctx is
// canceled.
func (p *Provider) poll(ctx context.Context, predictionID string) (*Prediction, error) {
	pollURL := fmt.Sprintf("%s/predictions/%s", p.baseURL, predictionID)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			pred, err := p.pollOnce(ctx, pollURL)
			if err != nil {
				return nil, err
			}
			switch pred.Status {
			case statusSucceeded:
				return pred, nil
			case statusFailed, statusCanceled:
				return nil, fmt.Errorf("replicate prediction %s: %s", pred.Status, pred.Error)
			}
		}
	}
}

// pollOnce performs a single GET of a prediction's current state.
func (p *Provider) pollOnce(ctx context.Context, pollURL string) (*Prediction, error) {
	pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create poll request: %w", err)
	}
	pollReq.Header.Set("Authorization", p.authHeader())

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
		return nil, core.APIError(Name, pollResp.StatusCode, pollBody)
	}
	var pred Prediction
	if err := json.Unmarshal(pollBody, &pred); err != nil {
		return nil, fmt.Errorf("failed to unmarshal poll response: %w", err)
	}
	return &pred, nil
}

// doneFinishReason extracts the Replicate stream's terminal reason (if any) from
// a "done" event payload and normalizes it to the OpenAI vocabulary, defaulting
// to "stop" when the payload carries no reason.
func doneFinishReason(payload string) string {
	reason := finishReasonStop
	if payload != "" && payload != "{}" {
		var done struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(payload), &done) == nil && done.Reason != "" {
			reason = done.Reason
		}
	}
	return core.NormalizeFinishReason(reason)
}

func (p *Provider) readStream(ctx context.Context, body io.ReadCloser, ch chan<- core.StreamChunk, predictionID, model string) {
	defer close(ch)
	defer func() { _ = body.Close() }()

	scanner := core.NewSSEScanner(body)

	event := eventMessage
	var data strings.Builder
	dispatch := func() bool {
		if data.Len() == 0 && event == eventMessage {
			return true
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		switch event {
		case "output":
			ch <- core.StreamChunk{
				ID:    predictionID,
				Model: model,
				Choices: []core.StreamChoice{{
					Index: 0,
					Delta: core.MessageDelta{Content: payload},
				}},
			}
		case "error":
			ch <- core.StreamChunk{Error: fmt.Errorf("replicate stream error: %s", payload)}
			return false
		case "done":
			ch <- core.StreamChunk{
				ID:    predictionID,
				Model: model,
				Choices: []core.StreamChoice{{
					Index:        0,
					FinishReason: doneFinishReason(payload),
				}},
			}
			return false
		}
		return true
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- core.StreamChunk{Error: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()
		if line == "" {
			if !dispatch() {
				return
			}
			event = eventMessage
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(value, " "))
			data.WriteByte('\n')
		}
	}
	if data.Len() > 0 {
		_ = dispatch()
	}
	if err := scanner.Err(); err != nil {
		ch <- core.StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
	}
}
