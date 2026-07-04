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

// APIError builds a provider error from a non-success HTTP response body. It
// extracts the message from the OpenAI {"error":{"message":…}} envelope, then the
// {"detail":"…"} envelope, and otherwise falls back to the raw body. label is the
// human-facing provider name (e.g. "groq"); status is embedded in parentheses so
// ParseStatusCode can recover it.
func APIError(label string, status int, body []byte) error {
	var e apiErrorEnvelope
	if json.Unmarshal(body, &e) == nil {
		if e.Error.Message != "" {
			return fmt.Errorf("%s API error (%d): %s", label, status, e.Error.Message)
		}
		if e.Detail != "" {
			return fmt.Errorf("%s API error (%d): %s", label, status, e.Detail)
		}
	}
	return fmt.Errorf("%s API error (%d): %s", label, status, string(body))
}
