// Package ai21 provides a client for the AI21 Labs API (Jamba and Jurassic models).
package ai21

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

// Name is the canonical provider identifier.
const Name = "ai21"

const defaultBaseURL = "https://api.ai21.com/studio/v1"

// Provider implements the AI21 Labs API client.
// Jamba models use the OpenAI-compatible chat completions endpoint.
// Jurassic models use the AI21-specific /complete endpoint.
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
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new AI21 provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}
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
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known AI21 models. AI21 is Jamba-only; the legacy
// Jurassic (j2-*) models are deprecated and no longer advertised.
func (p *Provider) SupportedModels() []string {
	return []string{
		"jamba-large-1.7",
		"jamba-mini-1.7",
		"jamba-1.5-large",
		"jamba-1.5-mini",
	}
}

// SupportsModel returns true for any model — AI21 validates model names.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// IsJambaModel returns true for Jamba models that use the OpenAI-compatible endpoint.
func IsJambaModel(model string) bool {
	return strings.HasPrefix(model, "jamba")
}

// chatParams builds the shared OpenAI-compatible request parameters for the
// Jamba chat-completions endpoint.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      Name,
		Headers: map[string]string{
			"Authorization": "Bearer " + p.apiKey,
			"Content-Type":  "application/json",
		},
	}
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
			Text string `json:"text"`
		} `json:"data"`
		FinishReason struct {
			Reason string `json:"reason"`
		} `json:"finishReason"`
	} `json:"completions"`
}

// Complete sends a chat completion request to AI21.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	if IsJambaModel(req.Model) {
		return p.completeJamba(ctx, req)
	}
	return p.completeJurassic(ctx, req)
}

func (p *Provider) completeJamba(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// completeJurassic sends a request to AI21's native /complete endpoint.
//
// Legacy: AI21's /complete (Jurassic) endpoint and the j2-* models are
// deprecated. AI21 is Jamba-only going forward; this path is kept functional for
// callers still pinned to Jurassic models but is no longer advertised.
func (p *Provider) completeJurassic(ctx context.Context, req core.Request) (*core.Response, error) {
	prompt := ""
	for _, msg := range req.Messages {
		if msg.Role == core.RoleUser {
			prompt = msg.Content
		}
	}

	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req,
		"max_tokens", "temperature", "top_p", "stop")

	completeReq := ai21CompleteRequest{
		Prompt:        prompt,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
	}

	bodyReader, _, release, err := core.JSONBodyReader(completeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	url := fmt.Sprintf("%s/%s/complete", p.baseURL, req.Model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
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
		return nil, core.APIError(Name, httpResp.StatusCode, respBody)
	}

	var completeResp ai21CompleteResponse
	if err := json.Unmarshal(respBody, &completeResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var choices []core.Choice
	for i, c := range completeResp.Completions {
		choices = append(choices, core.Choice{
			Index: i,
			Message: core.Message{
				Role:    core.RoleAssistant,
				Content: c.Data.Text,
			},
			FinishReason: core.NormalizeFinishReason(c.FinishReason.Reason),
		})
	}

	return &core.Response{
		ID:       completeResp.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices:  choices,
	}, nil
}

// CompleteStream sends a streaming chat completion request to AI21.
// Only Jamba models support streaming via the OpenAI-compatible endpoint.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	if !IsJambaModel(req.Model) {
		return nil, fmt.Errorf("streaming is only supported for Jamba models; use a jamba-* model for streaming")
	}
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
