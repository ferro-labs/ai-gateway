package strategies

import (
	"context"
	"fmt"
	"math"
	"time"

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

// shouldRetry returns true if the error is eligible for another attempt given
// the configured onStatusCodes list. When onStatusCodes is empty every error
// is retryable. When it is non-empty, only errors whose embedded HTTP status
// code appears in the list are retried.
func shouldRetry(err error, onStatusCodes []int) bool {
	if len(onStatusCodes) == 0 {
		return true
	}
	code := providers.ParseStatusCode(err)
	if code == 0 {
		// No parseable status code — treat as retryable so we don't silently
		// swallow errors that don't follow the standard format.
		return true
	}
	for _, c := range onStatusCodes {
		if c == code {
			return true
		}
	}
	return false
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

		for attempt := 0; attempt < retry.attempts; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(math.Pow(2, float64(attempt-1))) *
					time.Duration(retry.initialBackoffMs) * time.Millisecond
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff):
				}
				logging.Logger.Info("retrying provider", "provider", target.VirtualKey, "attempt", attempt+1)
			}

			resp, err := p.Complete(ctx, req)
			if err == nil {
				return resp, nil
			}
			lastErr = fmt.Errorf("provider %s attempt %d: %w", target.VirtualKey, attempt+1, err)

			// Stop retrying this target if the status code is not in the allow list.
			if !shouldRetry(err, retry.onStatusCodes) {
				logging.Logger.Debug("skipping retries for provider: status code not in retry list",
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
