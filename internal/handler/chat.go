package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/sse"
	"github.com/ferro-labs/ai-gateway/internal/version"
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
		if req.RoutingMetadata, err = routingMetadata(r.Header); err != nil {
			writeRoutingMetadataError(w, err)
			return
		}

		// --- Streaming path ---
		//
		// Whether anything here serves this model is the router's question, not
		// this handler's. It used to be asked twice: once here against the
		// REGISTERED provider set, which is not the routable one — targets is an
		// allowlist — and again inside the pipeline against the targets. The two
		// answered differently for the same condition, so a model nothing served
		// was a 400 on chat and a 404 on embeddings. The pipeline's answer is the
		// only one now: see apierror.WriteModelNotFound.
		//
		// That also folds the separate "provider does not support streaming"
		// refusal into the same 404. A target that cannot stream is not a
		// candidate for a streaming request, which is exactly what a target that
		// cannot embed is for /v1/embeddings — and that surface has always
		// reported it as model_not_found. One capability miss, one answer.
		attribution := &aigateway.RoutingAttribution{}
		ctx := aigateway.WithRoutingAttribution(r.Context(), attribution)
		if req.Stream {
			// The producer runs under a context this handler can cancel WITH A
			// REASON. Without one, the only cancellation it ever saw was the
			// request context dying as the handler returned, which says nothing
			// about why — so the gateway's own idle bound firing on a stalled
			// upstream was recorded as the caller hanging up.
			streamCtx, cancelStream := context.WithCancelCause(ctx)
			defer cancelStream(nil)

			ch, err := gw.RouteStream(streamCtx, req)
			// The pipeline has finished walking targets by the time the
			// stream starts, so the headers are known before the first byte.
			attribution.SetHeaders(w.Header())
			if err != nil {
				apierror.WriteRouteError(w, err)
				return
			}
			sse.Write(streamCtx, w, ch, cancelStream)
			return
		}

		// --- Non-streaming path ---
		resp, err := gw.Route(ctx, req)
		attribution.SetHeaders(w.Header())
		if err != nil {
			apierror.WriteRouteError(w, err)
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
		Name    string `json:"name"`
		Status  string `json:"status"`
		Circuit string `json:"circuit"`
		Models  int    `json:"models"`
	}

	return func(w http.ResponseWriter, _ *http.Request) {
		circuits := make(map[string]string)
		for _, pr := range gw.Readiness().Providers {
			circuits[pr.Name] = pr.Circuit
		}
		var providerStatuses []providerHealth
		for _, name := range gw.ListProviders() {
			if _, ok := gw.GetProvider(name); !ok {
				continue
			}
			circuit := circuits[name]
			if circuit == "" {
				circuit = "closed"
			}
			providerStatuses = append(providerStatuses, providerHealth{
				Name:    name,
				Status:  "available",
				Circuit: circuit,
				Models:  len(gw.ModelsFor(name)),
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
		if status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    status,
			"version":   version.Version,
			"commit":    version.Commit,
			"built":     version.Date,
			"providers": providerStatuses,
		})
	}
}
