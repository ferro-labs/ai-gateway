package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers/capabilities"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ChatResponse is the OpenAI-shaped non-streaming chat completion response body.
type ChatResponse struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

// APIError builds a provider error from a non-200 response body. It delegates to
// core.APIError, which uses the OpenAI {"error":{"message":…}} envelope when
// present and falls back to the raw body. label is the human-facing provider name.
func APIError(label string, status int, body []byte) error {
	return core.APIError(label, status, body)
}

// ChatParams configures a request to an OpenAI-compatible chat endpoint.
type ChatParams struct {
	HTTPClient *http.Client
	URL        string            // full chat-completions endpoint URL
	Headers    map[string]string // auth + content-type
	Provider   string            // sets core.Response.Provider
	Label      string            // human-facing name for error messages

	// BodyTransform, when set, reshapes the outgoing request body (e.g. to rename
	// a wire field like Mistral's seed→random_seed) while keeping the shared
	// response decoding, error handling, and finish_reason normalization. It
	// receives the request with Stream and StreamOptions already applied.
	BodyTransform func(core.Request) any

	// ExtraResponseFields, when set, captures these top-level response fields
	// (e.g. Perplexity's "citations", "search_results") into core.Response.Metadata,
	// surfacing provider-specific data the canonical response shape doesn't model.
	ExtraResponseFields []string

	// OnUnsupportedParam selects how a request parameter the provider cannot
	// express (per the capabilities matrix) is handled: warn, drop, or reject.
	// The zero value (UnsupportedParamWarn) preserves the historical
	// warn-and-forward behaviour. When left at the zero value, the mode is
	// resolved from the request context, which the gateway sets from config.
	OnUnsupportedParam core.UnsupportedParamMode
}

func newChatRequest(ctx context.Context, p ChatParams, req core.Request, stream bool) (*http.Response, func(), error) {
	// Enforce the provider parameter capability matrix (issue #207) on a local
	// copy of req before the body is built. Drop/reject only affect params the
	// provider declares Unsupported; the default warn mode is a no-op here.
	if err := enforceUnsupportedParams(ctx, p, &req); err != nil {
		return nil, nil, err
	}
	// ponytail: emission of the observability.AttrFerroForwardedParams span
	// attribute is deferred — the shared builder has no observability.Span in
	// scope, and threading one through ChatParams/context for a debug-only
	// attribute is not worth the plumbing. See observability/attributes.go.
	var (
		bodyReader io.Reader
		release    func()
		err        error
	)
	if p.BodyTransform != nil {
		req.Stream = stream
		bodyReader, _, release, err = core.JSONBodyReader(p.BodyTransform(req))
	} else {
		bodyReader, _, release, err = BuildBody(req, stream)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bodyReader)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}
	httpResp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	return httpResp, release, nil
}

// resolveUnsupportedMode returns the effective compatibility mode: the explicit
// ChatParams override when set, otherwise the gateway-supplied context value
// (which itself defaults to warn).
func resolveUnsupportedMode(ctx context.Context, p ChatParams) core.UnsupportedParamMode {
	if p.OnUnsupportedParam != core.UnsupportedParamWarn {
		return p.OnUnsupportedParam
	}
	return core.UnsupportedParamModeFromContext(ctx)
}

// enforceUnsupportedParams applies the compatibility mode to any populated
// parameter the provider declares Unsupported in the capabilities matrix.
// It mutates the caller's local copy of req only (never shared state). In warn
// mode it is a no-op: native providers already warn via core.WarnUnsupportedParams
// in their own builders, and providers routed through this builder forward
// everything (no matrix entry), so there is nothing to double-log here.
func enforceUnsupportedParams(ctx context.Context, p ChatParams, req *core.Request) error {
	mode := resolveUnsupportedMode(ctx, p)
	if mode == core.UnsupportedParamWarn {
		return nil
	}
	var offending []string
	for _, param := range capabilities.AllParams {
		if capabilities.SupportOf(p.Provider, param) == capabilities.Unsupported &&
			core.ParamPopulated(*req, param) {
			offending = append(offending, param)
		}
	}
	if len(offending) == 0 {
		return nil
	}
	if mode == core.UnsupportedParamReject {
		return core.NewUnsupportedParamError(p.Provider, offending)
	}
	// Drop mode: strip each offending param from the forwarded body, then warn once.
	for _, param := range offending {
		clearParam(req, param)
	}
	logging.FromContext(ctx).Warn(
		"provider does not support request parameter(s); dropping",
		"provider", p.Provider,
		"dropped_params", offending,
	)
	return nil
}

// clearParam zeroes the named optional parameter on req so that, with the
// omitempty JSON tags on core.Request, it is omitted from the forwarded upstream
// body. It operates on the caller's local copy of the request, never shared
// state. Unknown names are ignored.
func clearParam(req *core.Request, name string) {
	switch name {
	case "temperature":
		req.Temperature = nil
	case "top_p":
		req.TopP = nil
	case "n":
		req.N = nil
	case "seed":
		req.Seed = nil
	case "max_tokens":
		req.MaxTokens = nil
	case "max_completion_tokens":
		req.MaxCompletionTokens = nil
	case "presence_penalty":
		req.PresencePenalty = nil
	case "frequency_penalty":
		req.FrequencyPenalty = nil
	case "stop":
		req.Stop = nil
	case "tools":
		req.Tools = nil
	case "tool_choice":
		req.ToolChoice = nil
	case "response_format":
		req.ResponseFormat = nil
	case "logprobs":
		req.LogProbs = false
	case "top_logprobs":
		req.TopLogProbs = nil
	case "user":
		req.User = ""
	case "logit_bias":
		req.LogitBias = nil
	}
}

// PostChat sends a non-streaming OpenAI-compatible chat completion and decodes
// the canonical response. Providers with extended response fields (e.g. DeepSeek
// cache/reasoning usage) should decode the body themselves instead.
func PostChat(ctx context.Context, p ChatParams, req core.Request) (*core.Response, error) {
	httpResp, release, err := newChatRequest(ctx, p, req, false)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, APIError(p.Label, httpResp.StatusCode, respBody)
	}

	var pResp ChatResponse
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	// Normalize provider-specific finish reasons (e.g. Mistral's model_length)
	// to the canonical OpenAI vocabulary for every OpenAI-compatible provider.
	for i := range pResp.Choices {
		pResp.Choices[i].FinishReason = core.NormalizeFinishReason(pResp.Choices[i].FinishReason)
	}
	resp := &core.Response{
		ID:       pResp.ID,
		Model:    pResp.Model,
		Provider: p.Provider,
		Choices:  pResp.Choices,
		Usage:    pResp.Usage,
	}
	if meta := captureExtraFields(respBody, p.ExtraResponseFields); meta != nil {
		resp.Metadata = meta
	}
	return resp, nil
}

// captureExtraFields decodes the named top-level response fields into a metadata
// map, used to surface provider-specific data (e.g. Perplexity citations) that
// the canonical response shape does not model. Returns nil when nothing matches.
func captureExtraFields(body []byte, fields []string) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return nil
	}
	meta := make(map[string]any, len(fields))
	for _, k := range fields {
		rawVal, ok := raw[k]
		if !ok {
			continue
		}
		var v any
		if json.Unmarshal(rawVal, &v) == nil {
			meta[k] = v
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// PostStream sends a streaming OpenAI-compatible chat completion and returns a
// channel of decoded chunks (see StreamSSE). The non-200 body is drained and
// surfaced as an error before any goroutine is started.
func PostStream(ctx context.Context, p ChatParams, req core.Request) (<-chan core.StreamChunk, error) {
	// Request a terminal usage chunk for cost/metrics tracking unless the caller
	// already configured stream_options.
	if req.StreamOptions == nil {
		req.StreamOptions = &core.StreamOptions{IncludeUsage: true}
	}
	httpResp, release, err := newChatRequest(ctx, p, req, true)
	if err != nil {
		return nil, err
	}
	release()

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		return nil, APIError(p.Label, httpResp.StatusCode, respBody)
	}
	return StreamSSE(ctx, httpResp.Body), nil
}
