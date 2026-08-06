package handler

import (
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Embeddings handles POST /v1/embeddings.
// It routes embedding requests to the first registered EmbeddingProvider that
// supports the requested model.
func Embeddings(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req providers.EmbeddingRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		if req.Model == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}
		if req.Input == nil {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "input is required", "invalid_request_error", "invalid_request")
			return
		}

		// Resolved once, here, so no provider sees a format the response type
		// cannot hold — azure-openai and the shared OpenAI-compatible body
		// forward this field verbatim. See core.NormalizeEncodingFormat for why
		// "base64" is accepted rather than refused.
		req.EncodingFormat = core.NormalizeEncodingFormat(req.EncodingFormat)

		resp, err := gw.Embed(r.Context(), req)
		if err != nil {
			apierror.WriteRouteError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
