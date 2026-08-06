package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const codeRequestRejected = "request_rejected"

func TestWriteOpenAI_SetsContentType(t *testing.T) {
	w := httptest.NewRecorder()
	WriteOpenAI(w, http.StatusBadRequest, "bad request", errTypeInvalidRequest, "invalid_request")

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
}

func TestWriteOpenAI_SetsStatusCode(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"bad_request", http.StatusBadRequest},
		{"not_found", http.StatusNotFound},
		{"too_many_requests", http.StatusTooManyRequests},
		{"internal_server_error", http.StatusInternalServerError},
		{"bad_gateway", http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteOpenAI(w, tt.status, "msg", "type", "code")
			if w.Code != tt.status {
				t.Fatalf("expected status %d, got %d", tt.status, w.Code)
			}
		})
	}
}

func TestWriteOpenAI_JSONStructure(t *testing.T) {
	w := httptest.NewRecorder()
	WriteOpenAI(w, http.StatusBadRequest, "model is required", errTypeInvalidRequest, "invalid_request")

	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if body.Error.Message != "model is required" {
		t.Fatalf("expected message %q, got %q", "model is required", body.Error.Message)
	}
	if body.Error.Type != errTypeInvalidRequest {
		t.Fatalf("expected type %q, got %q", errTypeInvalidRequest, body.Error.Type)
	}
	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected code %q, got %q", "invalid_request", body.Error.Code)
	}
}

func TestRouteErrorDetails_BeforeRequest_Guardrail(t *testing.T) {
	err := &plugin.RejectionError{
		Stage:      plugin.StageBeforeRequest,
		PluginType: plugin.TypeGuardrail,
		Reason:     "blocked word",
	}
	status, errType, code := RouteErrorDetails(err)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
	if errType != errTypeInvalidRequest {
		t.Fatalf("expected invalid_request_error, got %q", errType)
	}
	if code != codeRequestRejected {
		t.Fatalf("expected request_rejected, got %q", code)
	}
}

func TestRouteErrorDetails_BeforeRequest_RateLimit(t *testing.T) {
	err := &plugin.RejectionError{
		Stage:      plugin.StageBeforeRequest,
		PluginType: plugin.TypeRateLimit,
		Reason:     "rate limit exceeded",
	}
	status, errType, code := RouteErrorDetails(err)
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", status)
	}
	if errType != "rate_limit_error" {
		t.Fatalf("expected rate_limit_error, got %q", errType)
	}
	if code != "rate_limit_exceeded" {
		t.Fatalf("expected rate_limit_exceeded, got %q", code)
	}
}

func TestRouteErrorDetails_AfterRequest(t *testing.T) {
	err := &plugin.RejectionError{
		Stage:  plugin.StageAfterRequest,
		Reason: "schema mismatch",
	}
	status, errType, code := RouteErrorDetails(err)
	if status != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", status)
	}
	if errType != "upstream_error" {
		t.Fatalf("expected upstream_error, got %q", errType)
	}
	if code != "response_rejected" {
		t.Fatalf("expected response_rejected, got %q", code)
	}
}

func TestRouteErrorDetails_UnknownStage(t *testing.T) {
	err := &plugin.RejectionError{
		Stage:  plugin.Stage("custom_stage"),
		Reason: "custom",
	}
	status, errType, code := RouteErrorDetails(err)
	if status != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", status)
	}
	if errType != errTypeServer {
		t.Fatalf("expected server_error, got %q", errType)
	}
	if code != codeRequestRejected {
		t.Fatalf("expected request_rejected, got %q", code)
	}
}

func TestRouteErrorDetails_NonRejectionError(t *testing.T) {
	err := errors.New("something broke")
	status, errType, code := RouteErrorDetails(err)
	if status != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", status)
	}
	if errType != errTypeServer {
		t.Fatalf("expected server_error, got %q", errType)
	}
	if code != "routing_error" {
		t.Fatalf("expected routing_error, got %q", code)
	}
}

func TestRouteErrorDetails_UnsupportedParam(t *testing.T) {
	err := core.NewUnsupportedParamError("gemini", []string{"logit_bias"})
	status, errType, code := RouteErrorDetails(err)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
	if errType != "invalid_request_error" {
		t.Fatalf("expected invalid_request_error, got %q", errType)
	}
	if code != "unsupported_parameter" {
		t.Fatalf("expected unsupported_parameter, got %q", code)
	}
}

func TestRouteErrorDetails_UnsupportedParamWrapped(t *testing.T) {
	// A wrapped reject error must still classify as a 400, not the 500 fallback.
	err := fmt.Errorf("routing: %w", core.NewUnsupportedParamError("gemini", []string{"seed"}))
	if status, _, _ := RouteErrorDetails(err); status != http.StatusBadRequest {
		t.Fatalf("wrapped unsupported-param error: expected 400, got %d", status)
	}
}

func TestRouteErrorDetails_NoCapableProvider(t *testing.T) {
	err := fmt.Errorf("%w: no embedding provider for %q", core.ErrNoCapableProvider, "unknown-model")
	status, errType, code := RouteErrorDetails(err)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
	if errType != errTypeInvalidRequest {
		t.Fatalf("expected invalid_request_error, got %q", errType)
	}
	if code != codeModelNotFound {
		t.Fatalf("expected %s, got %q", codeModelNotFound, code)
	}
}

func TestRouteErrorDetails_NoCapableProvider_DirectSentinel(t *testing.T) {
	status, errType, code := RouteErrorDetails(core.ErrNoCapableProvider)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
	if errType != errTypeInvalidRequest {
		t.Fatalf("expected invalid_request_error, got %q", errType)
	}
	if code != codeModelNotFound {
		t.Fatalf("expected %s, got %q", codeModelNotFound, code)
	}
}

func TestRouteErrorDetails_BrokenPluginIsAServerErrorNotARateLimit(t *testing.T) {
	// A rate-limit plugin whose backend is down has not rate-limited anybody.
	// Reporting 429 tells every OpenAI SDK to back off and retry — a retry
	// storm against a gateway that is broken, not busy. It is a server error.
	failure := &plugin.FailureError{
		Plugin:     "rate-limit",
		PluginType: plugin.TypeRateLimit,
		Stage:      plugin.StageBeforeRequest,
		Err:        errors.New("redis: connection refused"),
	}

	status, errType, code := RouteErrorDetails(failure)
	if status == http.StatusTooManyRequests {
		t.Fatal("a broken rate-limit plugin must not report 429: the client is not being rate limited, the gateway is broken")
	}
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if errType != "server_error" || code != "plugin_error" {
		t.Errorf("type/code = %q/%q, want server_error/plugin_error", errType, code)
	}
}

// upstreamErr builds the error shape a provider failure actually reaches the
// handler in: the strategy wraps the last attempt's error before returning it.
func upstreamErr(status int) error {
	body := []byte(`{"error":{"message":"upstream said no"}}`)
	return fmt.Errorf("all providers failed: %w", core.APIError("groq", status, body))
}

func TestRouteErrorDetails_UpstreamStatus(t *testing.T) {
	tests := []struct {
		name       string
		upstream   int
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{"throttled", http.StatusTooManyRequests, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded"},
		{"provider_key_rejected", http.StatusUnauthorized, http.StatusBadGateway, "upstream_error", "upstream_auth_error"},
		{"provider_key_forbidden", http.StatusForbidden, http.StatusBadGateway, "upstream_error", "upstream_auth_error"},
		{"malformed_request", http.StatusBadRequest, http.StatusBadRequest, errTypeInvalidRequest, "invalid_request"},
		{"unprocessable_request", http.StatusUnprocessableEntity, http.StatusUnprocessableEntity, errTypeInvalidRequest, "invalid_request"},
		{"unknown_model", http.StatusNotFound, http.StatusNotFound, errTypeInvalidRequest, codeModelNotFound},
		{"upstream_timed_out", http.StatusGatewayTimeout, http.StatusGatewayTimeout, "upstream_error", "upstream_timeout"},
		{"upstream_crashed", http.StatusInternalServerError, http.StatusBadGateway, "upstream_error", "upstream_error"},
		{"upstream_unavailable", http.StatusServiceUnavailable, http.StatusBadGateway, "upstream_error", "upstream_error"},
		{"upstream_conflict", http.StatusConflict, http.StatusBadGateway, "upstream_error", "upstream_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, errType, code := RouteErrorDetails(upstreamErr(tt.upstream))
			if status != tt.wantStatus {
				t.Errorf("upstream %d: status = %d, want %d", tt.upstream, status, tt.wantStatus)
			}
			if errType != tt.wantType || code != tt.wantCode {
				t.Errorf("upstream %d: type/code = %q/%q, want %q/%q", tt.upstream, errType, code, tt.wantType, tt.wantCode)
			}
		})
	}
}

func TestRouteErrorDetails_GatewayDecisionOutranksUpstreamStatus(t *testing.T) {
	// The upstream error is joined first in every case, so a classification that
	// read the provider status before the gateway's own decision would win here.
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{
			name:       "saturated_target",
			err:        errors.Join(upstreamErr(http.StatusServiceUnavailable), core.ErrProviderSaturated),
			wantStatus: http.StatusTooManyRequests,
			wantType:   "rate_limit_error",
			wantCode:   "provider_saturated",
		},
		{
			name: "guardrail_rejection",
			err: errors.Join(upstreamErr(http.StatusServiceUnavailable), &plugin.RejectionError{
				Plugin:     "word-filter",
				PluginType: plugin.TypeGuardrail,
				Stage:      plugin.StageBeforeRequest,
				Reason:     "blocked word",
			}),
			wantStatus: http.StatusBadRequest,
			wantType:   errTypeInvalidRequest,
			wantCode:   codeRequestRejected,
		},
		{
			name: "broken_plugin",
			err: errors.Join(upstreamErr(http.StatusTooManyRequests), &plugin.FailureError{
				Plugin:     "rate-limit",
				PluginType: plugin.TypeRateLimit,
				Stage:      plugin.StageBeforeRequest,
				Err:        errors.New("redis: connection refused"),
			}),
			wantStatus: http.StatusInternalServerError,
			wantType:   errTypeServer,
			wantCode:   "plugin_error",
		},
		{
			name:       "unsupported_parameter",
			err:        errors.Join(upstreamErr(http.StatusBadGateway), core.NewUnsupportedParamError("gemini", []string{"logit_bias"})),
			wantStatus: http.StatusBadRequest,
			wantType:   errTypeInvalidRequest,
			wantCode:   "unsupported_parameter",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, errType, code := RouteErrorDetails(tt.err)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if errType != tt.wantType || code != tt.wantCode {
				t.Errorf("type/code = %q/%q, want %q/%q", errType, code, tt.wantType, tt.wantCode)
			}
		})
	}
}

func TestRouteErrorDetails_ProviderSaturated(t *testing.T) {
	status, errType, code := RouteErrorDetails(fmt.Errorf("routing: %w", core.ErrProviderSaturated))
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", status)
	}
	if errType != "rate_limit_error" || code != "provider_saturated" {
		t.Errorf("type/code = %q/%q, want rate_limit_error/provider_saturated", errType, code)
	}
}

func TestRouteErrorDetails_DeliberateRateLimitRejectionStays429(t *testing.T) {
	rejection := &plugin.RejectionError{
		Plugin:     "rate-limit",
		PluginType: plugin.TypeRateLimit,
		Stage:      plugin.StageBeforeRequest,
		Reason:     "60 requests per minute exceeded",
	}

	status, errType, code := RouteErrorDetails(rejection)
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d: an actual rate-limit decision is still a 429", status, http.StatusTooManyRequests)
	}
	if errType != "rate_limit_error" || code != "rate_limit_exceeded" {
		t.Errorf("type/code = %q/%q, want rate_limit_error/rate_limit_exceeded", errType, code)
	}
}

// A throttled upstream tells the caller how long to wait, and that hint is only
// worth capturing if it survives to the response.
func TestWriteRouteError_CarriesRetryAfter(t *testing.T) {
	tests := map[string]struct {
		err       error
		wantCode  int
		wantRetry string
	}{
		"upstream retry hint reaches the client": {
			err:       fmt.Errorf("all providers failed: %w", &core.HTTPStatusError{StatusCode: 429, Message: "groq API error (429): slow down", RetryAfter: 30 * time.Second}),
			wantCode:  http.StatusTooManyRequests,
			wantRetry: "30",
		},
		"a sub-second hint is rounded up rather than truncated to zero": {
			err:       &core.HTTPStatusError{StatusCode: 429, Message: "throttled", RetryAfter: 1500 * time.Millisecond},
			wantCode:  http.StatusTooManyRequests,
			wantRetry: "2",
		},
		"no hint sets no header": {
			err:       &core.HTTPStatusError{StatusCode: 503, Message: "upstream down"},
			wantCode:  http.StatusBadGateway,
			wantRetry: "",
		},
		"a non-upstream error sets no header": {
			err:       errors.New("something else"),
			wantCode:  http.StatusInternalServerError,
			wantRetry: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteRouteError(w, tt.err)

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}
			if got := w.Header().Get("Retry-After"); got != tt.wantRetry {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantRetry)
			}
		})
	}
}
