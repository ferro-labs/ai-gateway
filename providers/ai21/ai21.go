// Package ai21 provides a client for the AI21 Labs API (Jamba and Jurassic models).
package ai21

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
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
	baseURL, err := core.ResolveAPIRoot(Name, baseURL, defaultBaseURL)
	if err != nil {
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

// SupportsModel returns true for any model — AI21 validates model names.
func (p *Provider) SupportsModel(_ string) bool { return true }

// IsJambaModel returns true for Jamba models that use the OpenAI-compatible endpoint.
func IsJambaModel(model string) bool {
	return strings.HasPrefix(model, "jamba")
}

// escapeModelSegment percent-escapes a model id for use as the single path
// segment the Jurassic /complete route is keyed by. Empty, dot ("."/"..") and
// separator-bearing values are rejected: url.PathEscape leaves those intact, so
// they would otherwise let a caller-supplied model reach a different AI21 route
// with the operator's API key attached.
func escapeModelSegment(model string) (string, error) {
	if model == "" || model == "." || model == ".." || strings.ContainsAny(model, `/\`) {
		return "", fmt.Errorf("ai21: invalid model %q", model)
	}
	return url.PathEscape(model), nil
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
	escaped, err := escapeModelSegment(req.Model)
	if err != nil {
		return nil, err
	}

	// /complete has one input field and no system channel, so the conversation is
	// flattened into it by the shared join every single-prompt provider uses. An
	// image part has no field to travel in and is refused with a 400 rather than
	// dropped under a 200 — see core.JoinMessagesAsPrompt.
	prompt, err := core.JoinMessagesAsPrompt(Name, req.Model, req.Messages)
	if err != nil {
		return nil, err
	}

	// AI21 is dual-path: Jamba speaks the OpenAI-compatible chat API (and forwards
	// everything through the shared builder), while the deprecated Jurassic
	// /complete endpoint expresses only these four. The support set is therefore a
	// property of the ENDPOINT, not of the provider, so it is declared here rather
	// than as a provider-level entry in the capability matrix — the same treatment
	// Bedrock gets for its per-model families.
	if err := core.EnforceUnsupportedParamsList(ctx, p.Name(), req.Model, req,
		"max_tokens", "temperature", "top_p", "stop"); err != nil {
		return nil, err
	}

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

	completeURL := fmt.Sprintf("%s/%s/complete", p.baseURL, escaped)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, completeURL, bodyReader)
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

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse(Name, httpResp, respBody)
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
		// 400, not the 500 a bare error classifies as: the caller named a model
		// this provider cannot stream, which only the caller can fix and no
		// retry can change.
		return nil, core.StatusError(Name, http.StatusBadRequest,
			"streaming is only supported for Jamba models; use a jamba-* model for streaming")
	}
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
