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

// ParseStatusCode recovers the HTTP status code from a provider error. It
// first tries errors.As for a typed *HTTPStatusError (as returned by
// APIError), unwrapping through any %w wrapping; if the error was never
// constructed via APIError, it falls back to regexing a 3-digit parenthesised
// code out of the message (e.g. "... API error (NNN): message"). Returns 0 if
// neither recovers a code.
func ParseStatusCode(err error) int {
	if err == nil {
		return 0
	}
	var unsupportedErr *UnsupportedParamError
	if errors.As(err, &unsupportedErr) {
		return unsupportedErr.HTTPStatus()
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode
	}
	m := statusCodePattern.FindStringSubmatch(err.Error())
	if len(m) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(m[1])
	return code
}
