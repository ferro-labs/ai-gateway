package core

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
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
		{"delta-seconds just above the max representable duration yields no hint", "9223372037", 0},
		{"delta-seconds at math.MaxInt64 yields no hint", strconv.FormatInt(math.MaxInt64, 10), 0},
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
		{"flat message", `{"message":"bad key"}`, "bad key"},
		{"openai wins over detail", `{"error":{"message":"m"},"detail":"d"}`, "m"},
		// A body in no recognised shape contributes nothing: the status phrase
		// stands in. See TestAPIError_UnrecognisedBodyNeverSurfaces.
		{"unrecognised body", `weird body`, "Unauthorized"},
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

// TestUpstreamMessage_DetailAsArray verifies the FastAPI-style "detail" field is
// read in BOTH of its shapes. Mistral sends a plain string for a gateway-level
// error and an array of validation objects for a malformed request; decoding it
// into a string made the array shape fail the whole unmarshal, so a 422 that
// named the offending field was discarded and reported as bare "Unprocessable
// Entity" — and that status is the one whose upstream text reaches the caller.
func TestUpstreamMessage_DetailAsArray(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "detail as string",
			body: `{"detail":"Not enough credits"}`,
			want: "Not enough credits",
		},
		{
			name: "detail as validation array",
			body: `{"detail":[{"type":"missing","loc":["body","model"],"msg":"Field required","input":null}]}`,
			want: "Field required",
		},
		{
			name: "detail array with several messages is joined",
			body: `{"detail":[{"msg":"Field required"},{"msg":"Input should be a valid string"}]}`,
			want: "Field required; Input should be a valid string",
		},
		{
			name: "error.message still wins over detail",
			body: `{"error":{"message":"upstream said no"},"detail":[{"msg":"Field required"}]}`,
			want: "upstream said no",
		},
		{
			name: "detail array of unrecognised objects contributes nothing",
			body: `{"detail":[{"internal_host":"10.0.0.4"}]}`,
			want: "",
		},
		{
			name: "detail of an unrecognised type contributes nothing",
			body: `{"detail":{"nested":"object"}}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upstreamMessage([]byte(tt.body)); got != tt.want {
				t.Errorf("upstreamMessage(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestUpstreamMessage_ErrorAndDetailShapes verifies the upstream's own words are
// recovered whether a provider sends "error" as a plain string or an object, and
// whether it sends "detail" as a string or an object. Each shape below is a real
// provider body; before the fix the string-form "error" and the object-form
// "detail" failed the unmarshal and the caller got a bare status phrase.
func TestUpstreamMessage_ErrorAndDetailShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "error as a plain string (xAI, Hugging Face)",
			body: `{"error":"Incorrect API key provided."}`,
			want: "Incorrect API key provided.",
		},
		{
			name: "error as an object (OpenAI/Anthropic/Gemini)",
			body: `{"error":{"message":"bad request"}}`,
			want: "bad request",
		},
		{
			name: "object-shaped detail carrying error (DeepInfra 404)",
			body: "{\"detail\":{\"error\":\"The model `bad/model` does not exist\"}}",
			want: "The model `bad/model` does not exist",
		},
		{
			name: "string detail (DeepInfra 401)",
			body: `{"detail":"Unauthorized"}`,
			want: "Unauthorized",
		},
		{
			name: "object-shaped detail carrying message",
			body: `{"detail":{"message":"quota exceeded"}}`,
			want: "quota exceeded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upstreamMessage([]byte(tt.body))
			if got == "" {
				t.Fatalf("upstreamMessage(%s) = %q, want a non-empty message", tt.body, got)
			}
			if got != tt.want {
				t.Errorf("upstreamMessage(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestUpstreamMessage_DetailArrayNeverEchoesInput pins that only "msg" is taken
// from a validation element. The same element carries "input", which echoes the
// request value that failed validation, and this text reaches the caller on a
// 400/422.
func TestUpstreamMessage_DetailArrayNeverEchoesInput(t *testing.T) {
	const body = `{"detail":[{"msg":"Field required","input":"sk-live-secret","loc":["body","api_key"]}]}`
	got := upstreamMessage([]byte(body))
	if strings.Contains(got, "sk-live-secret") {
		t.Fatalf("upstreamMessage = %q, want the element's input value withheld", got)
	}
	if got != "Field required" {
		t.Errorf("upstreamMessage = %q, want %q", got, "Field required")
	}
}

// TestWithRetryAfter_RateLimitResetFallback verifies X-RateLimit-Reset supplies
// the hint when Retry-After is absent, and that Retry-After still wins when both
// are present. Providers that throttle with only the rate-limit header set had
// their stated wait dropped, so the retry ran on the fallback strategy's guess.
func TestWithRetryAfter_RateLimitResetFallback(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		reset      string
		want       time.Duration
	}{
		{name: "no headers", want: 0},
		{name: "retry-after only", retryAfter: "5", want: 5 * time.Second},
		{name: "rate-limit reset only", reset: "12", want: 12 * time.Second},
		{name: "retry-after wins over reset", retryAfter: "5", reset: "12", want: 5 * time.Second},
		{name: "unusable retry-after falls back to reset", retryAfter: "soon-ish", reset: "9", want: 9 * time.Second},
		{name: "zero reset yields no hint", reset: "0", want: 0},
		{name: "negative reset yields no hint", reset: "-3", want: 0},
		{name: "reset at the cap is honoured", reset: strconv.Itoa(int(maxRateLimitReset.Seconds())), want: maxRateLimitReset},
		// A Unix timestamp published under the same header name. Honouring it
		// would put Retry-After decades out.
		{name: "epoch-timestamp reset is discarded", reset: "1753900000", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.retryAfter != "" {
				h.Set("Retry-After", tt.retryAfter)
			}
			if tt.reset != "" {
				h.Set("X-RateLimit-Reset", tt.reset)
			}
			err := StatusError("mistral", http.StatusTooManyRequests, "rate limited").WithRetryAfter(h)
			if got := RetryAfterFrom(err); got != tt.want {
				t.Errorf("RetryAfter = %v, want %v", got, tt.want)
			}
		})
	}
}
