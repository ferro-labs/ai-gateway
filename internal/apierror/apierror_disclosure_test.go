package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// decodeError reads the OpenAI error envelope back out of a recorded response.
func decodeError(t *testing.T, w *httptest.ResponseRecorder) (message, errType, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body.Error.Message, body.Error.Type, body.Error.Code
}

// The gateway's target list is operator configuration. A caller that names a
// model no target can serve learns that the model is unavailable — not which
// providers were consulted, which of them were missing, or what they are called.
//
// The names below are the ones from the reported failure, so this test fails on
// the exact string that was being handed out.
func TestWriteRouteError_DoesNotDiscloseConfiguredTargets(t *testing.T) {
	tests := map[string]error{
		"fallback exhausted its target list": fmt.Errorf(
			"all providers failed: %w", fmt.Errorf("provider not found: anthropic: %w", core.ErrNoCapableProvider)),
		"the only target names nothing registered": fmt.Errorf(
			"provider not found: not-a-real-provider: %w", core.ErrNoCapableProvider),
		"a broken plugin names its own backend": &plugin.FailureError{
			Plugin:     "rate-limit",
			PluginType: plugin.TypeRateLimit,
			Stage:      plugin.StageBeforeRequest,
			Err:        errors.New("dial tcp redis.internal:6379: connection refused"),
		},
		"an unclassified routing failure": errors.New("all providers failed: provider openai attempt 1: boom"),
		"a saturated target":              fmt.Errorf("routing: %w", core.ErrProviderSaturated),
		"an upstream outage": fmt.Errorf("all providers failed: provider groq attempt 2: %w",
			core.APIError("groq", http.StatusServiceUnavailable, []byte(`{"error":{"message":"capacity"}}`))),
	}

	// Every name that only exists because an operator wrote it in a config file.
	leaks := []string{"anthropic", "not-a-real-provider", "openai", "groq", "redis.internal", "rate-limit"}

	for name, err := range tests {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteRouteError(w, err)

			message, _, _ := decodeError(t, w)
			for _, leak := range leaks {
				if strings.Contains(strings.ToLower(message), leak) {
					t.Errorf("response message %q discloses %q", message, leak)
				}
			}
			if message == "" {
				t.Error("response message is empty: the caller is owed an explanation, just not this one")
			}
		})
	}
}

// An unroutable model is the caller's to fix and no amount of retrying changes
// it. 404 model_not_found is what the OpenAI contract calls this and what every
// SDK's retry policy excludes; 500 is what invites the retry storm.
func TestWriteRouteError_UnroutableModelIsNotFound(t *testing.T) {
	err := fmt.Errorf("all providers failed: %w",
		fmt.Errorf("provider not found: anthropic: %w", core.ErrNoCapableProvider))

	w := httptest.NewRecorder()
	WriteRouteError(w, err)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	_, errType, code := decodeError(t, w)
	if errType != errTypeInvalidRequest || code != codeModelNotFound {
		t.Errorf("type/code = %q/%q, want %q/%q", errType, code, errTypeInvalidRequest, codeModelNotFound)
	}
}

// The other half of the same distinction: a target that was called and failed is
// a retryable upstream fault, and must keep answering as one.
func TestWriteRouteError_UpstreamFailureStaysRetryable(t *testing.T) {
	err := fmt.Errorf("all providers failed: provider groq attempt 2: %w",
		core.APIError("groq", http.StatusServiceUnavailable, []byte(`{"error":{"message":"capacity"}}`)))

	w := httptest.NewRecorder()
	WriteRouteError(w, err)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d — an upstream outage is not an unroutable model", w.Code, http.StatusBadGateway)
	}
	_, errType, code := decodeError(t, w)
	if errType != errTypeUpstream || code != "upstream_error" {
		t.Errorf("type/code = %q/%q, want upstream_error/upstream_error", errType, code)
	}
}

// A provider that read the caller's request and rejected its shape is describing
// the caller's own request. That account survives, because taking it away leaves
// nothing to act on.
func TestWriteRouteError_UpstreamRequestRejectionKeepsItsDetail(t *testing.T) {
	err := fmt.Errorf("all providers failed: provider openai attempt 1: %w",
		core.APIError("openai", http.StatusBadRequest,
			[]byte(`{"error":{"message":"max_tokens is too large: 100000"}}`)))

	w := httptest.NewRecorder()
	WriteRouteError(w, err)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	message, _, _ := decodeError(t, w)
	if !strings.Contains(message, "max_tokens is too large") {
		t.Errorf("message = %q, want the provider's account of the request", message)
	}
	if strings.Contains(message, "all providers failed") || strings.Contains(message, "attempt 1") {
		t.Errorf("message = %q, want the provider's own text without the gateway's routing narrative", message)
	}
}

// A plugin that DENIES a request is telling the caller why. That text is the
// point of the rejection and is written to be seen.
func TestWriteRouteError_PluginRejectionKeepsItsReason(t *testing.T) {
	err := &plugin.RejectionError{
		Plugin:     "word-filter",
		PluginType: plugin.TypeGuardrail,
		Stage:      plugin.StageBeforeRequest,
		Reason:     "prompt contains a blocked word",
	}

	w := httptest.NewRecorder()
	WriteRouteError(w, err)

	message, _, _ := decodeError(t, w)
	if !strings.Contains(message, "blocked word") {
		t.Errorf("message = %q, want the plugin's stated reason", message)
	}
}

// 429 without Retry-After is the shape that produces the storm it is meant to
// prevent: the OpenAI Go client retries 429 by default and, absent a hint, falls
// back to its own short fixed schedule.
func TestWriteRouteError_GatewaySide429CarriesRetryAfter(t *testing.T) {
	tests := map[string]error{
		"saturated target": fmt.Errorf("routing: %w", core.ErrProviderSaturated),
		"rate-limit plugin denial": &plugin.RejectionError{
			Plugin:     "rate-limit",
			PluginType: plugin.TypeRateLimit,
			Stage:      plugin.StageBeforeRequest,
			Reason:     "60 requests per minute exceeded",
		},
	}

	for name, err := range tests {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteRouteError(w, err)

			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
			}
			if got := w.Header().Get("Retry-After"); got != retryAfterSeconds {
				t.Errorf("Retry-After = %q, want %q", got, retryAfterSeconds)
			}
		})
	}
}

// An upstream that stated its own wait keeps it: the provider knows when it will
// be ready and the gateway's constant must not overwrite that.
func TestWriteRouteError_UpstreamRetryAfterOutranksTheConstant(t *testing.T) {
	w := httptest.NewRecorder()
	WriteRouteError(w, fmt.Errorf("all providers failed: %w", &core.HTTPStatusError{
		StatusCode: http.StatusTooManyRequests,
		Message:    "groq API error (429): slow down",
		RetryAfter: 30_000_000_000, // 30s
	}))

	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want %q", got, "30")
	}
}
