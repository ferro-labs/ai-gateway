package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestAPIError_Envelopes verifies APIError extracts the message from the OpenAI
// envelope and the FastAPI {"detail":…} envelope, and falls back to the raw body.
func TestAPIError_Envelopes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"openai", `{"error":{"message":"bad key"}}`, "bad key"},
		{"detail", `{"detail":"Invalid API Key"}`, "Invalid API Key"},
		{"raw fallback", `weird body`, "weird body"},
		{"openai wins over detail", `{"error":{"message":"m"},"detail":"d"}`, "m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := APIError("test", 401, []byte(tc.body))
			if err == nil {
				t.Fatal("APIError returned nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "(401)") {
				t.Errorf("error = %q, want it to embed the status", err.Error())
			}
		})
	}
}

// TestAPIError_TypedStatusRecoverable verifies APIError returns an error that
// errors.As can recover a typed *HTTPStatusError from, so callers no longer
// need to regex-parse the status out of the formatted message.
func TestAPIError_TypedStatusRecoverable(t *testing.T) {
	err := APIError("groq", 429, []byte(`{"error":{"message":"rate limited"}}`))

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("errors.As failed to recover *HTTPStatusError from %v", err)
	}
	if statusErr.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", statusErr.StatusCode)
	}
	if statusErr.Error() != err.Error() {
		t.Errorf("HTTPStatusError.Error() = %q, want it to match the top-level error message %q", statusErr.Error(), err.Error())
	}
}

// TestAPIError_TypedStatusSurvivesWrapping verifies errors.As still recovers
// the typed status after the error is wrapped with %w, matching how
// internal/strategies/fallback.go wraps provider errors with retry-attempt
// context.
func TestAPIError_TypedStatusSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("provider groq attempt 1: %w", APIError("groq", 503, []byte("down")))

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("errors.As failed to recover *HTTPStatusError from wrapped error %v", err)
	}
	if statusErr.StatusCode != 503 {
		t.Errorf("StatusCode = %d, want 503", statusErr.StatusCode)
	}
}
