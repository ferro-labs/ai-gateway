package main

import (
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
)

// imagesHandler handles POST /v1/images/generations.
// It routes image generation requests to the first registered ImageProvider that
// supports the requested model.
func imagesHandler(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req providers.ImageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error", "invalid_request")
			return
		}
		if req.Model == "" {
			writeOpenAIError(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}
		if req.Prompt == "" {
			writeOpenAIError(w, http.StatusBadRequest, "prompt is required", "invalid_request_error", "invalid_request")
			return
		}

		resp, err := gw.GenerateImage(r.Context(), req)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "routing_error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
