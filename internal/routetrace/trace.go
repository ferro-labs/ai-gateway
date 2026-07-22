// Package routetrace implements the dry-run route explanation surfaced by the
// POST /v1/route/trace endpoint (issue #238).
//
// It reproduces how the active routing strategy would SELECT a target for a
// given request WITHOUT calling any provider, so it consumes no provider
// quota and never opens a circuit. The output is a structured trace that a
// client-facing router UX (e.g. Pi's Ferro router surface) can render to
// explain why a request would route somewhere.
//
// The selection logic here mirrors the per-strategy Execute implementations in
// internal/strategies/. If a strategy's selection rules change, the matching
// branch in Trace() should be updated to keep the dry-run faithful.
package routetrace

import (
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// StrategyMode is the gateway's strategy mode (re-declared here to avoid an
// import cycle with the root gateway package). Values MUST match
// aigateway.StrategyMode / aigateway.Mode*.
type StrategyMode string

// Strategy modes the tracer understands, mirroring aigateway.Mode*.
const (
	ModeSingle        StrategyMode = "single"
	ModeFallback      StrategyMode = "fallback"
	ModeLoadBalance   StrategyMode = "loadbalance"
	ModeConditional   StrategyMode = "conditional"
	ModeLatency       StrategyMode = "least-latency"
	ModeCostOptimized StrategyMode = "cost-optimized"
)

// Target mirrors aigateway.Target for the trace (only the fields the dry-run
// needs to evaluate candidates).
type Target struct {
	VirtualKey string
	Weight     float64
}

// ConditionRule mirrors strategies.ConditionRule.
type ConditionRule struct {
	Key   string // "model" | "model_prefix"
	Value string
	Target
}

// Config is a read-only view of the gateway state the tracer needs. The
// gateway constructs this from its live config under its mutex.
type Config struct {
	Mode            StrategyMode
	Targets         []Target
	Conditions      []ConditionRule
	Aliases         map[string]string
	Catalog         models.Catalog
	Lookup          func(name string) (providers.Provider, bool)
	CircuitState    func(name string) circuitbreaker.State
	LatencyP50      func(name string) time.Duration
	LatencyHasSamps func(name string) bool
	CostForTarget   func(targetKey, modelID string) (models.CostResult, bool)
}

// CandidateTarget explains one configured target's evaluation for the request.
type CandidateTarget struct {
	TargetKey         string  `json:"target_key"`
	Matched           bool    `json:"matched"`
	Reason            string  `json:"reason"`
	Healthy           bool    `json:"healthy"`
	CircuitOpen       bool    `json:"circuit_open"`
	SupportsModel     bool    `json:"supports_model"`
	Weight            float64 `json:"weight,omitempty"`
	EstimatedCostUSD  float64 `json:"estimated_cost_usd"`
	P50LatencyMs      int64   `json:"p50_latency_ms,omitempty"`
	HasLatencySamples bool    `json:"has_latency_samples,omitempty"`
}

// CatalogMatch summarizes the model-catalog lookup for the requested model.
type CatalogMatch struct {
	ModelFound    bool   `json:"model_found"`
	Priced        bool   `json:"priced"`
	ContextWindow int    `json:"context_window"`
	Mode          string `json:"mode,omitempty"`
	Status        string `json:"status,omitempty"`
	Provider      string `json:"provider,omitempty"`
}

// TraceResponse is the JSON contract returned by POST /v1/route/trace.
type TraceResponse struct {
	Strategy          StrategyMode      `json:"strategy"`
	RequestedModel    string            `json:"requested_model"`
	ResolvedModel     string            `json:"resolved_model"`
	SelectedTargetKey string            `json:"selected_target_key,omitempty"`
	SelectedModel     string            `json:"selected_model,omitempty"`
	CandidateTargets  []CandidateTarget `json:"candidate_targets"`
	Catalog           CatalogMatch      `json:"catalog"`
	TraceID           string            `json:"trace_id,omitempty"`
	DryRun            bool              `json:"dry_run"`
}

// Trace builds a route trace for the requested model using cfg. It never calls
// a provider, consumes no quota, and never opens a circuit.
//
// CandidateTargets is ordered to reflect the strategy's preference: for
// single/conditional the matched target is first; for fallback/cost/latency
// the eligible targets are ranked by the strategy's selection rule.
func Trace(cfg Config, requestedModel string) TraceResponse {
	resp := TraceResponse{
		Strategy:       cfg.Mode,
		RequestedModel: requestedModel,
		ResolvedModel:  requestedModel,
		DryRun:         true,
	}

	// Apply gateway alias resolution (mirrors Gateway.resolveModelAlias).
	if cfg.Aliases != nil {
		if target, ok := cfg.Aliases[requestedModel]; ok && target != "" {
			resp.ResolvedModel = target
		}
	}

	resp.Catalog = catalogMatch(cfg.Catalog, resp.ResolvedModel)

	candidates := buildCandidates(cfg, resp.ResolvedModel)
	resp.CandidateTargets = candidates

	selectIndex := selectIndex(cfg.Mode, candidates)
	if selectIndex >= 0 {
		c := candidates[selectIndex]
		resp.SelectedTargetKey = c.TargetKey
		resp.SelectedModel = resp.ResolvedModel
	}

	return resp
}

// buildCandidates evaluates every configured target for the resolved model
// WITHOUT calling it. SupportsModel comes from the provider's own
// SupportsModel check; Healthy and CircuitOpen come from the circuit breaker
// (when configured). Matched reflects the strategy's selection rule — for
// conditional only the rule-matched target is Matched; for the ranked
// strategies every eligible target is Matched and ordering carries the
// preference.
func buildCandidates(cfg Config, model string) []CandidateTarget {
	if len(cfg.Targets) == 0 {
		return nil
	}

	// Determine which target the conditional strategy would pick, so we can
	// mark exactly that one as matched (mirrors Conditional.matchTarget).
	conditionalMatchKey := ""
	if cfg.Mode == ModeConditional {
		conditionalMatchKey = matchedConditionalTargetKey(cfg.Conditions, model, cfg.Targets)
	}

	out := make([]CandidateTarget, 0, len(cfg.Targets))
	for _, t := range cfg.Targets {
		c := CandidateTarget{
			TargetKey:     t.VirtualKey,
			Weight:        t.Weight,
			Matched:       true,
			Healthy:       true,
			SupportsModel: true,
		}

		// Provider presence + model support.
		p, ok := cfg.Lookup(t.VirtualKey)
		if !ok {
			c.Reason = "provider not registered"
			c.Matched = false
			c.Healthy = false
			c.SupportsModel = false
			out = append(out, c)
			continue
		}
		if !p.SupportsModel(model) {
			c.SupportsModel = false
			c.Matched = false
			c.Reason = "provider does not support requested model"
			out = append(out, c)
			continue
		}

		// Circuit breaker state (optional).
		if cfg.CircuitState != nil {
			state := cfg.CircuitState(t.VirtualKey)
			c.CircuitOpen = state == circuitbreaker.StateOpen
			if c.CircuitOpen {
				c.Healthy = false
				c.Matched = false
				c.Reason = "circuit breaker open"
			}
		}

		// Conditional: only the rule-matched target (or the configured
		// fallback target = Targets[0]) is Matched.
		if cfg.Mode == ModeConditional && conditionalMatchKey != "" && t.VirtualKey != conditionalMatchKey {
			c.Matched = false
			c.Reason = "conditional rule did not match"
		}

		// Cost optimization hook (per-target estimated cost for the model).
		if cfg.CostForTarget != nil {
			if cost, found := cfg.CostForTarget(t.VirtualKey, model); found {
				c.EstimatedCostUSD = cost.TotalUSD
			}
		}

		// Latency hook (least-latency).
		if cfg.LatencyP50 != nil {
			c.P50LatencyMs = cfg.LatencyP50(t.VirtualKey).Milliseconds()
		}
		if cfg.LatencyHasSamps != nil {
			c.HasLatencySamples = cfg.LatencyHasSamps(t.VirtualKey)
		}

		if c.Matched && c.Reason == "" {
			c.Reason = "model condition matched"
		}
		out = append(out, c)
	}

	// Rank candidates by strategy preference so callers reading
	// candidate_targets top-down see the strategy's order.
	return orderCandidates(cfg.Mode, out, conditionalMatchKey)
}

// matchedConditionalTargetKey returns the VirtualKey of the target the
// conditional strategy would pick for this model (first matching rule wins,
// else the fallback = configured Targets[0]).
func matchedConditionalTargetKey(rules []ConditionRule, model string, targets []Target) string {
	for _, rule := range rules {
		if conditionMatches(rule.Key, rule.Value, model) {
			return rule.VirtualKey
		}
	}
	if len(targets) > 0 {
		return targets[0].VirtualKey
	}
	return ""
}

// conditionMatches mirrors strategies.Conditional.matches.
func conditionMatches(key, value, model string) bool {
	switch key {
	case "model":
		return model == value
	case "model_prefix":
		return strings.HasPrefix(model, value)
	default:
		return false
	}
}

// orderCandidates reorders candidates so the strategy's preferred target is
// first (matches the issue's "ordered candidate explanations" requirement).
func orderCandidates(mode StrategyMode, in []CandidateTarget, conditionalMatchKey string) []CandidateTarget {
	if len(in) <= 1 {
		return in
	}
	switch mode {
	case ModeConditional:
		// Bring the matched candidate to the front.
		for i, c := range in {
			if c.Matched && c.TargetKey == conditionalMatchKey {
				if i == 0 {
					return in
				}
				out := make([]CandidateTarget, 0, len(in))
				out = append(out, in[i])
				out = append(out, in[:i]...)
				out = append(out, in[i+1:]...)
				return out
			}
		}
	case ModeCostOptimized:
		// Eligible first (matched+healthy+supports), lowest cost first.
		sortEligible(in, func(a, b CandidateTarget) bool {
			return a.EstimatedCostUSD < b.EstimatedCostUSD
		})
	case ModeLatency:
		// Eligible first, lowest P50-with-samples first; unsampled eligible
		// candidates sink below sampled ones (mirrors HasSeen handling).
		sortEligible(in, func(a, b CandidateTarget) bool {
			switch {
			case a.HasLatencySamples && !b.HasLatencySamples:
				return true
			case !a.HasLatencySamples && b.HasLatencySamples:
				return false
			default:
				return a.P50LatencyMs < b.P50LatencyMs
			}
		})
	case ModeSingle, ModeFallback, ModeLoadBalance, "":
		// Keep declared order. Single always routes to Targets[0]
		// regardless of any later target's health, so reordering here
		// would make the trace disagree with actual routing; fallback and
		// load-balance naturally lead with eligible candidates because the
		// gateway walks targets in declared order.
	}
	return in
}

// selectIndex returns the index of the candidate the strategy would route to.
// Returns -1 when no candidate is selectable (mirrors the Execute() error path
// the real gateway would return).
func selectIndex(mode StrategyMode, candidates []CandidateTarget) int {
	if len(candidates) == 0 {
		return -1
	}
	// After orderCandidates, the strategy's pick is the first eligible one
	// for every ranked mode; for single/conditional it's the first matched.
	for i, c := range candidates {
		if c.Matched && c.SupportsModel && c.Healthy {
			switch mode {
			case ModeSingle, "":
				if i == 0 {
					return 0
				}
				// Single only considers Targets[0]; a non-front candidate
				// cannot be selected.
				continue
			default:
				return i
			}
		}
	}
	return -1
}

// catalogMatch summarizes the catalog entry for the resolved model.
func catalogMatch(cat models.Catalog, model string) CatalogMatch {
	if cat == nil {
		return CatalogMatch{}
	}
	// Try "provider/model" first; fall back to bare model-id lookup
	// (mirrors models.Catalog.Get). Map iteration order is randomized, so
	// pick deterministically by lowest catalog key when multiple providers
	// share the same bare model ID.
	m, ok := cat[model]
	if !ok {
		bestKey := ""
		for k, v := range cat {
			if v.ModelID == model && (!ok || k < bestKey) {
				m = v
				bestKey = k
				ok = true
			}
		}
	}
	if !ok {
		return CatalogMatch{ModelFound: false}
	}
	return CatalogMatch{
		ModelFound:    true,
		Priced:        catalogPriced(m),
		ContextWindow: m.ContextWindow,
		Mode:          string(m.Mode),
		Status:        m.Lifecycle.Status,
		Provider:      m.Provider,
	}
}

// catalogPriced reports whether the catalog carries concrete token pricing
// for this model (input or output). nil pricing = not applicable for the mode.
func catalogPriced(m models.Model) bool {
	return m.Pricing.InputPerMTokens != nil || m.Pricing.OutputPerMTokens != nil
}

// String helper for reasons so the trace JSON stays readable for clients.
func (c CandidateTarget) String() string {
	var b strings.Builder
	b.WriteString(c.TargetKey)
	b.WriteString(" matched=")
	b.WriteString(boolStr(c.Matched))
	b.WriteString(" healthy=")
	b.WriteString(boolStr(c.Healthy))
	b.WriteString(" supports_model=")
	b.WriteString(boolStr(c.SupportsModel))
	if c.CircuitOpen {
		b.WriteString(" circuit=open")
	}
	return b.String()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// sortEligible is a tiny insertion sort that partitions candidates into
// eligible-then-ineligible and orders the eligible partition by less.
// We keep it inline (no sort.Slice allocation) since candidate lists are small
// (typically < 10 targets).
func sortEligible(in []CandidateTarget, less func(a, b CandidateTarget) bool) {
	eligible := make([]CandidateTarget, 0, len(in))
	ineligible := make([]CandidateTarget, 0, len(in))
	for _, c := range in {
		if c.Matched && c.SupportsModel && c.Healthy {
			eligible = append(eligible, c)
		} else {
			ineligible = append(ineligible, c)
		}
	}
	// Insertion sort the eligible partition by `less`.
	for i := 1; i < len(eligible); i++ {
		j := i
		for j > 0 && less(eligible[j], eligible[j-1]) {
			eligible[j], eligible[j-1] = eligible[j-1], eligible[j]
			j--
		}
	}
	copy(in, eligible)
	copy(in[len(eligible):], ineligible)
}
