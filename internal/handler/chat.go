package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/sse"
)

// ChatCompletions handles POST /v1/chat/completions.
func ChatCompletions(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := DecodeChatCompletionRequest(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				apierror.WriteOpenAI(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
				return
			}
			apierror.WriteOpenAI(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request")
			return
		}
		if err := req.Validate(); err != nil {
			apierror.WriteOpenAI(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request")
			return
		}

		// --- Streaming path ---
		if req.Stream {
			if _, ok := gw.FindByModel(req.Model); !ok {
				apierror.WriteOpenAI(w, http.StatusBadRequest, "no provider supports model: "+req.Model, "invalid_request_error", "model_not_found")
				return
			}
			if _, ok := gw.FindStreamingByModel(req.Model); !ok {
				apierror.WriteOpenAI(w, http.StatusBadRequest, "provider does not support streaming", "invalid_request_error", "streaming_not_supported")
				return
			}

			ch, err := gw.RouteStream(r.Context(), req)
			if err != nil {
				status, errType, code := apierror.RouteErrorDetails(err)
				apierror.WriteOpenAI(w, status, err.Error(), errType, code)
				return
			}
			sse.Write(r.Context(), w, ch)
			return
		}

		// --- Non-streaming path ---
		if _, ok := gw.FindByModel(req.Model); !ok {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "no provider supports model: "+req.Model, "invalid_request_error", "model_not_found")
			return
		}

		resp, err := gw.Route(r.Context(), req)
		if err != nil {
			status, errType, code := apierror.RouteErrorDetails(err)
			apierror.WriteOpenAI(w, status, err.Error(), errType, code)
			return
		}

		if resp.OverheadMs > 0 {
			w.Header().Set("X-Gateway-Overhead-Ms", fmt.Sprintf("%.3f", resp.OverheadMs))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// Health handles GET /health.
func Health(gw *aigateway.Gateway) http.HandlerFunc {
	type providerHealth struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Models int    `json:"models"`
	}

	return func(w http.ResponseWriter, _ *http.Request) {
		var providerStatuses []providerHealth
		for _, name := range gw.ListProviders() {
			p, ok := gw.GetProvider(name)
			if !ok {
				continue
			}
			providerStatuses = append(providerStatuses, providerHealth{
				Name:   name,
				Status: "available",
				Models: len(p.Models()),
			})
		}
		if providerStatuses == nil {
			providerStatuses = []providerHealth{}
		}
		status := "ok"
		if len(providerStatuses) == 0 {
			status = "no_providers"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    status,
			"providers": providerStatuses,
		})
	}
}
