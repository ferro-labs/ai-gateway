package main

import (
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
)

// embeddingsHandler handles POST /v1/embeddings.
// It routes embedding requests to the first registered EmbeddingProvider that
// supports the requested model.
func embeddingsHandler(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req providers.EmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error", "invalid_request")
			return
		}
		if req.Model == "" {
			writeOpenAIError(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}
		if req.Input == nil {
			writeOpenAIError(w, http.StatusBadRequest, "input is required", "invalid_request_error", "invalid_request")
			return
		}

		resp, err := gw.Embed(r.Context(), req)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "routing_error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
