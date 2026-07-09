package core

import (
	"encoding/json"
	"fmt"
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
}

func (e *HTTPStatusError) Error() string { return e.Message }

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
