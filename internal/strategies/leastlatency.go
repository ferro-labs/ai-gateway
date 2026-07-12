package strategies

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
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
		pick := unseen[rand.Intn(len(unseen))] //nolint:gosec // G404: math/rand is fine for cold-start provider sampling, not security-sensitive
		return dispatch(ctx, l.lookup, pick.target, req, "least latency based routing: provider not found")
	}

	// All providers have been sampled — pick the one with the lowest p50.
	var best *candidate
	for i := range candidates {
		c := &candidates[i]
		if best == nil || c.p50 < best.p50 {
			best = c
		}
	}

	return dispatch(ctx, l.lookup, best.target, req, "least latency based routing: provider not found")
}

// latencyOrderCandidate holds a streaming-capable target with its observed p50.
type latencyOrderCandidate struct {
	key        string
	p50        time.Duration
	hasSamples bool
}

// SelectTargets orders streaming-capable targets by observed p50 latency:
// unseen providers (no samples yet) are shuffled to the front so cold-start
// traffic profiles each of them, followed by sampled providers ascending by
// p50. Remaining targets are appended as fallbacks. When no target is a
// streaming candidate the declared target order is returned unchanged.
func (l *LeastLatency) SelectTargets(req providers.Request) ([]string, error) {
	var unseen, sampled []latencyOrderCandidate
	for _, t := range l.targets {
		if !streamCandidate(l.lookup, t.VirtualKey, req.Model) {
			continue
		}
		p50, hasSamples := l.tracker.Stats(t.VirtualKey)
		candidate := latencyOrderCandidate{key: t.VirtualKey, p50: p50, hasSamples: hasSamples}
		if hasSamples {
			sampled = append(sampled, candidate)
		} else {
			unseen = append(unseen, candidate)
		}
	}

	if len(unseen) == 0 && len(sampled) == 0 {
		return targetKeys(l.targets), nil
	}

	if len(unseen) > 1 {
		rand.Shuffle(len(unseen), func(i, j int) {
			unseen[i], unseen[j] = unseen[j], unseen[i]
		})
	}
	sort.SliceStable(sampled, func(i, j int) bool {
		return sampled[i].p50 < sampled[j].p50
	})

	keys := make([]string, 0, len(l.targets))
	for _, candidate := range unseen {
		keys = appendUniqueKey(keys, candidate.key)
	}
	for _, candidate := range sampled {
		keys = appendUniqueKey(keys, candidate.key)
	}
	return appendRemainingTargetKeys(keys, l.targets), nil
}
