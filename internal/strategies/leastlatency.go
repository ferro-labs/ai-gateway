package strategies

import (
	"math/rand"
	"sort"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/providers"
)

// explorationShare is the fraction of requests that lead with a random
// sampled non-leader, so the ranking keeps learning after every target has a
// window. Bounded, so the leader still takes the large majority.
const explorationShare = 0.1

// LeastLatency routes to whichever compatible provider has the lowest observed
// p50 latency for the request's upstream model.
//
// The sample is the time the target took to BEGIN answering: for a stream,
// until its first chunk; for a unary call, until the response returned, since
// it arrives whole. It is not the time to finish, so a model whose replies are
// long does not read as a slow provider. Samples are keyed by target and
// upstream model, so two models mapped onto one target rank on their own
// numbers, and they expire (latency.DefaultSampleTTL): a target nothing has
// measured recently is unseen, not ranked on a p50 from before an incident.
//
// Unseen targets lead so they get profiled; sampled ones follow by p50. Once
// every target is sampled the leader would lock in — nothing but its own
// samples changes, so a sibling that recovered is never seen — so
// explorationShare of requests lead with a sampled non-leader instead.
//
// Do not read the ordering as a claim about provider health — /health,
// /readyz and the circuit-breaker metric answer that.
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
// unseen providers (no live samples for this upstream model) are shuffled to
// the front so cold-start traffic profiles each of them, followed by sampled
// providers ascending by p50 — with explorationShare of requests leading on a
// random sampled non-leader. Remaining targets follow in declared order (see
// Strategy.SelectTargets).
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
		p50, hasSamples := l.tracker.Stats(t.VirtualKey, upstreamModel(t, req.Model))
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
	if len(unseen) == 0 && len(sampled) > 1 && rand.Float64() < explorationShare { //nolint:gosec // G404: exploration draw, not security-sensitive
		i := 1 + rand.Intn(len(sampled)-1) //nolint:gosec // G404: as above
		sampled[0], sampled[i] = sampled[i], sampled[0]
	}

	keys := make([]string, 0, len(l.targets))
	for _, candidate := range unseen {
		keys = appendUniqueKey(keys, candidate.key)
	}
	for _, candidate := range sampled {
		keys = appendUniqueKey(keys, candidate.key)
	}
	return appendRemainingTargetKeys(keys, l.targets), nil
}

// upstreamModel is the model the target is actually asked for: the model_map
// translation when one exists, otherwise the visible name.
func upstreamModel(t Target, model string) string {
	if mapped := t.ModelMap[model]; mapped != "" {
		return mapped
	}
	return model
}
