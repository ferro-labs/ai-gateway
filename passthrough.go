package aigateway

import (
	"context"
	"time"

	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// surfacePassthrough names the /v1/* pass-through in spans, metrics labels and
// plugin Metadata["surface"], alongside surfaceEmbeddings and surfaceImages.
const surfacePassthrough = "passthrough"

// ErrPassthroughUninspectable is the refusal a pass-through request gets when a
// content guardrail is configured and the request body cannot be projected into
// anything that guardrail could read — a multipart upload, an audio payload, a
// body past the projection cap, or one that is not JSON at all.
//
// It is a REFUSAL rather than a silent pass because a guardrail that cannot see
// a body has not approved it. Failing open here is what made the pass-through a
// hole: the six natively-handled endpoints screened their prompts and every
// other /v1/ path forwarded whatever it was given, under the gateway's own
// provider credential. The same rule is Envoy's ext_authz — whose 413 on an
// oversized body explicitly overrides failure_mode_allow — and OPA's, which
// returns a denial rather than an error precisely so a fail-open cannot rescue
// it.
//
// It is a plugin.RejectionError because that is what it IS — a policy denial
// made before the request reached a provider, whose reason is written to be read
// by the caller — and because being one gives it the treatment a denial should
// get everywhere downstream without a second vocabulary: 400 with its reason
// intact from internal/apierror, and counted as a rejection rather than an error
// by the metrics (see recordPluginAbort), so a deployment refusing binary
// uploads does not read as a provider outage.
//
// It is the one such error the plugin manager does not mint. Plugin names it as
// the gateway rather than as a plugin, because no plugin made this call: the
// deployment's guardrail policy did, in the one place a guardrail could not be
// asked.
var ErrPassthroughUninspectable error = &plugin.RejectionError{
	Plugin:     "gateway",
	PluginType: plugin.TypeGuardrail,
	Stage:      plugin.StageBeforeRequest,
	Reason:     "request body cannot be inspected by the configured content guardrail; this deployment does not forward uninspectable bodies",
}

// RoutePassthrough runs the gateway request lifecycle around one /v1/*
// pass-through forward, so a forwarded endpoint is governed by the same rules a
// natively-handled one is.
//
// It exists because there must not be two classes of /v1/ route. The wildcard
// pass-through is worth keeping — OpenAI's API is far larger than the endpoints
// served natively, and /v1/fine_tuning, /v1/images/edits, /v1/vector_stores and
// /v1/realtime are forwarded rather than each being reimplemented — but a route
// that skips admission,
// plugins, the circuit breaker, the concurrency limit and the request log is a
// route with different governance, and the endpoint a caller picks then decides
// which policy applies to them.
//
// target is the virtual key of the target that will receive the forward; the
// caller has already resolved it and checked it against the targets allowlist.
// model is the model the body named, empty when it named none — most of the
// endpoints this surface exists for carry no model at all.
//
// body is a best-effort text projection of the request body, and
// bodyInspectable reports whether that projection is faithful. When it is
// false and a before_request guardrail is configured, the request is refused
// with ErrPassthroughUninspectable before any plugin runs and before anything
// is forwarded. See projectBody in internal/proxy for what a projection covers.
//
// forward performs the actual HTTP forward and reports whether it succeeded. It
// writes the upstream response straight to the client, which bounds what the
// stages after it can do — see below.
//
// What applies, and what deliberately does not:
//
//   - before_request, after_request and on_error plugins: yes, with
//     Metadata["surface"] = "passthrough" so a plugin can branch on it.
//   - circuit breaker and per-target concurrency: yes, via the same
//     callUnderResilience the routed pipeline uses.
//   - request log and cost accounting: yes. Cost is recorded as UNPRICED, never
//     as a known zero: the response is opaque by construction, and buffering an
//     arbitrarily large, possibly streaming, possibly binary body to hunt for a
//     usage object would break the very endpoints this surface exists for.
//     unpriced is the honest answer; parse usage per-endpoint only if
//     pass-through billing is ever actually needed.
//   - retry: NO, deliberately, and excluded here rather than by omission. The
//     request body has already been streamed upstream by the time a failure is
//     known, so there is nothing left to replay, and the endpoints behind this
//     surface are not idempotent — a retried /v1/files upload is a second file
//     and a retried /v1/batches submission is a second batch.
//   - admission (admitModel): NO. The caller resolved the provider itself, from
//     the X-Provider header or from the routing index, and gated it on target
//     membership. Re-asking "does any target serve this model" would refuse
//     every model-less endpoint and would close the documented X-Provider
//     escape hatch for a model no index can enumerate.
//
// Known ceiling: forward writes the upstream response to the client as it
// arrives, so an after_request plugin that REJECTS cannot unsend those bytes.
// The stage still runs and its verdict is still recorded and returned, which is
// what puts the request in the log; it just cannot retract a delivered
// response. Streaming chat has the same ceiling for the same reason. Screening
// what a caller SENDS is before_request's job, and that stage runs with nothing
// yet written.
func (g *Gateway) RoutePassthrough(ctx context.Context, target, model, body string, bodyInspectable bool, forward func(context.Context) error) error {
	start := time.Now()
	hooksEnabled := g.hasHooks()

	g.mu.RLock()
	requestTimeout := g.config.RequestTimeout
	strategyMode := string(g.config.Strategy.Mode)
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	g.mu.RUnlock()

	// The per-request deadline applies for the same reason it applies to chat:
	// it bounds the whole request, not the provider call. A streamed
	// pass-through inherits the same exemption streaming chat has only in that
	// RequestTimeout is usually unset; an operator who sets it means it.
	ctx, cancelDeadline := withRequestDeadline(ctx, requestTimeout)
	defer cancelDeadline()

	// See Route: this is an exported entry point, so it seeds its own trace ID
	// rather than assuming HTTP middleware ran above it.
	ctx = logger.EnsureTraceID(ctx)
	ctx, identity := requestIdentity(ctx, "")
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfacePassthrough,
		RequestModel:    model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
		User:            identity.User,
		SessionID:       identity.SessionID,
		Metadata:        identity.Metadata,
	})
	defer span.End()

	record, err := g.runPassthroughGovernance(ctx, span, start, passthroughParams{target: target, model: model, body: body, bodyInspectable: bodyInspectable}, forward)
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		g.log.Ctx(ctx).Error("pass-through request failed", "target", target, "model", model, "error", safeErr)
		return err
	}
	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)
	return nil
}

// RouteResponses governs a POST /v1/responses forward exactly as RoutePassthrough
// does, and additionally PRICES it: the Responses API returns a usage object (on
// the JSON body, or on the terminal streaming event), and the forward captures it
// into usage before this returns, so cost stops being unknown for this one
// pass-through endpoint. maxOutputTokens, when non-zero, is surfaced to the
// before_request stage as the request's completion ceiling so a max-token
// guardrail governs Responses the way it governs chat (the field is renamed
// max_output_tokens there, so the guardrail cannot read it off the raw body).
//
// It is a thin wrapper over the shared governance rather than a second pipeline:
// the only differences from RoutePassthrough are the two governance inputs it
// threads through, both of which RoutePassthrough passes as zero.
func (g *Gateway) RouteResponses(ctx context.Context, target, model, body string, bodyInspectable bool, maxOutputTokens int, usage *providers.Usage, forward func(context.Context) error) error {
	return g.RouteResponsesWithPricingProvider(ctx, target, g.pricingProviderFor(target), model, body, bodyInspectable, maxOutputTokens, usage, forward)
}

// RouteResponsesWithPricingProvider governs a Responses forward using the
// canonical pricing identity captured when the proxy selected its provider.
func (g *Gateway) RouteResponsesWithPricingProvider(ctx context.Context, target, priceProvider, model, body string, bodyInspectable bool, maxOutputTokens int, usage *providers.Usage, forward func(context.Context) error) error {
	start := time.Now()
	hooksEnabled := g.hasHooks()

	g.mu.RLock()
	requestTimeout := g.config.RequestTimeout
	strategyMode := string(g.config.Strategy.Mode)
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	g.mu.RUnlock()

	ctx, cancelDeadline := withRequestDeadline(ctx, requestTimeout)
	defer cancelDeadline()

	ctx = logger.EnsureTraceID(ctx)
	ctx, identity := requestIdentity(ctx, "")
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfaceResponses,
		RequestModel:    model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
		User:            identity.User,
		SessionID:       identity.SessionID,
		Metadata:        identity.Metadata,
	})
	defer span.End()

	record, err := g.runPassthroughGovernance(ctx, span, start, passthroughParams{
		target: target, priceProvider: priceProvider, model: model, body: body, bodyInspectable: bodyInspectable,
		maxOutputTokens: maxOutputTokens, usage: usage,
	}, forward)
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		g.log.Ctx(ctx).Error("responses request failed", "target", target, "model", model, "error", safeErr)
		return err
	}
	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)
	return nil
}

// surfaceResponses names the /v1/responses surface in spans and metrics.
const surfaceResponses = "responses"

// passthroughParams carries the inputs to runPassthroughGovernance. The last two
// are set only by RouteResponses: usage is priced into the record when the
// forward captured a non-zero one, and maxOutputTokens becomes the plugin
// request's completion ceiling. The generic pass-through leaves both zero.
type passthroughParams struct {
	target, priceProvider, model, body string
	bodyInspectable                    bool
	maxOutputTokens                    int
	usage                              *providers.Usage
}

// runPassthroughGovernance is the pass-through's plugin stage sequence. It is
// deliberately the same shape as runSurfaceGovernance and uses the same frozen
// plugin.Manager/Context seam rather than a lifecycle of its own; it is a
// separate function only because this surface resolves its target before it is
// called and therefore must not run admitModel (see RoutePassthrough).
func (g *Gateway) runPassthroughGovernance(
	ctx context.Context,
	span observability.Span,
	start time.Time,
	p passthroughParams,
	forward func(context.Context) error,
) (surfaceRecord, error) {
	target, model, body, bodyInspectable := p.target, p.model, p.body, p.bodyInspectable
	g.mu.RLock()
	plugins := g.plugins
	release := acquirePluginManager(plugins)
	g.mu.RUnlock()
	defer release()

	call := func(ctx context.Context) (surfaceRecord, error) {
		// The record names the target on both paths: on success it is who
		// answered, on failure who was asked. A pass-through failure that could
		// not say which provider failed was the same blind spot the routed
		// surfaces had.
		record := surfaceRecord{provider: target, routedModel: model}
		err := g.forwardUnderResilience(ctx, target, forward)
		// Responses (RouteResponses) captures a real usage object off the forwarded
		// body/stream, so this one pass-through endpoint IS priced on success —
		// catalog pricing, the request-log cost column, the span cost and the
		// completed event all light up. Every other pass-through leaves usage nil
		// and stays unpriced. Priced here, inside call, so both the plugin and the
		// no-plugin path (which returns call directly) get it.
		if err == nil && p.usage != nil && (p.usage.PromptTokens > 0 || p.usage.CompletionTokens > 0 || p.usage.TotalTokens > 0) {
			record = g.priceSurface(routedTarget{key: target, priceProvider: p.priceProvider, upstreamModel: model}, model, *p.usage, 0)
		}
		return record, err
	}

	if !plugins.HasPlugins() {
		return call(ctx)
	}

	// The pass-through request in the only request shape the plugin seam can
	// hold. Messages carries the projection so a content guardrail has the same
	// thing to read here that it reads on chat; a body that projected to nothing
	// leaves Messages empty, which is what a guardrail already sees for a
	// request with no prose in it.
	req := providers.Request{Model: model}
	if body != "" {
		req.Messages = []providers.Message{{Role: "user", Content: body}}
	}
	// Responses renames chat's max_tokens to max_output_tokens, so a max-token
	// guardrail cannot read the ceiling off this projected request unless it is
	// surfaced here. The generic pass-through leaves it zero.
	if p.maxOutputTokens > 0 {
		n := p.maxOutputTokens
		req.MaxTokens = &n
	}
	pctx := g.newPluginContext(ctx, plugins, span, &req)
	defer plugin.PutContext(pctx)
	pctx.Metadata["surface"] = surfacePassthrough

	// Fail closed BEFORE the stage runs, not inside it. A guardrail handed an
	// empty projection would approve the request — it has nothing to match on —
	// and that approval is exactly the silent pass this refusal exists to
	// prevent. Refusing here also means the request never reaches the limiter or
	// the upstream. on_error still runs, so it is recorded like any other denial.
	//
	// The routed surfaces answer the same question the other way round: they set
	// plugin.MetadataUninspectableContent and let each guardrail return its own
	// verdict. That delegation is right where a guardrail has been asked and can
	// answer — an embeddings input given as token IDs still runs the stage. It
	// is not available here, for two reasons. A guardrail that does not read the
	// key fails OPEN, and this is the surface where failing open forwards the
	// body upstream under the gateway's own credential; and running the stage to
	// reach a refusal spends a rate limiter's tokens and a budget's money on a
	// request that was never going to be served, which is the cost admitModel
	// exists to avoid.
	//
	// A guardrail that declares plugin.ContentAgnostic is not counted here.
	// max-token reads no request content unless max_input_length is set, so a
	// deployment running only that no longer has uninspectable bodies refused —
	// and the declaration is answered per instance rather than per type, because
	// the same plugin does read content once that limit is configured.
	//
	// A guardrail that declares nothing still triggers the refusal. That polarity
	// is deliberate: forwarding an unscreened body upstream under the gateway's
	// own credential is not recoverable, and a guardrail built outside this
	// repository cannot know to declare anything.
	if !bodyInspectable && plugins.HasBeforeRequestGuardrail() {
		pctx.Error = ErrPassthroughUninspectable
		pctx.Measurements = plugin.Measurements{DurationMs: elapsedMs(start)}
		plugins.RunOnError(ctx, pctx)
		return surfaceRecord{}, ErrPassthroughUninspectable
	}

	if err := plugins.RunBefore(ctx, pctx); err != nil {
		return surfaceRecord{}, err
	}
	// Whatever the before stage left in the two provider-substitution fields is
	// dropped, for the same reason the non-chat surfaces drop it: a plugin
	// setting SkipProvider is offering a providers.Response in place of the
	// upstream call, and there is no /v1/files listing or audio stream inside
	// one. Reject is the supported way to stop a request on this surface.
	pctx.SkipProvider = false
	pctx.Response = nil

	record, err := call(ctx)
	pctx.Target = record.provider
	if err != nil {
		pctx.Error = err
		pctx.Measurements = plugin.Measurements{DurationMs: elapsedMs(start)}
		plugins.RunOnError(ctx, pctx)
		return record, err
	}

	pctx.Response = record.pluginView()
	// HasCost stays false for an unpriced pass-through: see RoutePassthrough on why
	// it is unpriced rather than free. A plugin that persists this keeps the
	// distinction, so a request-log row reads "cost unknown", not "cost 0".
	pctx.Measurements = plugin.Measurements{DurationMs: elapsedMs(start)}
	pctx.Metadata["completed"] = true
	if err := plugins.RunAfter(ctx, pctx); err != nil {
		pctx.Error = err
		plugins.RunOnError(ctx, pctx)
		return record, err
	}
	return record, nil
}

// forwardUnderResilience runs the forward under the target's circuit breaker
// and concurrency limiter — the same pair, in the same order (breaker
// outermost, limiter innermost), that every routed provider call runs under.
//
// It reuses callUnderResilience rather than reimplementing the pair. The
// generic parameters are unused here because a pass-through has no typed
// request or response to carry: the bytes go straight from the client to the
// upstream and back.
func (g *Gateway) forwardUnderResilience(ctx context.Context, key string, forward func(context.Context) error) error {
	g.mu.RLock()
	cb := g.circuitBreakers[key]
	lim := g.limiters[key]
	g.mu.RUnlock()

	_, err := callUnderResilience(ctx, key, nil, cb, lim, struct{}{}, "",
		func(ctx context.Context, _ providers.Provider, _ struct{}, _ string) (struct{}, error) {
			return struct{}{}, forward(ctx)
		})
	return err
}
