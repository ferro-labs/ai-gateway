// Package httpgateway exposes HTTP handlers for applications that embed an
// aigateway.Gateway behind their own authentication and policy middleware.
package httpgateway

import (
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	internalproxy "github.com/ferro-labs/ai-gateway/internal/proxy"
)

// Passthrough exposes the gateway-owned transparent /v1/* fallback. Mount it
// only after every native/static route the embedding application owns.
func Passthrough(gw *aigateway.Gateway) http.HandlerFunc {
	return internalproxy.Handler(gw)
}

// Batch exposes the fixed-target Files and Batches surface. BatchTarget selects
// the one configured backend.
func Batch(gw *aigateway.Gateway) http.HandlerFunc {
	return internalproxy.BatchHandler(gw)
}

// ResponsesCreate handles model-routed POST /v1/responses, pricing the surface
// from the response usage as it streams through.
func ResponsesCreate(gw *aigateway.Gateway) http.HandlerFunc {
	return internalproxy.ResponsesCreate(gw)
}

// ResponsesStateful exposes the fixed-target Responses id sub-routes.
// ResponsesTarget selects the one configured backend.
func ResponsesStateful(gw *aigateway.Gateway) http.HandlerFunc {
	return internalproxy.ResponsesIDs(gw)
}
