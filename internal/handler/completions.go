// Package handler provides the HTTP handlers for legacy OpenAI completions endpoint.
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/providers"
)

// LegacyCompletionRequest mirrors the OpenAI /v1/completions request body.
// This is the non-chat (text-only) completion format supported by models
// like gpt-3.5-turbo-instruct, deepseek-chat, etc.
//
// Prompt and Stop are decoded as json.RawMessage because OpenAI accepts both
// of them in more than one JSON shape (prompt: string | []string | []int |
// [][]int; stop: string | []string). Typing them as bare Go string/[]string
// would hard-fail json.Unmarshal for the other valid shapes, collapsing every
// one of them into the same opaque "invalid request body" 400 — including
// `stop` as a bare string, which is representable and must be accepted.
// shimPrompt/shimStop decode them per shape so each is either translated or
// refused with a message naming what is wrong.
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

// legacyChoice is a single choice in the legacy completions response envelope.
type legacyChoice struct {
	Text  string `json:"text"`
	Index int    `json:"index"`
	// Logprobs is always present and explicitly null when not requested —
	// real OpenAI never omits the key, so no omitempty here. A chat
	// completion carries no token log probabilities, so this is always nil;
	// see the "Legacy-only fields" section on Completions.
	Logprobs     any    `json:"logprobs"`
	FinishReason string `json:"finish_reason"`
}

// legacyResponse is the legacy /v1/completions response envelope, translated
// from a chat completion response.
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
// The prompt is wrapped as a single user message and routed through the same
// Gateway entry point /v1/chat/completions uses, so the configured targets,
// aliases, routing strategy, plugins, circuit breakers, per-target concurrency
// limits, metrics, and request logging all apply here. The chat response is
// then re-wrapped in the legacy completions envelope.
//
// There is deliberately no pass-through to a provider's own /v1/completions
// route. An opaque body forwarded upstream carries none of the above: a
// guardrail cannot read it, a budget cannot bill it, and a provider excluded
// from `targets` is reachable through it. Requests the chat surface cannot
// express — prompt batches, token-id prompts, streaming — are refused with an
// explicit 400 instead.
//
// # Legacy-only fields
//
// Four fields exist on /v1/completions and have no chat equivalent. One is
// honoured and three are accepted and ignored. Nothing in the response says
// which is which — an ignored field produces a perfectly ordinary 200 — so it
// is stated here and in the endpoint's operator documentation:
//
//   - echo — HONOURED. Each choice's text is the prompt followed by the
//     completion, which is what "echo back the prompt in addition to the
//     completion" means. It describes the response envelope and nothing else,
//     so the shim reproduces it exactly: the request sent upstream is
//     unchanged, and usage still counts only the tokens the model generated.
//   - suffix — ACCEPTED AND IGNORED. It is not a response format. It is text
//     the model is asked to generate INTO: the completion returned is the
//     middle that bridges prompt and suffix, and the suffix itself is never
//     part of it. A chat call cannot be conditioned on it, so appending it to
//     the answer would return text the model never wrote and that OpenAI never
//     returns — an approximation that looks right and is not.
//   - best_of — ACCEPTED AND IGNORED. It picks among candidates generated
//     server-side, ranked by log probability per token; the chat surface
//     neither generates the candidates nor scores them.
//   - logprobs — ACCEPTED AND IGNORED. A chat completion carries no token log
//     probabilities, so every choice reports logprobs: null.
//
// Ignored means exactly that: none of the three is rejected. Refusing one would
// turn a request callers send today into a 400.
func Completions(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var legacyReq LegacyCompletionRequest
		if !decodeJSONBody(w, r, &legacyReq) {
			return
		}
		if legacyReq.Model == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}
		// Admission is Route's to decide, and is left to it: this endpoint
		// accepts exactly the model names /v1/chat/completions accepts because
		// it goes through the same entry point, aliases and targets allowlist
		// included. Checking first against the registered provider set — which
		// is wider than the routable one — is what made this surface answer 400
		// where embeddings answered 404 for the same model.
		if legacyReq.Stream {
			apierror.WriteOpenAI(w,
				http.StatusBadRequest,
				"streaming is not supported on /v1/completions; use /v1/chat/completions",
				"invalid_request_error",
				"streaming_not_supported",
			)
			return
		}

		// promptText is the only prompt shape representable as a single chat
		// message. A multi-element batch, a token-id array, or an array of
		// token-id arrays cannot be represented and must be rejected.
		promptText, ok := shimPrompt(legacyReq.Prompt)
		if !ok {
			apierror.WriteOpenAI(w,
				http.StatusBadRequest,
				"prompt must be a string or single-element array; batch, token-id, and nested-array prompts are not supported",
				"invalid_request_error",
				"unsupported_parameter",
			)
			return
		}

		stopSeqs, ok := shimStop(legacyReq.Stop)
		if !ok {
			apierror.WriteOpenAI(w,
				http.StatusBadRequest,
				"stop must be a string or an array of strings",
				"invalid_request_error",
				"invalid_request",
			)
			return
		}

		// echo is applied to the response below; it says nothing about the
		// request, so nothing about it travels upstream. best_of/logprobs/suffix
		// are decoded above and neither forwarded nor rejected — see the
		// "Legacy-only fields" section on Completions for why each one.
		chatReq := providers.Request{
			Model:            legacyReq.Model,
			Messages:         []providers.Message{{Role: "user", Content: promptText}},
			MaxTokens:        legacyReq.MaxTokens,
			Temperature:      legacyReq.Temperature,
			TopP:             legacyReq.TopP,
			N:                legacyReq.N,
			Stop:             stopSeqs,
			PresencePenalty:  legacyReq.PresencePenalty,
			FrequencyPenalty: legacyReq.FrequencyPenalty,
			Seed:             legacyReq.Seed,
			User:             legacyReq.User,
		}

		attribution := &aigateway.RoutingAttribution{}
		chatResp, err := gw.Route(aigateway.WithRoutingAttribution(r.Context(), attribution), chatReq)
		attribution.SetHeaders(w.Header())
		if err != nil {
			apierror.WriteRouteError(w, err)
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
			text := c.Message.Content
			if legacyReq.Echo {
				// "Echo back the prompt in addition to the completion": the
				// prompt precedes the completion, per choice — with n > 1 each
				// candidate is echoed, because each is its own completion.
				text = promptText + text
			}
			legacy.Choices = append(legacy.Choices, legacyChoice{
				Text:         text,
				Index:        c.Index,
				FinishReason: c.FinishReason,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(legacy) //nolint:errcheck,gosec // response headers already committed; an encode error to the client cannot be reported
	}
}

// shimPrompt decodes the `prompt` field, which the chat shim can only represent
// as a single text prompt. It accepts a bare string or a single-element string
// array (equivalent to a bare string) and returns ok=false for shapes it cannot
// represent: a multi-element string array (batch), token-id arrays, and arrays
// of token-id arrays.
func shimPrompt(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var s string
	// A bare JSON null decodes into a string as a no-op, leaving "". That is
	// what "prompt": null has always produced here, so it stays accepted.
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) == 1 {
		// A null element would decode to "" by the same no-op, silently
		// turning ["prompt", null] into an empty prompt. There is no text to
		// send, so treat it as unrepresentable rather than inventing one.
		if isJSONNull(arr[0]) {
			return "", false
		}
		if err := json.Unmarshal(arr[0], &s); err == nil {
			return s, true
		}
	}
	return "", false
}

// isJSONNull reports whether raw is the JSON literal null. Decoding null into a
// string or a slice succeeds and leaves the zero value, so callers that must
// distinguish "absent" from "present but empty" have to check the bytes.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// shimStop normalizes the `stop` field (a bare string or an array of up to 4
// strings, per the OpenAI spec) into the slice form providers.Request wants.
// Both forms are representable, so this never rejects one of them.
//
// It is shared with /v1/chat/completions (DecodeChatCompletionRequest): `stop`
// is the same union on both surfaces, and two decoders for it is how they came
// to disagree about a bare string.
func shimStop(raw json.RawMessage) ([]string, bool) {
	// "stop": null means no stop sequences. Decoding it into a string succeeds
	// and yields "", which would send a single empty stop sequence upstream —
	// a different request from sending none at all.
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}, true
	}
	// A null inside the array decodes to "", which is the behaviour this
	// endpoint has always had. Anything else — a number, an object, a boolean,
	// or an array holding one — is not a stop sequence. Reporting it beats
	// silently continuing with no stop sequences at all, which is what the
	// caller would otherwise get for what is plainly a mistake on their side.
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, true
	}
	return nil, false
}
