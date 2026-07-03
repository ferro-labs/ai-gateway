// Package deepseek provides a client for the DeepSeek API.
package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical identifier for the DeepSeek provider.
	// Re-exported as providers.NameDeepSeek in providers/names.go.
	Name           = "deepseek"
	defaultBaseURL = "https://api.deepseek.com"
)

// Provider implements the core.Provider interface for DeepSeek.
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
	_ core.DiscoveryProvider = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new DeepSeek provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	// DeepSeek's chat and models paths already carry the /v1 prefix, so trim a
	// trailing /v1 to avoid doubling it when callers pass ".../v1" as the base.
	baseURL = strings.TrimSuffix(baseURL, "/v1")
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

// SupportedModels returns the static list of known DeepSeek models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"deepseek-chat",
		"deepseek-reasoner",
		"deepseek-v4-flash",
		"deepseek-v4-pro",
	}
}

// SupportsModel returns true if the model is supported by DeepSeek.
func (p *Provider) SupportsModel(model string) bool {
	return strings.HasPrefix(model, "deepseek-")
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the DeepSeek /v1/models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/v1/models", p.apiKey, p.name)
}

// ------------------------------------------------------------------ types ---

type response struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   usage         `json:"usage"`
}

// usage extends the OpenAI usage shape with DeepSeek's cache-accounting and
// reasoning fields, which the gateway's canonical usage would otherwise drop.
type usage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	PromptCacheHitTokens    int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens   int `json:"prompt_cache_miss_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// toCore maps DeepSeek usage onto the canonical usage. The cache-hit count is
// the tokens served from DeepSeek's context cache (read); the miss count is
// derivable as prompt_tokens − hit and is left off the canonical shape.
func (u usage) toCore() core.Usage {
	return core.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		ReasoningTokens:  u.CompletionTokensDetails.ReasoningTokens,
		CacheReadTokens:  u.PromptCacheHitTokens,
	}
}

// Complete sends a chat completion request to DeepSeek.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	bodyReader, _, release, err := openaicompat.BuildBody(req, false)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bodyReader) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("deepseek", httpResp.StatusCode, respBody)
	}

	var pResp response
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	// Normalize finish reasons to the canonical OpenAI vocabulary, matching the
	// shared streaming path (the hand-rolled decode here is kept only to capture
	// DeepSeek's extended cache/reasoning usage).
	for i := range pResp.Choices {
		pResp.Choices[i].FinishReason = core.NormalizeFinishReason(pResp.Choices[i].FinishReason)
	}

	return &core.Response{
		ID:       pResp.ID,
		Model:    pResp.Model,
		Provider: p.name,
		Choices:  pResp.Choices,
		Usage:    pResp.Usage.toCore(),
	}, nil
}

// CompleteStream sends a streaming chat completion request to DeepSeek.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "deepseek",
		Headers: map[string]string{
			"Authorization": "Bearer " + p.apiKey,
			"Content-Type":  "application/json",
		},
	}, req)
}
