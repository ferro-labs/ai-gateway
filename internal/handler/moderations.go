package handler

import (
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Moderations handles POST /v1/moderations.
// It routes moderation requests to the first registered ModerationProvider that
// serves the requested model.
func Moderations(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req providers.ModerationRequest
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

		attribution := &aigateway.RoutingAttribution{}
		ctx := aigateway.WithRoutingAttribution(r.Context(), attribution)
		resp, err := gw.Moderate(ctx, req)
		attribution.SetHeaders(w.Header())
		if err != nil {
			apierror.WriteRouteError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
