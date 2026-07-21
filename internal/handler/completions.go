// Package handler provides the HTTP handlers for legacy OpenAI completions endpoint.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/streamio"
	"github.com/ferro-labs/ai-gateway/providers"
)

// LegacyCompletionRequest mirrors the OpenAI /v1/completions request body.
// This is the non-chat (text-only) completion format supported by models
// like gpt-3.5-turbo-instruct, deepseek-chat, etc.
//
// Prompt and Stop are decoded as json.RawMessage because OpenAI accepts both
// of them in more than one JSON shape (prompt: string | []string | []int |
// [][]int; stop: string | []string). Typing them as bare Go string/[]string
// would hard-fail json.Unmarshal for the other valid shapes before Path 1
// (the native proxy, which forwards the body verbatim) ever gets a chance to
// forward them untouched. Path 2 (the chat shim) decodes them via
// shimPrompt/shimStop.
type LegacyCompletionRequest struct {
	Model            string             `json:"model"`
	Prompt           json.RawMessage    `json:"prompt"`
	MaxTokens        *int               `json:"max_tokens,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	N                *int               `json:"n,omitempty"`
	Stream           bool               `json:"stream,omitempty"`
	Stop             json.RawMessage    `json:"stop,omitempty"`
	PresencePenalty  *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64           `json:"frequency_penalty,omitempty"`
	Seed             *int64             `json:"seed,omitempty"`
	User             string             `json:"user,omitempty"`
	LogitBias        map[string]float64 `json:"logit_bias,omitempty"`
	LogProbs         *int               `json:"logprobs,omitempty"`
	Echo             bool               `json:"echo,omitempty"`
	BestOf           *int               `json:"best_of,omitempty"`
	Suffix           string             `json:"suffix,omitempty"`
}

// legacyChoice is a single choice in the Path 2 (chat shim) legacy response
// envelope.
type legacyChoice struct {
	Text  string `json:"text"`
	Index int    `json:"index"`
	// Logprobs is always present and explicitly null when not requested —
	// real OpenAI never omits the key, so no omitempty here. The shim never
	// computes logprobs (LogProbs/Echo/BestOf/Suffix are decoded and
	// ignored, unchanged from v1.3.0), so this is always nil.
	Logprobs     any    `json:"logprobs"`
	FinishReason string `json:"finish_reason"`
}

// legacyResponse is the Path 2 (chat shim) legacy /v1/completions response
// envelope, translated from a chat completion response.
type legacyResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []legacyChoice  `json:"choices"`
	Usage   providers.Usage `json:"usage"`
}

// Completions handles POST /v1/completions (legacy text completion API).
//
// Strategy:
//  1. If the provider supports ProxiableProvider, forward the request verbatim
//     to the upstream /v1/completions endpoint — the provider handles it natively.
//  2. Otherwise, convert the prompt to a single user message and route through
//     the chat completions path, then reformat the response.
func Completions(registry *providers.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				apierror.WriteOpenAI(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
				return
			}
			apierror.WriteOpenAI(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "invalid_request")
			return
		}

		var legacyReq LegacyCompletionRequest
		if err := json.Unmarshal(body, &legacyReq); err != nil {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error", "invalid_request")
			return
		}
		if legacyReq.Model == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}

		p, ok := registry.FindByModel(legacyReq.Model)
		if !ok {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "no provider supports model: "+legacyReq.Model, "invalid_request_error", "model_not_found")
			return
		}

		// --- Path 1: native proxy to provider's /v1/completions ---
		if pp, canProxy := p.(providers.ProxiableProvider); canProxy {
			target, err := CompletionsEndpointURL(pp.BaseURL())
			if err != nil {
				// The error embeds the operator-configured base URL verbatim,
				// which may carry a credential in a query string (self-hosted
				// OpenAI-compatible proxies). Log it server-side only; the
				// client gets a generic message.
				logging.FromContext(r.Context()).Error("invalid provider completions URL", "provider", p.Name(), "error", err)
				apierror.WriteOpenAI(w, http.StatusInternalServerError, "invalid provider configuration", "server_error", "internal_error")
				return
			}
			// Streaming clears http.Server's WriteTimeout per write, so an idle
			// upstream is bounded by cancelling this context instead.
			upstreamCtx, cancelUpstream := context.WithCancel(r.Context())
			defer cancelUpstream()

			outReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, target, bytes.NewReader(body))
			if err != nil {
				apierror.WriteOpenAI(w, http.StatusInternalServerError, "failed to create upstream request: "+err.Error(), "server_error", "internal_error")
				return
			}
			outReq.Header.Set("Content-Type", "application/json")
			for k, v := range pp.AuthHeaders() {
				outReq.Header.Set(k, v)
			}
			if legacyReq.Stream {
				outReq.Header.Set("Accept", "text/event-stream")
			}

			client := httpclient.ForProvider(p.Name())
			if legacyReq.Stream {
				client = httpclient.SharedStreaming()
			}
			resp, err := client.Do(outReq)
			if err != nil {
				apierror.WriteOpenAI(w, http.StatusBadGateway, "upstream request failed: "+err.Error(), "server_error", "upstream_error")
				return
			}
			defer func() { _ = resp.Body.Close() }()

			upstreamBody := io.Reader(resp.Body)
			if legacyReq.Stream {
				// Closing the wrapper stops its idle timer; it also closes
				// resp.Body, which net/http makes idempotent.
				idle := streamio.NewIdleReadCloser(resp.Body, streamio.IdleTimeout(), cancelUpstream)
				defer func() { _ = idle.Close() }()
				upstreamBody = idle
			}

			// Mirror status + content-type and stream the body back.
			for k, vs := range resp.Header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Gateway-Provider", p.Name())
			w.WriteHeader(resp.StatusCode)

			var copyErr error
			if legacyReq.Stream {
				_, copyErr = streamio.Copy(r.Context(), w, upstreamBody)
			} else {
				_, copyErr = io.Copy(w, upstreamBody)
			}
			// Headers are already out, so this cannot become an error response —
			// but an idle-timeout cut of a stalled upstream would otherwise be
			// invisible. A client that hung up is not worth reporting.
			if copyErr != nil && r.Context().Err() == nil {
				logging.FromContext(r.Context()).Warn("completions response copy failed",
					"provider", p.Name(), "stream", legacyReq.Stream, "error", copyErr)
			}
			return
		}

		// --- Path 2: chat-completion shim ---
		if legacyReq.Stream {
			apierror.WriteOpenAI(w,
				http.StatusBadRequest,
				"streaming is not supported for this provider on /v1/completions",
				"invalid_request_error",
				"streaming_not_supported",
			)
			return
		}

		// promptText is the only prompt shape the shim can represent as a
		// single chat message. A multi-element batch, a token-id array, or an
		// array of token-id arrays cannot be represented and must be
		// rejected — at v1.3.0 these shapes already failed with a 400 (a
		// json.Unmarshal error against the old string-typed field), so this
		// is the same status with a clearer message, not a new break.
		promptText, ok := shimPrompt(legacyReq.Prompt)
		if !ok {
			apierror.WriteOpenAI(w,
				http.StatusBadRequest,
				"prompt must be a string or single-element array for this provider (no native /v1/completions support); batch, token-id, and nested-array prompts are not supported",
				"invalid_request_error",
				"unsupported_parameter",
			)
			return
		}

		// Wrap the prompt as a user message and call through the chat path,
		// then re-wrap the response in the legacy completions envelope.
		// echo/best_of/logprobs/suffix are decoded above but intentionally
		// not forwarded or rejected here — v1.3.0 silently ignored them and
		// that behavior is unchanged; rejecting them is a /v1 breaking
		// change deferred to a later minor release.
		chatReq := providers.Request{
			Model:            legacyReq.Model,
			Messages:         []providers.Message{{Role: "user", Content: promptText}},
			MaxTokens:        legacyReq.MaxTokens,
			Temperature:      legacyReq.Temperature,
			TopP:             legacyReq.TopP,
			N:                legacyReq.N,
			Stop:             shimStop(legacyReq.Stop),
			PresencePenalty:  legacyReq.PresencePenalty,
			FrequencyPenalty: legacyReq.FrequencyPenalty,
			Seed:             legacyReq.Seed,
			User:             legacyReq.User,
		}

		chatResp, err := p.Complete(r.Context(), chatReq)
		if err != nil {
			apierror.WriteOpenAI(w, http.StatusInternalServerError, err.Error(), "server_error", "provider_error")
			return
		}

		// Translate chat response → legacy completions response.
		legacy := legacyResponse{
			ID:      chatResp.ID,
			Object:  "text_completion",
			Created: chatResp.Created,
			Model:   chatResp.Model,
			Usage:   chatResp.Usage,
			Choices: make([]legacyChoice, 0, len(chatResp.Choices)),
		}
		for _, c := range chatResp.Choices {
			legacy.Choices = append(legacy.Choices, legacyChoice{
				Text:         c.Message.Content,
				Index:        c.Index,
				FinishReason: c.FinishReason,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Provider", p.Name())
		json.NewEncoder(w).Encode(legacy) //nolint:errcheck,gosec // response headers already committed; an encode error to the client cannot be reported
	}
}

// shimPrompt decodes the `prompt` field for Path 2 (the chat shim), which can
// only represent a single text prompt. It accepts a bare string or a
// single-element string array (equivalent to a bare string) and returns
// ok=false for shapes it cannot represent: a multi-element string array
// (batch), token-id arrays, and arrays of token-id arrays. Path 1 (the native
// proxy) never calls this — it forwards the raw prompt bytes verbatim.
func shimPrompt(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) == 1 {
		if err := json.Unmarshal(arr[0], &s); err == nil {
			return s, true
		}
	}
	return "", false
}

// shimStop normalizes the `stop` field (a bare string or an array of up to 4
// strings, per the OpenAI spec) into the slice form providers.Request wants.
// Both forms are representable, so this never rejects.
func shimStop(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return nil
}

// CompletionsEndpointURL resolves the upstream /v1/completions URL from a provider base URL.
func CompletionsEndpointURL(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid provider base URL %q: %w", baseURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid provider base URL %q: absolute URL required", baseURL)
	}

	basePath := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(basePath, "/v1") {
		u.Path = basePath + "/completions"
	} else {
		u.Path = basePath + "/v1/completions"
	}

	return u.String(), nil
}
