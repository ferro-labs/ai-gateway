package aigateway

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/internal/strategies"
)

// runTargetAttempts applies the configured fallback retry policy to a generic
// provider-start call. Other strategy modes perform exactly one attempt;
// their retry fields have never been part of the documented contract — only
// fallback's per-target retry block is. Backoff normalisation (an unset
// InitialBackoffMs falling back to a sane default rather than 0) lives once
// in strategies.WaitBeforeRetry, via strategies.NormalizeBackoffMs, so this
// helper never re-derives that guard: an unset InitialBackoffMs gets the same
// jittered-exponential wait here as it does for /v1/chat/completions instead
// of hammering the provider with immediate retries.
func (g *Gateway) runTargetAttempts(ctx context.Context, targetKey string, call func(context.Context) error) error {
	g.mu.RLock()
	mode := g.config.Strategy.Mode
	var retry *RetryConfig
	for i := range g.config.Targets {
		if g.config.Targets[i].VirtualKey == targetKey {
			retry = g.config.Targets[i].Retry
			break
		}
	}
	g.mu.RUnlock()

	attempts := 1
	if mode == ModeFallback && retry != nil && retry.Attempts > 0 {
		attempts = retry.Attempts
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			proceed, err := strategies.WaitBeforeRetry(ctx, attempt, retry.InitialBackoffMs, lastErr)
			if err != nil {
				return err
			}
			if !proceed {
				break
			}
		}
		err := call(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if mode != ModeFallback || retry == nil || !strategies.ShouldRetry(err, retry.OnStatusCodes) {
			break
		}
	}
	if lastErr == nil {
		return fmt.Errorf("provider %s was not attempted", targetKey)
	}
	return lastErr
}
