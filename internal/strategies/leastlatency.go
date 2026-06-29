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
		p50, hasSeen := l.tracker.Stats(t.VirtualKey)
		candidates = append(candidates, candidate{
			target:  t,
			p50:     p50,
			hasSeen: hasSeen,
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no provider supports model %s", req.Model)
	}

	// Collect unseen providers so they get sampled before we commit to a best-known.
	// This ensures all providers are profiled during cold-start, not just the first
	// one that happened to be picked at random.
	var unseen []*candidate
	for i := range candidates {
		if !candidates[i].hasSeen {
			unseen = append(unseen, &candidates[i])
		}
	}
	if len(unseen) > 0 {
		// Round-robin through unseen providers to gather latency samples for each
		// before settling on the best-known option.
		pick := unseen[rand.Intn(len(unseen))] //nolint:gosec
		p, ok := l.lookup(pick.target.VirtualKey)
		if !ok {
			return nil, fmt.Errorf("least latency based routing: provider not found: %s", pick.target.VirtualKey)
		}
		resp, err := p.Complete(ctx, req)
		if err != nil {
			return nil, err
		}
		return responseWithProvider(resp, pick.target.VirtualKey), nil
	}

	// All providers have been sampled — pick the one with the lowest p50.
	var best *candidate
	for i := range candidates {
		c := &candidates[i]
		if best == nil || c.p50 < best.p50 {
			best = c
		}
	}

	p, ok := l.lookup(best.target.VirtualKey)
	if !ok {
		return nil, fmt.Errorf("least latency based routing: provider not found: %s", best.target.VirtualKey)
	}
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	return responseWithProvider(resp, best.target.VirtualKey), nil
}
