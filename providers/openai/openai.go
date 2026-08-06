// Package openai provides a client for the OpenAI API. Chat completions use a
// direct HTTP + JSON path so every core.Request field is forwarded verbatim on
// both the streaming and non-streaming paths (they cannot diverge); embeddings
// and image generation use the official Go SDK.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "openai"

// defaultBaseURL carries the /v1 segment because the base URL is the API ROOT,
// not the host: every official OpenAI client defaults to exactly this string
// (openai-python's and openai-node's default is "https://api.openai.com/v1"),
// and both append only the operation path to it.
const defaultBaseURL = "https://api.openai.com/v1"

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
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.EmbeddingProvider     = (*Provider)(nil)
	_ core.ImageProvider         = (*Provider)(nil)
	_ core.ModerationProvider    = (*Provider)(nil)
	_ core.TranscriptionProvider = (*Provider)(nil)
	_ core.SpeechProvider        = (*Provider)(nil)
	_ core.BatchProvider         = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.DiscoveryProvider     = (*Provider)(nil)
)

// New creates a new OpenAI provider.
// The optional baseURL parameter allows overriding the API endpoint (pass "" for the default).
func New(apiKey, baseURL string) (*Provider, error) {
	resolvedBase, err := core.ResolveAPIRoot(Name, baseURL, defaultBaseURL)
	if err != nil {
		return nil, err
	}
	client := oai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(sdkClient()),
		// Retry is the gateway's, and only the gateway's. openai-go retries
		// 408/409/429/5xx twice by default, which multiplies against the
		// per-target attempts the operator configured: attempts:3 against a
		// throttled upstream made nine calls, not three, and the operator has no
		// way to see the extra six. The gateway also owns the circuit breaker
		// and the concurrency limiter, both of which are bypassed by a retry the
		// SDK performs inside a single Embed call. The chat path here is raw
		// HTTP and never had a second retry authority; this makes embeddings and
		// images match it.
		option.WithMaxRetries(0),
		// The SDK is handed the SAME resolved root the hand-built URLs use.
		// Handing it the raw value instead is what made one variable mean two
		// things: chat appended /v1 and the SDK did not, so a base without the
		// suffix served chat and 404'd embeddings and images.
		option.WithBaseURL(resolvedBase+"/"),
	)
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    resolvedBase,
		httpClient: providerhttp.ForProvider(Name),
		client:     client,
	}, nil
}

// sdkClient returns the HTTP client handed to the openai-go SDK.
//
// It is the shared per-provider client with one addition: a 3xx is turned into
// a classified error before the SDK sees it.
//
// The SDK's success/failure boundary is `>= 400` (requestconfig.go), so a 3xx
// falls through to its SUCCESS decoder. Since the gateway's clients surface
// redirects rather than following them (SEC-002), a 302 carrying a JSON body
// decoded cleanly into an empty result: Embed returned no vectors and
// GenerateImage no images, both with a nil error, and the gateway served that
// as HTTP 200 costed at zero — silent wrong data (REG-003). A 3xx without a
// JSON content type instead produced an opaque SDK decode message carrying no
// status, so the gateway could not classify it either.
//
// Only the SDK client needs this. The direct-HTTP surfaces on this provider
// (chat, streaming) already treat anything other than 200 as an error.
func sdkClient() *http.Client {
	shared := providerhttp.ForProvider(Name)
	// A value copy — the shared client is pooled and must not be mutated, and
	// copying it whole is what keeps every field the pool sets. A field-by-field
	// struct literal would carry only the fields listed the day it was written
	// and drop any added later, with no compiler error and no test failure.
	c := *shared
	c.Transport = redirectGuard{base: shared.Transport}
	return &c
}

// redirectGuard converts a surfaced 3xx into a core.HTTPStatusError so the
// gateway's normal error classification applies.
//
// It wraps the shared client's transport from the outside, which means the
// otelhttp transport underneath it ends its CLIENT span before the 3xx becomes
// an error: the span records http.status_code=302 with status Unset. That is
// left alone deliberately. The HTTP semantic conventions make a client span an
// error only for 4xx/5xx or a failed request, so a 3xx really is Unset there —
// forcing it to Error would put this one provider at odds with every other
// instrumented client. The refusal is not lost: it is returned as a classified
// error and recorded on the gateway's own request span.
type redirectGuard struct{ base http.RoundTripper }

func (g redirectGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	base := g.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The body is never handed on, so it must be closed here. A real
		// transport always supplies one, but a RoundTripper is an interface and
		// a test double need not: closing a nil Body would panic inside the
		// guard rather than report the redirect it was called to report.
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, core.StatusError(Name, resp.StatusCode,
			"upstream returned a redirect; redirects are not followed because they would replay the provider credential to another host")
	}
	return resp, nil
}

// sdkError converts an openai-go failure into the gateway's typed provider
// error, so an SDK-backed call (embeddings, images) classifies exactly like the
// raw-HTTP chat path.
//
// It is the openai-go half of the adapter pattern bedrock uses for smithy
// (providers/bedrock.bedrockInvokeError): only the package that imports an SDK
// can name that SDK's error type, so each SDK gets a three-line adapter here and
// they all build the same core.HTTPStatusError. Nothing about the shared
// contract is re-decided per provider.
//
// Two deliberate omissions:
//
//   - The SDK error is NOT wrapped with %w. "Wrapping an error makes that error
//     part of your API. Do not wrap an error when doing so would expose
//     implementation details" (go.dev/blog/go1.13-errors). Exposing
//     *openai.Error would commit the gateway to openai-go's error type
//     permanently, and its Error() renders the full request URL plus the raw
//     response JSON — text that would then ride out through every %v of the
//     chain.
//   - Only apiErr.Message is taken, never apiErr.RawJSON(). Message is the
//     field the SDK decoded out of OpenAI's documented envelope; RawJSON is
//     whatever the upstream sent.
//
// An error with no HTTP response — a dial failure, a cancelled context — is
// returned untouched. It genuinely has no status, and inventing one would put
// back exactly the guesswork this contract removes.
func sdkError(err error) error {
	var apiErr *oai.Error
	if !errors.As(err, &apiErr) || apiErr.Response == nil {
		return err
	}
	return core.StatusError(Name, apiErr.StatusCode, apiErr.Message).
		WithRetryAfter(apiErr.Response.Header)
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
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

// DiscoverModels fetches the live model list from the API root's models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return core.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
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

	// base64 is refused rather than forwarded, even though OpenAI accepts it.
	// openai-go decodes the response into Embedding []float64 and its
	// forward-compatible decoder marks a field it cannot read INVALID without
	// erroring, so OpenAI's real base64 wire shape — a JSON string — returned
	// err == nil with an empty vector, which the gateway served as HTTP 200 with
	// "embedding": [] costed at zero. Nothing downstream could tell that apart
	// from a genuine result.
	//
	// Decoding it here would preserve nothing either: core.Embedding.Embedding is
	// []float64 and internal/handler/embeddings.go JSON-encodes it directly, so
	// the saving base64 exists for cannot survive past this function.
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	params.EncodingFormat = oai.EmbeddingNewParamsEncodingFormatFloat

	if req.Dimensions != nil {
		params.Dimensions = oai.Int(int64(*req.Dimensions))
	}
	if req.User != "" {
		params.User = oai.String(req.User)
	}

	result, err := p.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, sdkError(err)
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
		Model:  result.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: int(result.Usage.PromptTokens),
			TotalTokens:  int(result.Usage.TotalTokens),
		},
	}, nil
}

// GenerateImage sends an image generation request to OpenAI (DALL-E).
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	// OpenAI's answer is per-model, so the producible set is passed explicitly
	// rather than read from the provider-level map: DALL·E honours both formats,
	// gpt-image-* always returns base64 and rejects the field outright. Asking
	// gpt-image-* for a URL used to be answered with base64 and a 200.
	produced := []string{core.ImageResponseFormatURL, core.ImageResponseFormatB64JSON}
	if !isDallEModel(req.Model) {
		produced = []string{core.ImageResponseFormatB64JSON}
	}
	if err := core.EnforceImageResponseFormat(Name, req, produced...); err != nil {
		return nil, err
	}
	params := oai.ImageGenerateParams{
		Prompt: req.Prompt,
		Model:  req.Model,
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
		return nil, sdkError(err)
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
		// Only the gpt-image family reports this, and it is the only thing that
		// can price one: those models carry no per-tile catalog price and are
		// billed per token. DALL·E reports nothing here and keeps being priced
		// per image. Discarding it costed every gpt-image generation at zero.
		Usage: core.Usage{
			PromptTokens:     int(result.Usage.InputTokens),
			CompletionTokens: int(result.Usage.OutputTokens),
			TotalTokens:      int(result.Usage.TotalTokens),
		},
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
		respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		return nil, core.APIErrorFromResponse(p.name, httpResp, respBody)
	}

	// Bound decode + drain together so an oversized upstream response can't
	// exhaust memory via the decoder's internal buffering or the drain copy.
	limited := io.LimitReader(httpResp.Body, core.MaxProviderResponseBytes)
	var completion openAIChatCompletionResponse
	if err := json.NewDecoder(limited).Decode(&completion); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
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

// streamOptions carries the OpenAI stream_options object sent upstream. The
// gateway always requests usage here — deliberately ignoring
// req.StreamOptions / req.ClientStreamOptions — so cost and metrics tracking
// never depend on what the client asked for.
//
// DO NOT thread req.ClientStreamOptions (the client's real include_usage
// choice, see providers/core.Request) into this struct. That field exists so
// the gateway can honor an explicit include_usage:false on the *client-facing*
// stream, one layer up in internal/streamwrap.Meter (MeterMeta.
// SuppressUsageForClient), while this provider keeps requesting the real
// usage from OpenAI unconditionally. Making IncludeUsage here depend on the
// client's choice would stop OpenAI from ever sending the terminal usage
// chunk when a client sets include_usage:false — zeroing cost/token
// accounting for that request without any error, which for a soft spend cap
// means unlimited spend on demand.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamingRequest is a core.Request forwarded verbatim (so no field can be
// dropped, unlike a rebuilt param struct) plus the stream_options the streaming
// path always sets. The outer StreamOptions field here has JSON-encoding
// priority over the core.Request.StreamOptions field embedded through
// core.Request (shallower field wins on a tag collision), so
// core.Request.StreamOptions is never actually written to the wire by this
// struct — only IncludeUsage: true below is.
type streamingRequest struct {
	core.Request
	StreamOptions streamOptions `json:"stream_options"`
}

// CompleteStream sends a streaming chat completion request to OpenAI. It
// forwards the same raw core.Request body as Complete (adding stream:true and
// stream_options.include_usage), so streaming and non-streaming cannot diverge
// on forwarded fields such as logit_bias and multimodal image content. Usage is
// always requested upstream — unconditionally, regardless of what the client
// asked for in its own stream_options — so the final SSE chunk always carries
// token statistics for cost and metrics tracking; a client that opted out of
// the usage chunk still gets it upstream here, but has it stripped from the
// forwarded stream one layer up by internal/streamwrap.Meter instead.
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatCompletionsEndpoint(), bodyReader)
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
		respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		return nil, core.APIErrorFromResponse(p.name, httpResp, respBody)
	}

	ch := make(chan core.StreamChunk)
	// Sends are guarded by core.SendChunk: on ctx cancellation the pending send
	// is abandoned and the deferred close(ch)/Body.Close() run, so a direct
	// consumer that stops reading cannot leak this goroutine or the upstream
	// connection. Gateway-routed traffic drains through streamwrap.Meter as well.
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		scanner := core.NewSSEScanner(httpResp.Body)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data:")
			if !ok {
				continue
			}
			// The SSE spec strips a single optional space after the colon, so a
			// server that writes "data:{...}" is as legal as one that writes
			// "data: {...}". Requiring the space drops every frame of the first
			// kind, which reads downstream as an empty but successful stream.
			data = strings.TrimPrefix(data, " ")
			if data == core.SSEDone {
				return
			}
			var chunk openAIStreamChunk
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}
			sc := chunk.toStreamChunk()
			if !core.SendChunk(ctx, ch, sc) {
				return
			}
			// A mid-stream error ends the stream; nothing valid follows it.
			if sc.Error != nil {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			core.SendChunk(ctx, ch, core.StreamChunk{Error: err})
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
	return p.baseURL + "/chat/completions"
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
	// Error is the envelope OpenAI emits when a request fails once the 200
	// headers are already out. It arrives in place of choices, so decoding it
	// alongside them is what keeps a truncated answer from being reported as a
	// complete one.
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
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
	// Only a populated message counts as a failure: "error": null appears on
	// healthy frames, and a frame can carry content alongside the field.
	if c.Error != nil && c.Error.Message != "" {
		sc.Error = streamError(c.Error.Type, c.Error.Message)
	}
	return sc
}

// streamError formats a mid-stream error envelope. The type is optional — it is
// absent on some error shapes.
func streamError(typ, msg string) error {
	if typ == "" {
		return fmt.Errorf("stream error: %s", msg)
	}
	return fmt.Errorf("stream error (%s): %s", typ, msg)
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
