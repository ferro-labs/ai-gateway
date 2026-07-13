package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// apiErrorEnvelope covers the OpenAI {"error":{"message":…}} error body shape and
// the FastAPI-style {"detail":"…"} envelope some providers (e.g. AI21) return for
// gateway-level errors.
type apiErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Detail string `json:"detail"`
}

// HTTPStatusError is a provider error carrying the upstream HTTP status code
// as a typed field, so callers can classify errors via errors.As instead of
// parsing the code back out of the formatted message.
type HTTPStatusError struct {
	StatusCode int
	Message    string // fully formatted message, e.g. "groq API error (429): rate limited"
	// RetryAfter carries the upstream Retry-After hint, or 0 when the response
	// did not supply a usable one. The fallback strategy honors it in preference
	// to its own computed backoff, so a 429/503 is retried when the provider says
	// it is ready rather than on a guess.
	RetryAfter time.Duration
}

// Error implements error.
func (e *HTTPStatusError) Error() string { return e.Message }

// ParseRetryAfter parses an HTTP Retry-After header value (RFC 9110 §10.2.3),
// which is either delta-seconds ("120") or an HTTP-date. It returns 0 when the
// value is absent, unparseable, non-positive, or already in the past — all of
// which mean "no usable hint", never a negative wait.
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// RetryAfterFrom returns the Retry-After hint carried by err, or 0 when err is
// not a provider status error or carried no usable hint.
func RetryAfterFrom(err error) time.Duration {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.RetryAfter
	}
	return 0
}

// APIErrorFromResponse builds a provider error from a non-success HTTP response,
// capturing the Retry-After hint alongside the status code. Prefer it over
// APIError wherever the *http.Response is in hand, so throttling responses can
// drive retry backoff instead of being guessed at.
func APIErrorFromResponse(label string, resp *http.Response, body []byte) error {
	err := APIError(label, resp.StatusCode, body)
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		statusErr.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return err
}

// APIError builds a provider error from a non-success HTTP response body. It
// extracts the message from the OpenAI {"error":{"message":…}} envelope, then the
// {"detail":"…"} envelope, and otherwise falls back to the raw body. label is the
// human-facing provider name (e.g. "groq"). The returned error is a
// *HTTPStatusError: status is both embedded in the message in parentheses (for
// display/logging) and available as a typed field via errors.As.
func APIError(label string, status int, body []byte) error {
	msg := string(body)
	var e apiErrorEnvelope
	if json.Unmarshal(body, &e) == nil {
		if e.Error.Message != "" {
			msg = e.Error.Message
		} else if e.Detail != "" {
			msg = e.Detail
		}
	}
	return &HTTPStatusError{
		StatusCode: status,
		Message:    fmt.Sprintf("%s API error (%d): %s", label, status, msg),
	}
}

// UnsupportedParamError is returned by the reject compatibility mode when a
// request sets parameters the target provider cannot express. It is a distinct
// type (not a generic upstream HTTPStatusError) so the HTTP layer can map it to
// a 400 invalid_request_error without affecting how upstream provider errors are
// classified. It names only parameter names and the provider — never prompt
// content or secrets — so it is safe to return to the caller.
type UnsupportedParamError struct {
	// Provider is the target provider that cannot express the parameters.
	Provider string
	// Params are the offending OpenAI parameter names, in stable order.
	Params []string
}

// Error implements error.
func (e *UnsupportedParamError) Error() string {
	return fmt.Sprintf(
		"provider %q does not support request parameter(s): %s",
		e.Provider, strings.Join(e.Params, ", "),
	)
}

// HTTPStatus reports the HTTP status this error maps to (400 Bad Request).
func (e *UnsupportedParamError) HTTPStatus() int { return http.StatusBadRequest }

// NewUnsupportedParamError builds the reject-mode error naming the request
// parameters the provider cannot express.
func NewUnsupportedParamError(provider string, params []string) error {
	return &UnsupportedParamError{Provider: provider, Params: params}
}
