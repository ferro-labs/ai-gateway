package strategies

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// defaultBackoffMs is used when RetryConfig.InitialBackoffMs is zero.
const defaultBackoffMs = 100

// NormalizeBackoffMs returns the effective initial backoff in milliseconds,
// substituting defaultBackoffMs when ms is unset (<= 0). It is exported so
// every retry surface shares one normalisation rule instead of re-deriving the
// guard. Without a single source, an unset InitialBackoffMs can silently mean
// "0ms backoff" on one surface and "100ms backoff" on another.
func NormalizeBackoffMs(ms int) int {
	if ms <= 0 {
		return defaultBackoffMs
	}
	return ms
}

// Fallback tries each target in declared order, moving to the next on failure.
//
// It holds no retry policy of its own. Retry is the gateway pipeline's, read per
// target from config, which is what makes `targets[].retry` mean the same thing
// under every mode instead of only under this one.
type Fallback struct {
	// keys is the SelectTargets answer, built once — see Single.keys.
	keys []string
}

// NewFallback creates a new fallback strategy.
func NewFallback(targets []Target) *Fallback {
	return &Fallback{keys: targetKeys(targets)}
}

// maxRetryAfter caps how long an upstream Retry-After hint may stall a retry. A
// longer hint means the provider will not be ready within a useful window, so the
// fallback abandons that target and moves on rather than holding the request open.
const maxRetryAfter = 30 * time.Second

// defaultRetryableStatus reports whether an HTTP status is retryable under the
// default policy (no explicit on_status_codes): request timeout, throttling, and
// server-side failures. Every other 4xx is a deterministic client error —
// retrying it against the same provider cannot change the outcome and only burns
// the retry budget.
func defaultRetryableStatus(code int) bool {
	return code == http.StatusRequestTimeout ||
		code == http.StatusTooManyRequests ||
		code >= http.StatusInternalServerError
}

// shouldRetry returns true if the error is eligible for another attempt against
// the same target. Cancellation, deadline expiry, open-circuit, and saturation
// sentinels are never retryable. Saturation means the target shed the request
// because its queue is already full, so backing off and offering it the same
// request only delays the move to a target that can take the work now. With no
// configured onStatusCodes the default policy applies (transport errors plus
// 408/429/5xx); when onStatusCodes is set, only those codes are retried. A
// transport error carries no status code and is always retryable — it is
// exactly the transient case retries exist for.
func shouldRetry(err error, onStatusCodes []int) bool {
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, circuitbreaker.ErrCircuitOpen) ||
		errors.Is(err, core.ErrProviderSaturated) {
		return false
	}
	code := providers.ParseStatusCode(err)
	if code == 0 {
		// No parseable status code — a transport-level failure; retry it.
		return true
	}
	if len(onStatusCodes) == 0 {
		return defaultRetryableStatus(code)
	}
	return slices.Contains(onStatusCodes, code)
}

// ShouldRetry reports whether err is eligible for another configured attempt
// against the same target. It exposes the fallback strategy's retry
// classification to the streaming start-phase retry helper in the root
// package so every routing surface follows one retry policy instead of
// growing a second, possibly-diverging one.
func ShouldRetry(err error, onStatusCodes []int) bool {
	return shouldRetry(err, onStatusCodes)
}

// retryDelay returns how long to wait before a retry attempt (attempt >= 1). It
// honors an upstream Retry-After hint from the previous failure when present —
// the provider knows when it will be ready better than any local guess.
// Otherwise it applies exponential backoff with FULL JITTER: a uniform random
// wait in [0, exponential). Full jitter spreads a thundering herd of retrying
// clients far better than a fixed exponential, which re-synchronises them.
//
// It returns a negative duration to signal that the Retry-After hint exceeds
// maxRetryAfter and the caller should stop retrying this target.
func retryDelay(attempt, initialBackoffMs int, prevErr error) time.Duration {
	if hint := providers.RetryAfterFrom(prevErr); hint > 0 {
		if hint > maxRetryAfter {
			return -1
		}
		return hint
	}
	exponential := time.Duration(math.Pow(2, float64(attempt-1))) *
		time.Duration(initialBackoffMs) * time.Millisecond
	if exponential <= 0 {
		return 0
	}
	//nolint:gosec // G404: retry jitter only needs to de-correlate concurrent
	// clients, not resist prediction. A CSPRNG would buy nothing here and cost
	// entropy on every retry.
	return rand.N(exponential)
}

// WaitBeforeRetry blocks for the configured retry delay ahead of attempt
// (attempt >= 1), normalising initialBackoffMs through NormalizeBackoffMs so
// callers never have to re-derive the <=0 default themselves. The returned
// bool is false when an upstream Retry-After hint exceeds maxRetryAfter and
// the caller should abandon this target rather than wait; ctx cancellation is
// returned as an error.
func WaitBeforeRetry(ctx context.Context, attempt, initialBackoffMs int, prevErr error) (bool, error) {
	delay := retryDelay(attempt, NormalizeBackoffMs(initialBackoffMs), prevErr)
	if delay < 0 {
		return false, nil
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(delay):
		return true, nil
	}
}

// SelectTargets returns every target key in declared order. The returned slice
// is shared and must not be modified.
func (f *Fallback) SelectTargets(_ providers.Request) ([]string, error) {
	return f.keys, nil
}
