package strategies

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/providers"
)

// LeastLatency routes to whichever compatible provider has the lowest observed
// p50 latency. Providers without recorded samples are candidates only when all
// compatible providers are unseen; in that case one is selected at random.
type LeastLatency struct {
	targets []Target
	lookup  ProviderLookup
	tracker *latency.Tracker
}

// NewLeastLatency creates a new least-latency strategy.
func NewLeastLatency(targets []Target, lookup ProviderLookup, tracker *latency.Tracker) *LeastLatency {
	return &LeastLatency{targets: targets, lookup: lookup, tracker: tracker}
}

// Execute selects the compatible provider with the lowest p50 latency and
// forwards the request to it.
func (l *LeastLatency) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if len(l.targets) == 0 {
		return nil, fmt.Errorf("no targets configured for least-latency strategy")
	}

	type candidate struct {
		target  Target
		p50     time.Duration
		hasSeen bool
	}

	var candidates []candidate
	for _, t := range l.targets {
		p, ok := l.lookup(t.VirtualKey)
		if !ok || !p.SupportsModel(req.Model) {
			continue
		}
		candidates = append(candidates, candidate{
			target:  t,
			p50:     l.tracker.P50(t.VirtualKey),
			hasSeen: l.tracker.HasSamples(t.VirtualKey),
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no provider supports model %s", req.Model)
	}

	// Among providers with observed latency, pick the one with the lowest p50.
	var best *candidate
	for i := range candidates {
		c := &candidates[i]
		if !c.hasSeen {
			continue
		}
		if best == nil || c.p50 < best.p50 {
			best = c
		}
	}

	// No observations yet — fall back to random selection so cold-start traffic
	// is distributed evenly and the tracker warms up across all providers.
	if best == nil {
		best = &candidates[rand.Intn(len(candidates))] //nolint:gosec
	}

	p, _ := l.lookup(best.target.VirtualKey)
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	return responseWithProvider(resp, best.target.VirtualKey), nil
}
