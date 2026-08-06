package strategies

import (
	"math/rand"
	"sort"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/providers"
)

// LeastLatency routes to whichever compatible provider has the lowest observed
// p50 latency. Providers without recorded samples are candidates only when all
// compatible providers are unseen; in that case one is selected at random.
//
// The sample is total wall-clock for the request, so it measures how long a
// completion took, not how fast the provider was. Generation time dominates it:
// a provider that answers the same prompt at length reads as slower than a terse
// one on identical hardware, and a model whose replies are simply longer will
// lose to a model whose replies are shorter regardless of service speed.
//
// That is the intended trade. Wall-clock is what a caller waits, so ranking on
// it optimises the number the caller actually experiences. Isolating service
// speed would mean ranking on time-to-first-token, which says nothing about
// when the response finishes. Choose this strategy when total response time is
// the objective; do not read its ordering as a claim about provider health —
// /health, /readyz and the circuit-breaker metric answer that.
type LeastLatency struct {
	targets []Target
	lookup  ProviderLookup
	tracker *latency.Tracker
}

// NewLeastLatency creates a new least-latency strategy.
func NewLeastLatency(targets []Target, lookup ProviderLookup, tracker *latency.Tracker) *LeastLatency {
	return &LeastLatency{targets: targets, lookup: lookup, tracker: tracker}
}

// latencyOrderCandidate holds a candidate target with its observed p50.
type latencyOrderCandidate struct {
	key        string
	p50        time.Duration
	hasSamples bool
}

// SelectTargets orders model-compatible targets by observed p50 latency:
// unseen providers (no samples yet) are shuffled to the front so cold-start
// traffic profiles each of them, followed by sampled providers ascending by
// p50. Remaining targets follow in declared order; this mode does not advance
// past a failing target, so they stand in only when a preferred one is skipped
// (see Strategy.SelectTargets).
//
// When no target serves the model it returns nil rather than the declared
// order, so the caller reports "nothing here can serve this" instead of
// attempting a target the strategy already ruled out.
func (l *LeastLatency) SelectTargets(req providers.Request) ([]string, error) {
	var unseen, sampled []latencyOrderCandidate
	for _, t := range l.targets {
		if !routableCandidate(l.lookup, t.VirtualKey, req.Model) {
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
		return nil, nil
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
