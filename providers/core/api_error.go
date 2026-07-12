package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
