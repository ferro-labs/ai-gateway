// Package httpgateway exposes OSS-owned HTTP surfaces to applications that
// embed an aigateway.Gateway behind their own authentication and policy layer.
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

// ResponsesCreate exposes governed, model-routed POST /v1/responses. The
// response usage tee keeps this surface priced by the OSS lifecycle.
func ResponsesCreate(gw *aigateway.Gateway) http.HandlerFunc {
	return internalproxy.ResponsesCreate(gw)
}

// ResponsesStateful exposes the fixed-target Responses id sub-routes.
// ResponsesTarget selects the one configured backend.
func ResponsesStateful(gw *aigateway.Gateway) http.HandlerFunc {
	return internalproxy.ResponsesIDs(gw)
}
