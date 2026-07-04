// Package openai provides a client for the OpenAI API. Chat completions use a
// direct HTTP + JSON path so every core.Request field is forwarded verbatim on
// both the streaming and non-streaming paths (they cannot diverge); embeddings
// and image generation use the official Go SDK.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "openai"

const defaultBaseURL = "https://api.openai.com"

// Provider implements the OpenAI API client using the official Go SDK.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
	client     oai.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new OpenAI provider.
// The optional baseURL parameter allows overriding the API endpoint (pass "" for the default).
func New(apiKey, baseURL string) (*Provider, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(providerhttp.ForProvider(Name)),
	}
	resolvedBase := defaultBaseURL
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("openai: invalid base URL %q: must be http or https with a host", baseURL)
		}
		opts = append(opts, option.WithBaseURL(baseURL))
		resolvedBase = baseURL
	}
	client := oai.NewClient(opts...)
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(resolvedBase, "/"),
		httpClient: providerhttp.ForProvider(Name),
		client:     client,
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

// SupportedModels returns the list of models supported by this provider.
func (p *Provider) SupportedModels() []string {
	return []string{
		// GPT-4o family
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-4o-2024-11-20",
		"gpt-4o-2024-08-06",
		"gpt-4o-mini-2024-07-18",
		// GPT-4.1 family
		"gpt-4.1",
		"gpt-4.1-mini",
		"gpt-4.1-nano",
		"gpt-4.1-2025-04-14",
		// GPT-4 legacy
		"gpt-4-turbo",
		"gpt-4-turbo-2024-04-09",
		"gpt-4",
		// GPT-3.5
		"gpt-3.5-turbo",
		// o-series reasoning
		"o1",
		"o1-mini",
		"o1-preview",
		"o3",
		"o3-mini",
		"o4-mini",
		// ChatGPT
		"chatgpt-4o-latest",
	}
}

// SupportsModel returns true if the model matches known OpenAI prefixes.
func (p *Provider) SupportsModel(model string) bool {
	for _, prefix := range []string{"gpt-", "chatgpt-", "codex-", "sora-", "dall-e-", "whisper-", "tts-", "text-embedding-", "ft:", "babbage-", "davinci-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	if len(model) >= 2 && model[0] == 'o' && model[1] >= '0' && model[1] <= '9' {
		return true
	}
	return false
}

// Models returns model information for all supported models.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the OpenAI /v1/models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	url := p.baseURL + "/v1/models"
	if strings.HasSuffix(p.baseURL, "/v1") {
		url = p.baseURL + "/models"
	}
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, url, p.apiKey, p.name)
}

// Embed sends an embedding request to OpenAI.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	params := oai.EmbeddingNewParams{
		Model: req.Model,
	}

	normalized, err := core.NormalizeEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}
	switch v := normalized.(type) {
	case string:
		params.Input = oai.EmbeddingNewParamsInputUnion{OfString: oai.String(v)}
	case []string:
		params.Input = oai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: v}
	}

	switch req.EncodingFormat {
	case "", "float":
		params.EncodingFormat = oai.EmbeddingNewParamsEncodingFormatFloat
	case "base64":
		params.EncodingFormat = oai.EmbeddingNewParamsEncodingFormatBase64
	default:
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; valid values are \"float\" and \"base64\"", req.EncodingFormat)
	}

	if req.Dimensions != nil {
		params.Dimensions = oai.Int(int64(*req.Dimensions))
	}
	if req.User != "" {
		params.User = oai.String(req.User)
	}

	result, err := p.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, err
	}

	embeddings := make([]core.Embedding, len(result.Data))
	for i, d := range result.Data {
		embeddings[i] = core.Embedding{
			Object:    string(d.Object),
			Embedding: d.Embedding,
			Index:     int(d.Index),
		}
	}

	return &core.EmbeddingResponse{
		Object: string(result.Object),
		Data:   embeddings,
		Model:  string(result.Model),
		Usage: core.EmbeddingUsage{
			PromptTokens: int(result.Usage.PromptTokens),
			TotalTokens:  int(result.Usage.TotalTokens),
		},
	}, nil
}

// GenerateImage sends an image generation request to OpenAI (DALL-E).
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	params := oai.ImageGenerateParams{
		Prompt: req.Prompt,
		Model:  oai.ImageModel(req.Model),
	}
	if req.N != nil {
		params.N = oai.Int(int64(*req.N))
	}
	if req.Size != "" {
		params.Size = oai.ImageGenerateParamsSize(req.Size)
	}
	if req.Quality != "" {
		params.Quality = oai.ImageGenerateParamsQuality(req.Quality)
	}
	if req.Style != "" {
		params.Style = oai.ImageGenerateParamsStyle(req.Style)
	}
	// response_format is only valid for the DALL·E models. gpt-image-* rejects it
	// and always returns base64, so omit it entirely for that family and read the
	// result from B64JSON.
	if isDallEModel(req.Model) {
		if req.ResponseFormat == "b64_json" {
			params.ResponseFormat = oai.ImageGenerateParamsResponseFormatB64JSON
		} else {
			params.ResponseFormat = oai.ImageGenerateParamsResponseFormatURL
		}
	}
	if req.User != "" {
		params.User = oai.String(req.User)
	}

	result, err := p.client.Images.Generate(ctx, params)
	if err != nil {
		return nil, err
	}

	images := make([]core.GeneratedImage, len(result.Data))
	for i, d := range result.Data {
		images[i] = core.GeneratedImage{
			URL:           d.URL,
			B64JSON:       d.B64JSON,
			RevisedPrompt: d.RevisedPrompt,
		}
	}

	return &core.ImageResponse{
		Created: result.Created,
		Data:    images,
	}, nil
}

// Complete sends a chat completion request to OpenAI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	req.Stream = false
	// o-series reasoning models reject max_tokens; the gateway seam leaves both
	// token fields populated, so forward only the modern max_completion_tokens.
	req.PreferCompletionTokens()

	bodyReader, _, release, err := core.JSONBodyReader(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatCompletionsEndpoint(), bodyReader) //nolint:gosec // baseURL validated in New()
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

	if httpResp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		return nil, core.APIError(p.name, httpResp.StatusCode, respBody)
	}

	var completion openAIChatCompletionResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&completion); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if _, err := io.Copy(io.Discard, httpResp.Body); err != nil {
		return nil, fmt.Errorf("failed to drain response: %w", err)
	}

	return &core.Response{
		ID:       completion.ID,
		Object:   completion.Object,
		Created:  completion.Created,
		Model:    completion.Model,
		Provider: p.name,
		Choices:  completion.Choices,
		Usage: core.Usage{
			PromptTokens:     completion.Usage.PromptTokens,
			CompletionTokens: completion.Usage.CompletionTokens,
			TotalTokens:      completion.Usage.TotalTokens,
			ReasoningTokens:  completion.Usage.CompletionTokensDetails.ReasoningTokens,
			CacheReadTokens:  completion.Usage.PromptTokensDetails.CachedTokens,
		},
	}, nil
}

// streamOptions carries the OpenAI stream_options object. The gateway always
// requests usage so cost and metrics tracking work regardless of what the client
// sent.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamingRequest is a core.Request forwarded verbatim (so no field can be
// dropped, unlike a rebuilt param struct) plus the stream_options the streaming
// path always sets.
type streamingRequest struct {
	core.Request
	StreamOptions streamOptions `json:"stream_options"`
}

// CompleteStream sends a streaming chat completion request to OpenAI. It
// forwards the same raw core.Request body as Complete (adding stream:true and
// stream_options.include_usage), so streaming and non-streaming cannot diverge
// on forwarded fields such as logit_bias and multimodal image content. Usage is
// always requested so the final SSE chunk carries token statistics for cost and
// metrics tracking.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	// o-series reasoning models reject max_tokens; keep only the modern field.
	req.PreferCompletionTokens()
	sreq := streamingRequest{Request: req, StreamOptions: streamOptions{IncludeUsage: true}}
	sreq.Stream = true

	bodyReader, _, release, err := core.JSONBodyReader(sreq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatCompletionsEndpoint(), bodyReader) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq) //nolint:gosec // baseURL validated in New()
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		return nil, core.APIError(p.name, httpResp.StatusCode, respBody)
	}

	ch := make(chan core.StreamChunk)
	// The producer uses unguarded blocking sends (ch <- ...), matching every
	// other provider. It stays leak-free because the gateway routes every
	// provider stream through streamwrap.Meter, which ALWAYS drains this channel
	// to completion — even on consumer abandonment or context cancellation — so
	// this goroutine reaches close(ch) and Body.Close(). See the "Stream-send
	// drain contract" in internal/streamwrap.
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		scanner := core.NewSSEScanner(httpResp.Body)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			if data == core.SSEDone {
				return
			}
			var chunk openAIStreamChunk
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}
			ch <- chunk.toStreamChunk()
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}

// openAIUsage is the OpenAI usage object with the nested reasoning/cached token
// detail. It is shared by the streaming and non-streaming response decoders so
// the token-accounting fields stay aligned; the canonical core.Usage decoder
// does not capture the nested detail.
type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type openAIChatCompletionResponse struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   openAIUsage   `json:"usage"`
}

func (p *Provider) chatCompletionsEndpoint() string {
	if strings.HasSuffix(p.baseURL, "/v1") {
		return p.baseURL + "/chat/completions"
	}
	return p.baseURL + "/v1/chat/completions"
}

// isDallEModel reports whether the image model is a DALL·E model — the only
// family whose image API accepts the response_format parameter.
func isDallEModel(model string) bool {
	return strings.HasPrefix(model, "dall-e")
}

// openAIStreamChunk is one OpenAI chat.completion.chunk SSE frame. It carries the
// nested usage-detail fields (reasoning / cached tokens) that the canonical
// core.Usage decoder does not, so streaming preserves the same accounting as the
// non-streaming path.
type openAIStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string                 `json:"role"`
			Content   string                 `json:"content"`
			ToolCalls []openAIStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

type openAIStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toStreamChunk converts a decoded OpenAI SSE frame to the gateway's canonical
// stream chunk, carrying usage (including reasoning/cache detail) on the frame
// that reports it. OpenAI finish reasons are already canonical.
func (c openAIStreamChunk) toStreamChunk() core.StreamChunk {
	sc := core.StreamChunk{ID: c.ID, Model: c.Model}
	for _, choice := range c.Choices {
		sc.Choices = append(sc.Choices, core.StreamChoice{
			Index: choice.Index,
			Delta: core.MessageDelta{
				Role:      choice.Delta.Role,
				Content:   choice.Delta.Content,
				ToolCalls: mapStreamToolCalls(choice.Delta.ToolCalls),
			},
			FinishReason: choice.FinishReason,
		})
	}
	if c.Usage != nil && c.Usage.TotalTokens > 0 {
		usage := &core.Usage{
			PromptTokens:     c.Usage.PromptTokens,
			CompletionTokens: c.Usage.CompletionTokens,
			TotalTokens:      c.Usage.TotalTokens,
		}
		if c.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
			usage.ReasoningTokens = c.Usage.CompletionTokensDetails.ReasoningTokens
		}
		if c.Usage.PromptTokensDetails.CachedTokens > 0 {
			usage.CacheReadTokens = c.Usage.PromptTokensDetails.CachedTokens
		}
		sc.Usage = usage
	}
	return sc
}

// mapStreamToolCalls maps SSE tool-call deltas to canonical tool calls,
// preserving the streaming index each fragment belongs to.
func mapStreamToolCalls(in []openAIStreamToolCall) []core.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]core.ToolCall, 0, len(in))
	for _, tc := range in {
		index := tc.Index
		out = append(out, core.ToolCall{
			Index: &index,
			ID:    tc.ID,
			Type:  tc.Type,
			Function: core.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return out
}
