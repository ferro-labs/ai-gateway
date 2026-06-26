package core

import (
	"errors"
	"regexp"
	"strconv"
)

// ErrNoCapableProvider signals that no registered provider supports the
// requested model for a given capability (embeddings, image generation, etc.).
// Handlers wrap this with errors.Is to distinguish "model not found / capability
// unsupported" (HTTP 404, invalid_request_error) from upstream server faults.
var ErrNoCapableProvider = errors.New("no capable provider for model")

// statusCodePattern matches HTTP status codes formatted as "(NNN)" inside
// provider error messages (e.g. "provider API error (429): ...").
var statusCodePattern = regexp.MustCompile(`\((\d{3})\)`)

// ParseStatusCode extracts the HTTP status code embedded in a provider error
// message. Returns 0 if no 3-digit parenthesised code can be found.
// All built-in providers embed a 3-digit HTTP status code in parentheses inside
// their error messages (e.g. "... API error (NNN): message").
func ParseStatusCode(err error) int {
	if err == nil {
		return 0
	}
	m := statusCodePattern.FindStringSubmatch(err.Error())
	if len(m) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(m[1])
	return code
}
