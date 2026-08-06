package core

import "errors"

// ErrNoCapableProvider signals that no registered provider supports the
// requested model for a given capability (embeddings, image generation, etc.).
// Handlers wrap this with errors.Is to distinguish "model not found / capability
// unsupported" (HTTP 404, invalid_request_error) from upstream server faults.
var ErrNoCapableProvider = errors.New("no capable provider for model")

// ErrProviderSaturated signals that a target is already at its configured
// concurrency limit and its wait queue is full, so the request was shed rather
// than queued indefinitely.
//
// It is backpressure, not a provider fault: the upstream was never called, so it
// must never count toward opening the provider's circuit breaker. The HTTP layer
// surfaces it as 429 so callers back off instead of retrying immediately.
var ErrProviderSaturated = errors.New("provider concurrency queue is full")

// ParseStatusCode recovers the HTTP status code from a provider error via
// errors.As, unwrapping through any %w wrapping. It returns 0 only when the
// error carries no status at all — a transport failure, a cancellation, a
// credential that could not be built — which is a different claim from "the
// upstream answered and we could not tell how".
//
// It used to fall back to regexing a parenthesised 3-digit code out of the
// message when errors.As found nothing. That fallback is gone. It read as a
// safety net and behaved as a trapdoor: every SDK-backed call returns an error
// whose message has no parentheses, so the regex silently yielded 0, and 0
// means "transport error" to everything downstream — retry retried a
// deterministic 400, the circuit breaker counted a rate limit as a fault, and
// the HTTP layer answered 500 for a failure it could have classified. A missing
// status now has exactly one cause, and providers/status_conformance_test.go
// fails any provider on any surface that stops supplying one.
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
	return 0
}
