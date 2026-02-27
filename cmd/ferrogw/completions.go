// Package main provides the HTTP handlers for legacy OpenAI completions endpoint.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers"
)

// LegacyCompletionRequest mirrors the OpenAI /v1/completions request body.
// This is the non-chat (text-only) completion format supported by models
// like gpt-3.5-turbo-instruct, deepseek-chat, etc.
type LegacyCompletionRequest struct {
	Model            string             `json:"model"`
	Prompt           string             `json:"prompt"`
	MaxTokens        *int               `json:"max_tokens,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	N                *int               `json:"n,omitempty"`
	Stream           bool               `json:"stream,omitempty"`
	Stop             []string           `json:"stop,omitempty"`
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

// completionsHandler handles POST /v1/completions (legacy text completion API).
//
// Strategy:
//  1. If the provider supports ProxiableProvider, forward the request verbatim
//     to the upstream /v1/completions endpoint — the provider handles it natively.
//  2. Otherwise, convert the prompt to a single user message and route through
//     the chat completions path, then reformat the response.
func completionsHandler(registry *providers.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "invalid_request")
			return
		}

		var legacyReq LegacyCompletionRequest
		if err := json.Unmarshal(body, &legacyReq); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error", "invalid_request")
			return
		}
		if legacyReq.Model == "" {
			writeOpenAIError(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}

		p, ok := registry.FindByModel(legacyReq.Model)
		if !ok {
			writeOpenAIError(w, http.StatusBadRequest, "no provider supports model: "+legacyReq.Model, "invalid_request_error", "model_not_found")
			return
		}

		// --- Path 1: native proxy to provider's /v1/completions ---
		if pp, canProxy := p.(providers.ProxiableProvider); canProxy {
			target := pp.BaseURL() + "/v1/completions"
			outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
			if err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, "failed to create upstream request: "+err.Error(), "server_error", "internal_error")
				return
			}
			outReq.Header.Set("Content-Type", "application/json")
			for k, v := range pp.AuthHeaders() {
				outReq.Header.Set(k, v)
			}
			if legacyReq.Stream {
				outReq.Header.Set("Accept", "text/event-stream")
			}

			resp, err := http.DefaultClient.Do(outReq)
			if err != nil {
				writeOpenAIError(w, http.StatusBadGateway, "upstream request failed: "+err.Error(), "server_error", "upstream_error")
				return
			}
			defer func() { _ = resp.Body.Close() }()

			// Mirror status + content-type and stream the body back.
			for k, vs := range resp.Header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Gateway-Provider", p.Name())
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body) //nolint:errcheck,gosec
			return
		}

		// --- Path 2: chat-completion shim ---
		// Wrap the prompt as a user message and call through the chat path,
		// then re-wrap the response in the legacy completions envelope.
		chatReq := providers.Request{
			Model:            legacyReq.Model,
			Messages:         []providers.Message{{Role: "user", Content: legacyReq.Prompt}},
			MaxTokens:        legacyReq.MaxTokens,
			Temperature:      legacyReq.Temperature,
			TopP:             legacyReq.TopP,
			N:                legacyReq.N,
			Stop:             legacyReq.Stop,
			PresencePenalty:  legacyReq.PresencePenalty,
			FrequencyPenalty: legacyReq.FrequencyPenalty,
			Seed:             legacyReq.Seed,
			User:             legacyReq.User,
		}

		chatResp, err := p.Complete(r.Context(), chatReq)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "provider_error")
			return
		}

		// Translate chat response → legacy completions response.
		type legacyChoice struct {
			Text         string `json:"text"`
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
		}
		type legacyResponse struct {
			ID      string          `json:"id"`
			Object  string          `json:"object"`
			Model   string          `json:"model"`
			Choices []legacyChoice  `json:"choices"`
			Usage   providers.Usage `json:"usage"`
		}

		legacy := legacyResponse{
			ID:     chatResp.ID,
			Object: "text_completion",
			Model:  chatResp.Model,
			Usage:  chatResp.Usage,
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
		json.NewEncoder(w).Encode(legacy) //nolint:errcheck,gosec
	}
}
