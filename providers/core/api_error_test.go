package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"delta-seconds", "120", 120 * time.Second},
		{"delta-seconds with surrounding space", "  30 ", 30 * time.Second},
		{"zero seconds yields no hint", "0", 0},
		{"negative seconds yields no hint", "-5", 0},
		{"absent header yields no hint", "", 0},
		{"unparseable value yields no hint", "soon-ish", 0},
		{"http-date in the past yields no hint", "Fri, 31 Dec 1999 23:59:59 GMT", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseRetryAfter(tt.value); got != tt.want {
				t.Errorf("ParseRetryAfter(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseRetryAfter_FutureHTTPDate(t *testing.T) {
	future := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	got := ParseRetryAfter(future)
	// The value is computed against wall-clock now, so assert a sane window
	// rather than an exact duration.
	if got <= 0 || got > 2*time.Minute {
		t.Errorf("ParseRetryAfter(future date) = %v, want a positive duration within 2m", got)
	}
}

func TestAPIErrorFromResponse_CapturesRetryAfter(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"7"}},
	}
	err := APIErrorFromResponse("groq", resp, []byte(`{"error":{"message":"slow down"}}`))

	if code := ParseStatusCode(err); code != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode = %d, want 429", code)
	}
	if got := RetryAfterFrom(err); got != 7*time.Second {
		t.Errorf("RetryAfterFrom = %v, want 7s", got)
	}
	if !strings.Contains(err.Error(), "slow down") {
		t.Errorf("message lost the upstream detail: %q", err.Error())
	}
}

func TestAPIErrorFromResponse_NoRetryAfterHeader(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{}}
	err := APIErrorFromResponse("groq", resp, []byte("boom"))

	if got := RetryAfterFrom(err); got != 0 {
		t.Errorf("RetryAfterFrom = %v, want 0 when the header is absent", got)
	}
}

func TestRetryAfterFrom_NonStatusErrorIsZero(t *testing.T) {
	if got := RetryAfterFrom(errors.New("transport failure")); got != 0 {
		t.Errorf("RetryAfterFrom = %v, want 0 for a non-status error", got)
	}
}

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
