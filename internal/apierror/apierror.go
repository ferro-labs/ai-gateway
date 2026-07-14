// Package apierror provides OpenAI-compatible JSON error response helpers.
package apierror

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const codeModelNotFound = "model_not_found"

// WriteOpenAI writes a unified OpenAI-compatible JSON error response.
func WriteOpenAI(w http.ResponseWriter, status int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
}

// RouteErrorDetails maps a routing or plugin error to an HTTP status and OpenAI error type/code.
func RouteErrorDetails(err error) (status int, errType, code string) {
	status = http.StatusInternalServerError
	errType = "server_error"
	code = "routing_error"

	// A fail-closed plugin that broke did not deny anything — it never got far
	// enough to have an opinion. That is a fault on our side of the wire, so it
	// is reported as one. Dressing it up as a rejection would tell the client to
	// fix a request that was fine, or — for a rate-limit plugin whose backend is
	// down — hand back a 429 that invites every SDK to retry into the outage.
	var failure *plugin.FailureError
	if errors.As(err, &failure) {
		return http.StatusInternalServerError, "server_error", "plugin_error"
	}

	var rejection *plugin.RejectionError
	if errors.As(err, &rejection) {
		switch rejection.Stage {
		case plugin.StageBeforeRequest:
			if rejection.PluginType == plugin.TypeRateLimit {
				return http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded"
			}
			return http.StatusBadRequest, "invalid_request_error", "request_rejected"
		case plugin.StageAfterRequest:
			return http.StatusBadGateway, "upstream_error", "response_rejected"
		default:
			return http.StatusInternalServerError, "server_error", "request_rejected"
		}
	}

	if errors.Is(err, core.ErrNoCapableProvider) {
		return http.StatusNotFound, "invalid_request_error", codeModelNotFound
	}

	// The target is at its concurrency limit and its queue is full. This is
	// backpressure, not a failure: 429 tells the caller to back off and retry,
	// which is exactly the desired behaviour under saturation.
	if errors.Is(err, core.ErrProviderSaturated) {
		return http.StatusTooManyRequests, "rate_limit_error", "provider_saturated"
	}

	var unsupportedParam *core.UnsupportedParamError
	if errors.As(err, &unsupportedParam) {
		return http.StatusBadRequest, "invalid_request_error", "unsupported_parameter"
	}

	return status, errType, code
}
