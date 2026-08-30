package aigateway

import (
	"context"
	"fmt"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// The routed request pipeline.
//
// Every surface the gateway routes — chat, streaming chat, embeddings, image
// generation — is the same walk: the strategy names an ordered list of
// candidate targets, and the gateway calls them until one answers. Each attempt
// runs under that target's retry policy, its circuit breaker and its
// concurrency limit, and the walk stops at the first candidate unless the
// strategy is one that falls back.
//
// Only two things genuinely differ between surfaces, and they are the only two
// things a caller supplies: which capability a target must have to be a
// candidate, and what the call gives back. Everything else — ordering, retry,
// breaker, limiter, "nothing here can serve this", which target to blame —
// lives here once.
//
// The walk itself is one ordinary function, not a generic one. The type
// parameters exist only on the leaf call, so a request and a response can cross
// the boundary without being boxed into `any` — boxing a Request would allocate
// on every call, and a type parameter is not otherwise faster than an interface
// (go.dev/blog/when-generics). That is the same split every comparable proxy
// made: LiteLLM's router hands one reliability wrapper an `original_function`
// per surface; Portkey's tryTargetsRecursively takes the surface as a value.
//
// Ownership follows Envoy's. Its route table decides where a request may go and
// its cluster decides which endpoint answers, but the RETRY POLICY hangs off the
// route and the CIRCUIT BREAKER off the cluster — neither belongs to the
// load-balancing policy. So here a strategy decides ORDER and nothing else: a
// strategy that picks differently does not get to retry differently. See
// internal/strategies.Strategy.
//
// Plugin stages stay OUTSIDE this walk, and that placement is the point. Envoy
// runs its downstream filter chain once and its upstream chain per retry
// attempt; a budget or guardrail that ran inside the retry loop would bill a
// request three times for one call.
//
// It is deliberately NOT the whole request either. Outcome recording stays with
// the surface, because "the request finished" does not mean the same thing on
// all four: a unary call knows its outcome when it returns, a stream does not
// know it until the channel drains. A hook shaped like a unary return can never
// observe what happens after it returns — the constraint that keeps gRPC-go's
// stream interceptors wrapping a ClientStream rather than post-processing a
// result. The pipeline is the part that IS the same: everything up to and
// including the call that either starts or fails.

// targetPlan is the strategy's decision for one request, resolved once before
// any provider is called.
type targetPlan struct {
	// keys is the strategy's candidate order, most preferred first. It is read
	// and never written, so a strategy may return a slice it owns.
	keys []string
	// model gates candidacy: a target whose provider does not serve this model
	// is not a candidate and is never called.
	model string
	// advance says the strategy falls back — try the next candidate when one
	// fails. When false only the FIRST key is eligible, which is what every
	// single-target strategy means: an a/b variant that cannot serve the request
	// is a 404, not a silent reroute to somebody else's target.
	advance bool
	// ignoreCircuitState opts out of the health filter eligibleKeys applies.
	//
	// Exactly one caller sets it: the /v1/models listing (Gateway.routingServes),
	// which shares this plan with the walk so the two cannot disagree about what
	// routing would do. A model listing must NOT move with breaker state — a
	// model that vanished while a breaker was open reads to a client as "this
	// gateway does not serve that model" and sends it to change its request over
	// an outage that clears itself. /health and /readyz report circuit state;
	// the inventory reports what is configured. See AllModels.
	ignoreCircuitState bool
	// responseOutlivesCall says the leaf call returns something still running —
	// streaming's channel — so the call RETURNING is not the request finishing.
	//
	// Both resilience decorators are then wrong at the call site and are handed
	// to the surface instead: resolving the breaker on return would score a
	// stream that merely STARTED as a success, and releasing the concurrency
	// slot on return would let unlimited streams run past a cap that only ever
	// counted their setup. The surface takes them as provider wrappers, which
	// hold the slot for the whole stream and leave the breaker outcome to
	// whatever observes the end of it.
	//
	// This is the same asymmetry that makes gRPC-go's stream interceptors wrap a
	// ClientStream instead of post-processing a result: the terminal status is
	// not available when the call returns.
	responseOutlivesCall bool
}

// targetCall performs one surface's provider call. It is passed as a plain
// function rather than a closure so the request does not have to be captured,
// which keeps the hot path free of a per-request closure allocation.
type targetCall[Req, Resp any] func(context.Context, providers.Provider, Req, string) (Resp, error)

// capabilityGate reports whether a provider can serve a surface at all —
// providers.StreamProvider for streaming, providers.EmbeddingProvider for
// embeddings, and so on. A nil gate means the base providers.Provider is
// enough, which is chat's case.
type capabilityGate func(providers.Provider) bool

// routedTarget carries both identities fixed at provider selection. key is the
// configured routing target used for attribution; priceProvider is the
// canonical vendor used only for catalog pricing.
type routedTarget struct {
	key           string
	priceProvider string
	upstreamModel string
}

// routeTargets walks plan and returns the first target's answer.
//
// The second return identifies the target that produced the response or, on
// failure, the last one attempted. Its key is empty only when nothing was
// attempted at all. Callers label metrics and spans with key; pricing uses the
// canonical provider captured from the same selected provider instance.
//
// A failure that is not the fault of any target — nothing registered, nothing
// that serves the model — is reported as core.ErrNoCapableProvider so it
// classifies as the caller's 404 rather than the gateway's 500. That decision
// rests on whether a call was ever made, never on whether some skipped target
// left an error behind.
func routeTargets[Req, Resp any](
	ctx context.Context,
	g *Gateway,
	plan targetPlan,
	req Req,
	gate capabilityGate,
	call targetCall[Req, Resp],
) (Resp, routedTarget, error) {
	var zero Resp

	keys := g.eligibleKeys(plan, gate)

	var (
		lastErr    error
		lastTarget routedTarget
		attempts   int
		// A failure the walk moves past is invisible to the terminal recorder,
		// which only ever names the last target tried. Under mode: fallback a
		// provider can fail every request and read zero on
		// gateway_provider_errors_total, while its circuit breaker — which does
		// count each attempt — opens. The two signals then contradict each
		// other about the same provider.
		//
		// It is recorded when the NEXT attempt begins rather than on failure:
		// only then has the walk committed to moving on, so the terminal
		// recorder can never also name it. The failure still pending at return
		// belongs to lastKey, which the surface records.
		maskedKey string
		maskedErr error
	)
	for _, key := range keys {
		p, cb, lim, upstreamModel, ok := g.resolveTarget(ctx, key, plan.model, gate)
		if !ok {
			continue
		}
		target := routedTarget{key: key, priceProvider: providers.CanonicalName(p), upstreamModel: upstreamModel}
		if plan.responseOutlivesCall {
			// Composition and order are decorateProvider's, which is the same
			// pair callUnderResilience applies at the call site — breaker
			// outermost, limiter innermost. Only WHEN they resolve differs.
			p = decorateProvider(key, p, cb, lim)
			cb, lim = nil, nil
		}
		attempts++
		// No clear is needed after recording: every path out of this loop body
		// either returns on success or reassigns both, so the pair cannot be
		// read twice for one failure.
		if maskedErr != nil {
			recordProviderErrorCtx(ctx, maskedKey, maskedErr)
		}
		lastTarget = target

		started := time.Now()
		resp, err := attemptTarget(ctx, g, key, p, cb, lim, req, upstreamModel, call)
		if err == nil {
			// Latency is recorded against the TARGET KEY and covers the provider
			// call only. Both halves matter: least-latency reads its samples back
			// by virtual key (LeastLatency.SelectTargets), and a measurement that
			// included plugin and alias time would rank targets on work no target
			// did.
			g.latencyTracker.Record(key, time.Since(started))
			return resp, target, nil
		}
		lastErr = fmt.Errorf("target %s: %w", key, err)
		maskedKey, maskedErr = key, err
		if !plan.advance {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}

	// Nothing was ever asked, so nothing failed: every candidate was either
	// unregistered or serving a different model. That is the caller's 404, and
	// it is decided on the attempt count alone — never on whether some skipped
	// target happened to leave an error behind.
	if attempts == 0 {
		return zero, routedTarget{}, errNoCapableTarget(plan.model)
	}
	if plan.advance {
		// Only a strategy that falls back can honestly claim this: it really did
		// ask every candidate. A single-target mode asked one, and says so.
		return zero, lastTarget, fmt.Errorf("all providers failed: %w", lastErr)
	}
	return zero, lastTarget, lastErr
}

// eligibleKeys narrows a strategy's candidate order to the targets the walk may
// actually attempt.
//
// A strategy that falls back may attempt all of them. A strategy that does not
// chose ONE, and the rest of its list is the fallback order the streaming path
// appends and only fallback consumes.
//
// The choice is honoured from the first target that could serve this SURFACE at
// all, not from keys[0] flat: SelectTargets is surface-neutral, so on
// /v1/embeddings and /v1/images the strategy may well have picked a target that
// was never a candidate — 10 of 30 providers cannot embed and 22 cannot generate
// images. Committing to that pick answers 404 on a coin-flip while a capable
// target sits behind it. Model mismatch is NOT skipped here: the strategy was
// handed the request, so a target it named anyway is a decision, and overriding
// it would silently reroute traffic an operator aimed somewhere specific. A
// strategy that CHOOSES from a set rather than naming one rules the model out
// itself, before it names anything — see servesSurface.
//
// Targets whose circuit is OPEN are dropped first, under every routing mode —
// see healthyKeys.
//
// Factored out of routeTargets so the model listing can ask which targets a
// request would reach rather than which ones the strategy mentioned. The two
// answers differ under exactly the modes that commit to one target, and while
// only the walk knew the rule, /v1/models advertised what those modes refuse.
func (g *Gateway) eligibleKeys(plan targetPlan, gate capabilityGate) []string {
	keys := g.healthyKeys(plan)
	if plan.advance || len(keys) <= 1 {
		return keys
	}
	for i, key := range keys {
		if g.servesSurface(key, gate) {
			return keys[i : i+1]
		}
	}
	return keys[:1]
}

// healthyKeys drops the candidates whose circuit is OPEN, and does so under
// every routing mode.
//
// The NAMED modes commit to one of the keys the strategy chose, and do not
// advance past it (see advancesPastFailure). Selection itself was breaker-blind,
// so a mode that commits could commit to a target the gateway had already decided
// not to call — and then answer 503 while a healthy sibling sat unused. Ranking
// modes were worst: a failed call records no latency sample (see routeTargets),
// so a target that was fastest and then died keeps its low p50, stays ranked
// first, and fails every request. Excluding it is what fixes that; a failure
// PENALTY would not, because a dead target fails fast — connection refused in
// about a millisecond — so its measured duration ranks it first, and a single
// sample cannot move a p50 taken over a 100-sample window anyway.
//
// This is deliberately the same rule Readiness applies (gateway_health.go): a
// target is routable when its circuit is not open, and a target with NO breaker
// configured is healthy, because "would a call be admitted" is the question and
// for that target the answer is yes. Reading state is non-consuming — it never
// admits or spends a half-open probe — and a HALF-OPEN target stays eligible,
// which is what lets a recovering target earn its probe through normal traffic.
//
// It filters, it does not refuse: when every candidate is open the UNFILTERED
// list is returned. That fail-open is load-bearing in two directions.
//
// Returning an empty list instead would make routeTargets attempt nothing, and
// "nothing was attempted" is errNoCapableTarget — a 404 model_not_found. "No
// target serves this model" and "every target that serves it is down" are
// different answers, and only the first is the caller's to fix. Handing the
// unfiltered list back instead lets the walk attempt a target, the breaker
// refuse it with circuitbreaker.ErrCircuitOpen, and apierror classify that as
// the 503 it already classifies it as. Every comparable system answers 503 here
// — Envoy's "no healthy upstream", HAProxy, Kong, ingress-nginx, gRPC's
// UNAVAILABLE — and none answers 404. 429 would be wrong for a different
// reason: it tells a caller at 1 rps that THEY are being throttled, and sends
// them hunting for their own rate limit while the upstream is down.
//
// It also keeps an operator's explicit routing decision from being overridden.
// Under `conditional` or `content-based` a matched rule names a target on
// purpose; the filter makes that target preferred-but-not-exclusive rather than
// ignored, and when nothing else is healthy the rule still gets its way.
//
// Circuit state is one thing the strategy genuinely cannot know — the same
// argument servesSurface makes about surface capability, and the reason both
// tests live here rather than in a strategy. Model support is not: a strategy
// was handed the request, so a target it named anyway is a decision to honour.
//
// The common case allocates nothing: with no open circuit the caller's own
// slice is returned untouched.
func (g *Gateway) healthyKeys(plan targetPlan) []string {
	if plan.ignoreCircuitState || len(plan.keys) <= 1 {
		return plan.keys
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	var healthy []string
	for i, key := range plan.keys {
		if cb := g.circuitBreakers[key]; cb != nil && cb.State() == circuitbreaker.StateOpen {
			if healthy == nil {
				// First open target: copy what was kept so far, and only now.
				healthy = make([]string, 0, len(plan.keys)-1)
				healthy = append(healthy, plan.keys[:i]...)
			}
			continue
		}
		if healthy != nil {
			healthy = append(healthy, key)
		}
	}
	// Empty covers both "nothing was open" (never allocated) and "everything was
	// open" (allocated, nothing kept). Both mean: filter nothing.
	if len(healthy) == 0 {
		return plan.keys
	}
	return healthy
}

// errNoCapableTarget is the refusal every routed surface reports when no
// configured target serves a model. It is built in one place so the pre-flight
// admission check and the walk itself cannot classify the same condition
// differently — the handlers map core.ErrNoCapableProvider to
// 404 invalid_request_error/model_not_found.
func errNoCapableTarget(model string) error {
	return fmt.Errorf("no provider supports model %s: %w", model, core.ErrNoCapableProvider)
}

// candidateLocked is the whole of resolveTarget's candidacy test for one
// registered provider: it serves the model according to the routing index, and
// it can serve this surface.
//
// Factored out so admitModel asks the router's own question rather than a
// second one beside it. A pre-flight that consulted anything else is exactly the
// defect that made the previous one wrong: it read the REGISTERED provider set
// while the walk read the targets, so the two gave different answers for the
// same request.
//
// Caller must hold g.mu (a read lock is sufficient).
func (g *Gateway) candidateLocked(key string, p providers.Provider, model string, gate capabilityGate) bool {
	if !g.supportsModelForRoutingLocked(key, p, model) {
		return false
	}
	return gate == nil || gate(p)
}

// admitModel refuses a request no configured target can serve, and is called
// BEFORE the before_request plugin stage — except when that stage can still
// change which model is routed, which is the first thing it tests.
//
// The order is the point. The stage is not free — a rate limiter spends a
// token, a budget spends money — so running it for a model that can never reach
// a provider let any caller drain a shared limiter with requests that cost the
// gateway nothing upstream and cost every other caller their quota. Admission
// therefore comes first, and the refusal still runs the on_error stage, so a
// request denied here is on record exactly as one denied by a plugin is.
//
// A transform plugin is the exception, because it is precisely the thing that
// turns an unroutable model into a routable one: a request naming a team-wide
// alias that a transform rewrites to a real model id has no answer here yet.
// So when any before_request plugin reports plugin.TypeTransform, admission is
// skipped and the routing walk answers the same 404 afterwards, on the model
// the plugins settled on.
//
// The trade is deliberate: a deployment that runs a transform gives up the
// drain protection, because refusing a request a plugin was about to make
// routable is a wrong answer, and no amount of quota protection makes a wrong
// answer acceptable. Correctness comes before the optimisation. The question is
// about the plugin set rather than about one surface, so all four stand down
// together.
//
// It is deliberately WIDER than the walk it precedes. The walk sees only the
// targets the strategy ordered, and the strategy runs later — after the plugins
// that may still rewrite the request. So this asks whether ANY configured target
// is a candidate, and a model some target serves is always admitted even when
// the strategy would go on to offer none. The walk answers errNoCapableTarget in
// that case regardless, so the only direction this can be wrong in is the one
// that changes no response.
//
// Being wider is also why running the check a SECOND time, after the stage,
// would recover nothing: every model it would refuse the walk already refuses,
// with the same error and at the same point in the request — by which time the
// tokens are spent. The protection is only ever worth anything ahead of the
// stage.
//
// Everything that can make a model routable is covered because
// candidateLocked is the router's own test: aliases (resolved by the callers
// before this runs), targets[].models, live discovery, the catalog, and a
// provider declaring core.AnyModelProvider.
func (g *Gateway) admitModel(ctx context.Context, plugins *plugin.Manager, model string, gate capabilityGate) error {
	if plugins.HasBeforeRequestTransform() {
		return nil
	}

	g.mu.RLock()
	// A config with no targets at all is a different diagnosis — buildStrategy
	// reports it, and saying "no provider serves this model" instead would send
	// an operator looking for the model rather than for the empty target list.
	admit := len(g.config.Targets) == 0
	var unregistered []string
	for _, t := range g.config.Targets {
		p, registered := g.providers[t.VirtualKey]
		if !registered {
			unregistered = append(unregistered, t.VirtualKey)
			continue
		}
		if g.candidateLocked(t.VirtualKey, p, model, gate) {
			admit = true
			break
		}
	}
	g.mu.RUnlock()

	if admit {
		return nil
	}
	// Refusing here means the walk that logs this never runs, and the name of a
	// target no provider answers to is the whole diagnosis of the most common
	// cause — a credential that has not rolled out. It is deliberately kept out
	// of the response (see resolveTarget), so the operator has to read it here.
	// Same line, same key, and only on the refusal path, so a config with an
	// absent credential does not gain a log line per served request.
	for _, key := range unregistered {
		g.log.Ctx(ctx).Warn("provider not found, skipping", "provider", key)
	}
	return errNoCapableTarget(model)
}

// resolveTarget looks up one candidate and the resilience decorators configured
// for it, in a single lock acquisition. It reports false when the target is not
// registered, does not serve the model, or cannot serve this surface.
func (g *Gateway) resolveTarget(ctx context.Context, key, model string, gate capabilityGate) (providers.Provider, *circuitbreaker.CircuitBreaker, *providerLimiter, string, bool) {
	g.mu.RLock()
	p, registered := g.providers[key]
	serves := registered && g.candidateLocked(key, p, model, gate)
	cb := g.circuitBreakers[key]
	lim := g.limiters[key]
	upstreamModel := model
	for _, target := range g.config.Targets {
		if target.VirtualKey == key {
			if mapped := target.ModelMap[model]; mapped != "" {
				upstreamModel = mapped
			}
			break
		}
	}
	g.mu.RUnlock()

	if !registered {
		// Logged, not returned. A target config names and no provider answers to
		// is almost always an absent credential, and its NAME is the whole
		// diagnosis — but naming it in the response would tell an API caller
		// which providers are configured and which of them are broken. The
		// operator reads it here instead.
		g.log.Ctx(ctx).Warn("provider not found, skipping", "provider", key)
		return nil, nil, nil, "", false
	}
	// A registered target that does not serve this model is ordinary routing,
	// not a fault, and logging it would put a line on the hot path of every
	// multi-target config.
	if !serves {
		return nil, nil, nil, "", false
	}
	return p, cb, lim, upstreamModel, true
}

// servesSurface reports whether a target could serve this surface at all,
// ignoring the model. It is what lets a non-fallback strategy commit to its
// choice without committing to a target that was never a candidate.
//
// The two reasons resolveTarget rejects a target are not the same kind of fact.
// "Does not serve this model" is something the strategy was in a position to
// know — the model is in the request it ordered on — so honouring its choice is
// right, and falling through would override a routing decision an operator
// expressed. The strategies that pick by weight rather than by name are not an
// exception to that; they consult the model themselves and drop the targets that
// cannot serve it, so a target they name has already passed the test (see
// LoadBalance.compatibleTargets and ABTest.eligibleVariants).
//
// "Cannot serve this surface" is something the strategy cannot know:
// SelectTargets is deliberately surface-neutral, so which capability a request
// needs is the executing surface's question, asked here. Committing to a target
// the strategy was never able to consider turns a coin-flip into a 404 while a
// capable target sits unused.
func (g *Gateway) servesSurface(key string, gate capabilityGate) bool {
	if gate == nil {
		return true
	}
	g.mu.RLock()
	p, registered := g.providers[key]
	g.mu.RUnlock()
	return registered && gate(p)
}

// attemptTarget runs one target's retry policy, calling it under its breaker
// and limiter on every try.
func attemptTarget[Req, Resp any](
	ctx context.Context,
	g *Gateway,
	key string,
	p providers.Provider,
	cb *circuitbreaker.CircuitBreaker,
	lim *providerLimiter,
	req Req,
	upstreamModel string,
	call targetCall[Req, Resp],
) (Resp, error) {
	var zero Resp
	policy := g.retryPolicyFor(key)

	var lastErr error
	for attempt := 0; attempt < policy.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if attempt > 0 {
			proceed, err := strategies.WaitBeforeRetry(ctx, attempt, policy.initialBackoffMs, lastErr)
			if err != nil {
				return zero, err
			}
			// Both outcomes are logged: a retry that happened and a target
			// abandoned because its Retry-After was longer than the gateway is
			// willing to hold a request open are each invisible otherwise, and
			// "did my retry config do anything" is the first question asked of
			// this block.
			if !proceed {
				g.log.Ctx(ctx).Info("abandoning target: Retry-After exceeds the cap",
					"target", key, "retry_after", providers.RetryAfterFrom(lastErr))
				break
			}
			g.log.Ctx(ctx).Info("retrying target", "target", key, "attempt", attempt+1)
		}
		resp, err := callUnderResilience(ctx, key, p, cb, lim, req, upstreamModel, call)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !strategies.ShouldRetry(err, policy.onStatusCodes) {
			break
		}
	}
	return zero, lastErr
}

// callUnderResilience performs one attempt with the target's circuit breaker
// outermost and its concurrency limiter innermost, so an open circuit fails
// fast without ever taking an in-flight slot or a queue position.
//
// The decorators are applied at the CALL SITE rather than by wrapping the
// provider. A wrapper embedding providers.Provider fails the
// EmbeddingProvider / ImageProvider type assertions the non-chat surfaces make,
// so a wrapper-based breaker can only ever cover chat — which is how the
// gateway ended up with two implementations of the same policy. Composition and
// semantics are identical to the wrapper pair (cbProvider, limitedProvider) it
// replaces on this path.
//
// The deferred recover is load-bearing: Allow() may have admitted a half-open
// probe, and a panicking probe that never resolves wedges the target for good —
// resolveState only repairs Open→HalfOpen on a timer, never a HalfOpen circuit
// stuck at its probe cap. The panic is recorded as a failure and re-raised, not
// swallowed.
func callUnderResilience[Req, Resp any](
	ctx context.Context,
	key string,
	p providers.Provider,
	cb *circuitbreaker.CircuitBreaker,
	lim *providerLimiter,
	req Req,
	upstreamModel string,
	call targetCall[Req, Resp],
) (resp Resp, err error) {
	if cb == nil && lim == nil {
		return call(ctx, p, req, upstreamModel)
	}

	if cb != nil {
		if !cb.Allow() {
			var zero Resp
			return zero, circuitbreaker.ErrCircuitOpen
		}
		defer func() {
			if r := recover(); r != nil {
				cb.RecordFailure()
				panic(r)
			}
			recordCircuitBreakerOutcome(ctx, cb, key, err)
		}()
	}

	if lim != nil {
		if acquireErr := lim.acquire(ctx); acquireErr != nil {
			var zero Resp
			return zero, acquireErr
		}
		defer lim.release()
	}

	return call(ctx, p, req, upstreamModel)
}

// planFor pairs a strategy's candidate order with whether that strategy falls
// back. It is the single point at which a routing mode influences execution.
func (g *Gateway) planFor(model string, keys []string) targetPlan {
	g.mu.RLock()
	advance := advancesPastFailure(g.config.Strategy.Mode)
	g.mu.RUnlock()
	return targetPlan{keys: keys, model: model, advance: advance}
}

// advancesPastFailure splits the routing modes by what their leading candidate
// MEANS, which is what decides whether moving off it is a repair or a betrayal.
//
// A POOL mode picks its head from targets the operator declared
// interchangeable, for a reason that is about the pool rather than about that
// target: spread the load, take the cheapest, take the fastest, split the
// traffic. The request belongs to the pool, so when the head is dead, carrying
// it to a sibling is what the operator asked for. Answering 503 with a healthy
// target holding the same model is the one outcome none of these modes was
// configured to produce.
//
// A NAMED mode picks its head because something said that target specifically —
// `single` names it, and a `conditional` or `content-based` rule matched it.
// Serving from somebody else would demote the rule to a suggestion, so these
// stop at the head and report the failure.
//
// Only `fallback` advanced before, which read as though advancing were a
// fallback-shaped feature. It is not: it is the pool's own semantics. Without
// it a dead target with no circuit breaker — the shipped default, and 29 of the
// 30 targets in the example configs — absorbed its whole selection share and
// failed it. Under `cost-optimized`, which ranks deterministically, that was
// every single request.
//
// A breaker remains worth configuring, and does a different job: it stops the
// walk from paying the dead target's connection timeout on every request. It is
// what makes failover CHEAP; this is what makes it HAPPEN.
func advancesPastFailure(mode config.StrategyMode) bool {
	switch mode {
	case config.ModeFallback, config.ModeLoadBalance, config.ModeLatency,
		config.ModeCostOptimized, config.ModeABTest:
		return true
	default:
		// single, conditional, content-based — and anything added later, which
		// stops at its head until this switch says otherwise. That is the safe
		// default: a new mode that should advance fails a test, while one that
		// should not would silently reroute.
		return false
	}
}

// completeChat is chat's provider call. A package-level function, not a
// closure: the request rides through routeTargets as a value, so the hot path
// allocates nothing to describe the call.

func completeChat(ctx context.Context, p providers.Provider, req providers.Request, upstreamModel string) (*providers.Response, error) {
	req.Model = upstreamModel
	return p.Complete(ctx, req)
}

// routeChat runs a non-streaming chat request through the pipeline and returns
// the response together with the target that served it.
func (g *Gateway) routeChat(ctx context.Context, s strategies.Strategy, req providers.Request) (*providers.Response, routedTarget, error) {
	keys, err := s.SelectTargets(req)
	if err != nil {
		return nil, routedTarget{}, err
	}
	resp, target, err := routeTargets(ctx, g, g.planFor(req.Model, keys), req, nil, completeChat)
	if err != nil {
		return nil, target, err
	}
	if resp != nil && resp.Provider == "" {
		resp.Provider = target.key
	}
	return resp, target, nil
}

// How the remaining surfaces attach.
//
// EMBEDDINGS and IMAGES are unary and attach exactly as chat does. Each needs a
// gate and a leaf call:
//
//	func embeddingProvider(p providers.Provider) bool {
//		_, ok := providers.As[providers.EmbeddingProvider](p)
//		return ok
//	}
//	func embed(ctx context.Context, p providers.Provider, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
//		provider, _ := providers.As[providers.EmbeddingProvider](p)
//		return provider.Embed(ctx, req)
//	}
//
// then routeTargets(ctx, g, g.planFor(req.Model, keys), req, embeddingProvider, embed).
// The gate has already run when the leaf call executes, so its assertion cannot
// fail. That replaces routeEmbedding/routeImage and routeSurfaceTarget outright,
// and the surfaces inherit the four fixes here — uniform retry, a real provider
// label, a duration observation on failures, and 503/504 classification — without
// writing any of them.
//
// The payload type is why SkipProvider cannot serve these surfaces and why the
// request logger never records them: plugin.Context.Response is the chat-shaped
// providers.Response with no field able to hold an EmbeddingResponse. That is a
// PLUGIN CONTRACT question, not a pipeline one — the pipeline is already
// payload-agnostic — so it is settled in runSurfaceGovernance, which stays where
// it is, outside the walk, exactly like chat's plugin stages.
//
// STREAMING attaches at the same seam but must not pretend to be unary. Its
// leaf call returns a channel whose lifetime outlives the attempt, so:
//
//   - Resp is <-chan providers.StreamChunk, and the leaf call is
//     raceCompleteStream(attemptCtx, streamCtx, …) — the wait is bounded, the
//     call is not. Two contexts, one of them captured by the closure that
//     builds the leaf call, because only the start phase may be deadlined.
//   - The gate is the providers.StreamProvider assertion.
//   - The circuit breaker must NOT resolve when the call returns: a stream that
//     started is not yet a success. Streaming therefore passes cb == nil to the
//     pipeline and keeps resolving the outcome through streamwrap's
//     CircuitBreakerOutcome, admitting the probe itself via cbProvider as it
//     does today. raceCompleteStream's ReleaseProbe on an abandoned-but-
//     successful start is part of that ownership and stays exactly where it is.
//     Anything else strands the half-open probe and wedges the target for good.
//   - Success recording stays in streamwrap; only the START failure path is the
//     pipeline's, which is the half that is genuinely unary-shaped.
//
// That asymmetry is not a wart to design away. It is why gRPC-go's stream
// interceptors wrap a ClientStream instead of post-processing a result: a hook
// shaped like a unary return cannot observe a terminal status that arrives
// later.
