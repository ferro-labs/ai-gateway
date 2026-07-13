package strategies

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers"
)

// targetRetry holds the resolved retry policy for a single target.
type targetRetry struct {
	attempts         int
	onStatusCodes    []int
	initialBackoffMs int
}

// defaultBackoffMs is used when RetryConfig.InitialBackoffMs is zero.
const defaultBackoffMs = 100

// Fallback tries each target in order, moving to the next on failure.
// Per-target retry policies (attempts, status code filtering, backoff) are
// configured via WithTargetRetry.
type Fallback struct {
	targets []Target
	lookup  ProviderLookup
	retries map[string]targetRetry // keyed by VirtualKey
}

// NewFallback creates a new fallback strategy with default retry settings
// (1 attempt per target, retry on any error).
func NewFallback(targets []Target, lookup ProviderLookup) *Fallback {
	return &Fallback{
		targets: targets,
		lookup:  lookup,
		retries: make(map[string]targetRetry),
	}
}

// WithMaxRetries sets a uniform attempt count for all targets.
// Kept for backwards-compatibility; WithTargetRetry is preferred for new code.
func (f *Fallback) WithMaxRetries(n int) *Fallback {
	for _, t := range f.targets {
		r := f.retries[t.VirtualKey]
		r.attempts = n
		f.retries[t.VirtualKey] = r
	}
	return f
}

// WithTargetRetry configures the retry policy for a specific target.
// attempts is the total attempt count (1 = no retries).
// onStatusCodes limits retries to requests that fail with those HTTP status
// codes; pass nil or empty to retry on any error.
// initialBackoffMs is the base for exponential backoff (0 → defaultBackoffMs).
func (f *Fallback) WithTargetRetry(virtualKey string, attempts int, onStatusCodes []int, initialBackoffMs int) *Fallback {
	f.retries[virtualKey] = targetRetry{
		attempts:         attempts,
		onStatusCodes:    onStatusCodes,
		initialBackoffMs: initialBackoffMs,
	}
	return f
}

// resolveRetry returns the effective retry config for a target, applying defaults.
func (f *Fallback) resolveRetry(virtualKey string) targetRetry {
	r, ok := f.retries[virtualKey]
	if !ok || r.attempts <= 0 {
		r.attempts = 1
	}
	if r.initialBackoffMs <= 0 {
		r.initialBackoffMs = defaultBackoffMs
	}
	return r
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
// the same target. Cancellation, deadline expiry, and open-circuit sentinels are
// never retryable. With no configured onStatusCodes the default policy applies
// (transport errors plus 408/429/5xx); when onStatusCodes is set, only those
// codes are retried. A transport error carries no status code and is always
// retryable — it is exactly the transient case retries exist for.
func shouldRetry(err error, onStatusCodes []int) bool {
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, circuitbreaker.ErrCircuitOpen) {
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

// Execute attempts each provider in order, retrying according to the per-target
// policy. Exponential backoff is applied between retries of the same target.
func (f *Fallback) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if len(f.targets) == 0 {
		return nil, fmt.Errorf("no targets configured for fallback")
	}

	var lastErr error
	attemptedCompatibleProvider := false
	for _, target := range f.targets {
		p, ok := f.lookup(target.VirtualKey)
		if !ok {
			logging.Logger.Warn("provider not found, skipping", "provider", target.VirtualKey)
			lastErr = fmt.Errorf("provider not found: %s", target.VirtualKey)
			continue
		}
		if !p.SupportsModel(req.Model) {
			continue
		}

		attemptedCompatibleProvider = true

		retry := f.resolveRetry(target.VirtualKey)

		// attemptErr is the previous attempt's failure on THIS target; it carries
		// any upstream Retry-After hint that should drive the next wait.
		var attemptErr error
		for attempt := 0; attempt < retry.attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if attempt > 0 {
				delay := retryDelay(attempt, retry.initialBackoffMs, attemptErr)
				if delay < 0 {
					logging.Logger.Info("abandoning provider: Retry-After exceeds the cap",
						"provider", target.VirtualKey,
						"retry_after", providers.RetryAfterFrom(attemptErr),
						"max_retry_after", maxRetryAfter,
					)
					break
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
				logging.Logger.Info("retrying provider",
					"provider", target.VirtualKey,
					"attempt", attempt+1,
					"delay", delay,
				)
			}

			resp, err := p.Complete(ctx, req)
			if err == nil {
				return responseWithProvider(resp, target.VirtualKey), nil
			}
			attemptErr = err
			lastErr = fmt.Errorf("provider %s attempt %d: %w", target.VirtualKey, attempt+1, err)

			// Stop retrying this target when the failure is not retryable.
			if !shouldRetry(err, retry.onStatusCodes) {
				logging.Logger.Debug("skipping retries for provider: error not retryable",
					"provider", target.VirtualKey,
					"status_code", providers.ParseStatusCode(err),
				)
				break
			}
		}
	}

	if !attemptedCompatibleProvider && lastErr == nil {
		return nil, fmt.Errorf("no provider supports model %s", req.Model)
	}

	if lastErr == nil {
		return nil, fmt.Errorf("all providers failed")
	}

	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

// SelectTargets returns every target key in declared order, matching the order
// Execute attempts providers.
func (f *Fallback) SelectTargets(_ providers.Request) ([]string, error) {
	return targetKeys(f.targets), nil
}
