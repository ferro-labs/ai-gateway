package handler

import (
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Rerank handles POST /v1/rerank.
// It routes rerank requests to the first registered RerankProvider that serves
// the requested model.
func Rerank(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req providers.RerankRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		if req.Model == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}
		if req.Query == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "query is required", "invalid_request_error", "invalid_request")
			return
		}
		if len(req.Documents) == 0 {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "documents is required", "invalid_request_error", "invalid_request")
			return
		}

		resp, err := gw.Rerank(r.Context(), req)
		if err != nil {
			apierror.WriteRouteError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
