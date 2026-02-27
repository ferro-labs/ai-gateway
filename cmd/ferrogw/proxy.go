package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ferro-labs/ai-gateway/providers"
)

// proxyHandler returns an http.HandlerFunc that transparently forwards
// any /v1/* request to the matching upstream provider.
//
// This enables pass-through for endpoints the gateway does not handle
// natively (e.g. /v1/files, /v1/batches, /v1/fine_tuning, /v1/responses,
// /v1/audio/*, /v1/images/edits, /v1/realtime, etc.) while still injecting
// the correct provider authentication headers.
//
// Provider resolution order:
//  1. X-Provider request header (e.g. "X-Provider: openai")
//  2. "model" field in the JSON request body
//
// If neither resolves a provider, a 400 is returned with instructions.
func proxyHandler(registry *providers.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := resolveProvider(r, registry)
		if !ok {
			writeOpenAIError(w, http.StatusBadRequest,
				`no provider resolved; set the X-Provider header (e.g. "X-Provider: openai") or include a "model" field in the request body`,
				"invalid_request_error",
				"provider_not_resolved",
			)
			return
		}

		pp, canProxy := p.(providers.ProxiableProvider)
		if !canProxy {
			writeOpenAIError(w, http.StatusNotImplemented,
				"provider "+p.Name()+" does not support proxy pass-through",
				"invalid_request_error",
				"proxy_not_supported",
			)
			return
		}

		target, err := url.Parse(pp.BaseURL())
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "invalid provider base URL: "+err.Error(), "server_error", "internal_error")
			return
		}

		authHeaders := pp.AuthHeaders()
		providerName := p.Name()

		proxy := httputil.NewSingleHostReverseProxy(target)

		// Director rewrites the outgoing request URL and injects auth.
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			// Remove gateway-internal headers before forwarding.
			req.Header.Del("X-Provider")
			req.Header.Del("Authorization")

			// Inject provider auth headers.
			for k, v := range authHeaders {
				req.Header.Set(k, v)
			}

			// Ensure the target receives the correct Host.
			if req.Header.Get("X-Forwarded-Host") == "" {
				req.Header.Set("X-Forwarded-Host", req.Host)
			}
		}

		proxy.ModifyResponse = func(resp *http.Response) error {
			resp.Header.Set("X-Gateway-Provider", providerName)
			return nil
		}

		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "proxy error: "+err.Error(), http.StatusBadGateway)
		}

		proxy.ServeHTTP(w, r)
	}
}

// resolveProvider determines which provider should receive the request.
// It checks the X-Provider header first, then falls back to model-based lookup
// by peeking at (and restoring) the JSON request body.
func resolveProvider(r *http.Request, registry *providers.Registry) (providers.Provider, bool) {
	// 1. Explicit header takes precedence.
	if name := r.Header.Get("X-Provider"); name != "" {
		return registry.Get(name)
	}

	// 2. Try to extract "model" from the request body.
	if r.Body == nil || r.ContentLength == 0 {
		return nil, false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		return nil, false
	}
	// Restore the body so the proxy can forward it unchanged.
	r.Body = io.NopCloser(bytes.NewReader(body))

	var partial struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &partial); err != nil || partial.Model == "" {
		return nil, false
	}

	return registry.FindByModel(partial.Model)
}
