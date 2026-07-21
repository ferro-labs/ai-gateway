package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
