package strategies

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Fallback tries each target in order, moving to the next on failure.
type Fallback struct {
	targets    []Target
	lookup     ProviderLookup
	maxRetries int
}

// NewFallback creates a new fallback strategy.
func NewFallback(targets []Target, lookup ProviderLookup) *Fallback {
	return &Fallback{
		targets:    targets,
		lookup:     lookup,
		maxRetries: 1,
	}
}

// WithMaxRetries sets the number of retries per target before moving to the next.
func (f *Fallback) WithMaxRetries(n int) *Fallback {
	f.maxRetries = n
	return f
}

// Execute attempts each provider in order, retrying on failure with exponential backoff.
func (f *Fallback) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if len(f.targets) == 0 {
		return nil, fmt.Errorf("no targets configured for fallback")
	}

	var lastErr error
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

		for attempt := 0; attempt < f.maxRetries; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(math.Pow(2, float64(attempt-1))) * 100 * time.Millisecond
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
		}
	}

	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}
