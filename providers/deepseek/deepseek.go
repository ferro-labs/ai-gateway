// Package deepseek provides a client for the DeepSeek API.
package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

const (
	// Name is the canonical identifier for the DeepSeek provider.
	// Re-exported as providers.NameDeepSeek in providers/names.go.
	Name           = "deepseek"
	defaultBaseURL = "https://api.deepseek.com/v1"
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

// SupportsModel returns true if the model is supported by DeepSeek.
func (p *Provider) SupportsModel(model string) bool {
	return strings.HasPrefix(model, "deepseek-")
}

// DiscoverModels fetches the live model list from the DeepSeek /v1/models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return core.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// ------------------------------------------------------------------ types ---

// deepseekChatBody reshapes the OpenAI-shaped chat body for DeepSeek, whose
// documented end-user identifier is "user_id", not OpenAI's "user"
// (https://api-docs.deepseek.com/api/create-chat-completion). The embedded
// core.Request forwards every other field unchanged. The User field shadows the
// promoted core.Request.User at a shallower depth and is left empty (omitempty)
// so "user" is never emitted; UserID carries the value under DeepSeek's key.
//
// A json:"-" shadow does not work here: encoding/json drops "-" fields
// entirely, so the promoted core.Request.User would still be emitted. A
// same-named field that dominates by depth and is omitted via omitempty is what
// actually suppresses it — the same shape providers/mistral uses for
// seed→random_seed.
type deepseekChatBody struct {
	core.Request
	User   string `json:"user,omitempty"`    // shadows core.Request.User; always empty so "user" is suppressed
	UserID string `json:"user_id,omitempty"` // DeepSeek's end-user identifier field
}

// deepseekChatTransform maps core.Request onto DeepSeek's chat body, renaming
// user → user_id.
func deepseekChatTransform(req core.Request) any {
	return deepseekChatBody{Request: req, UserID: req.User}
}

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
	// This path builds its own body rather than going through
	// openaicompat.PostChat, so it has to apply the capability matrix itself —
	// PostChat and PostStream do it for every other surface.
	if err := openaicompat.EnforceParams(ctx, p.chatParams(), &req); err != nil {
		return nil, err
	}
	// Not openaicompat.BuildBody: this surface has to apply the same
	// user → user_id rename PostStream gets from chatParams().BodyTransform,
	// or the two surfaces would send different keys for the same field.
	req.Stream = false
	bodyReader, _, release, err := core.JSONBodyReader(deepseekChatTransform(req))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bodyReader) //nolint:gosec // baseURL validated in New()
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

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse("deepseek", httpResp, respBody)
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

// chatParams is the shared upstream description of DeepSeek's chat endpoint,
// used by CompleteStream and by Complete's capability-matrix enforcement so the
// two surfaces cannot disagree about which provider they are.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "deepseek",
		Headers: map[string]string{
			"Authorization": "Bearer " + p.apiKey,
			"Content-Type":  "application/json",
		},
		BodyTransform: deepseekChatTransform,
	}
}

// CompleteStream sends a streaming chat completion request to DeepSeek.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
