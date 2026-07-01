package core

import (
	"encoding/json"
	"fmt"
)

// apiErrorEnvelope is the OpenAI {"error":{"message":…}} error body shape.
type apiErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// APIError builds a provider error from a non-success HTTP response body, using
// the OpenAI {"error":{"message":…}} envelope when present and falling back to
// the raw body. label is the human-facing provider name (e.g. "groq"); status is
// embedded in parentheses so ParseStatusCode can recover it.
func APIError(label string, status int, body []byte) error {
	var e apiErrorEnvelope
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s API error (%d): %s", label, status, e.Error.Message)
	}
	return fmt.Errorf("%s API error (%d): %s", label, status, string(body))
}
