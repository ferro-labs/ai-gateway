package aigateway

import (
	"context"
	"errors"
	"runtime/trace"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

type routingAttempt struct {
	target         routedTarget
	routedModel    string
	sequence       int
	targetSequence int
}

func (g *Gateway) recordRoutingAttempt(ctx context.Context, obs observability.Provider, active bool, attempt routingAttempt, latency time.Duration, err error) {
	if !active {
		return
	}

	status := providers.ParseStatusCode(err)
	if err == nil {
		status = 200
	} else if status == 0 {
		status, _, _ = apierror.RouteErrorDetails(err)
	}
	outcome := observability.RoutingAttemptSuccess
	errorMessage := ""
	if err != nil {
		outcome = observability.RoutingAttemptError
		errorMessage = redact.ErrorMessage(err)
	}
	obs.RecordEvent(ctx, observability.Event{
		Subject:   observability.SubjectRoutingAttempt,
		TraceID:   logger.TraceIDFromContext(ctx),
		Timestamp: time.Now(),
		RoutingAttempt: &observability.RoutingAttempt{
			TargetKey:      attempt.target.key,
			Provider:       attempt.target.priceProvider,
			RoutedModel:    attempt.routedModel,
			UpstreamModel:  attempt.target.upstreamModel,
			Sequence:       attempt.sequence,
			TargetSequence: attempt.targetSequence,
			LatencyMs:      latency.Milliseconds(),
			Status:         status,
			Outcome:        outcome,
			Error:          errorMessage,
		},
		Attributes: abVariantAttributes(attempt.target.abVariantLabel, attempt.target.hasABVariant),
	})
}

func abVariantAttributes(label string, present bool) map[string]any {
	if !present {
		return nil
	}
	return map[string]any{observability.AttrFerroRoutingABVariantLabel: label}
}

func (g *Gateway) dispatchRequestEventWithABVariant(ctx context.Context, obs observability.Provider, hooksEnabled, obsEventsActive bool, he events.HookEvent, label string, present bool) {
	if hooksEnabled {
		g.publishEvent(ctx, he)
	}
	if obsEventsActive {
		event := obsEventFromHook(he)
		event.Attributes = abVariantAttributes(label, present)
		obs.RecordEvent(ctx, event)
	}
}

// recordProviderErrorCtx counts one failed provider call, and is the only place
// any unary surface increments gateway_provider_errors_total. The reason label
// comes from metrics.ProviderErrorType, the same classifier the streaming
// terminal and the circuit breaker read, so a client cancel or a saturation
// shed cannot be a provider_error here and something else there.
//
// A model no configured target can serve is skipped: routing reached its
// verdict before any provider was called, so there is no provider whose health
// the sample describes. It was landing under provider="" — a label that names
// nothing, cannot be alerted on, and made a client typo look like an outage in
// the same series a real provider failure lands in.
func recordProviderErrorCtx(ctx context.Context, provider string, err error) {
	if errors.Is(err, core.ErrNoCapableProvider) {
		return
	}
	metrics.ForProviderError(provider, metrics.ProviderErrorType(ctx, err)).Inc()
}

// routeError finalizes a failed Route call: runs plugin error hooks, records
// error metrics, stamps the span with the error, logs the failure, and
// dispatches the failed lifecycle event. Shared by the initial provider call
// and the MCP tool-call loop's follow-up provider calls so both error paths
// stay in sync.
func (g *Gateway) routeError(ctx context.Context, span observability.Span, obs observability.Provider, pctx *plugin.Context, plugins *plugin.Manager, provider, model, abVariantLabel string, hasABVariant bool, err error, latency time.Duration, originalStream, hooksEnabled, obsEventsActive bool) {
	if pctx != nil {
		pctx.Error = err
		// The one fact a failed request's record could not otherwise carry.
		// There is no response to read a provider from, so without this the
		// on_error row said only that something failed, never what.
		pctx.Target = provider
		// A failed request still took time, and how long it took to fail is
		// worth as much as how long a success took — a provider timing out at
		// thirty seconds and one refusing instantly are different incidents.
		// There is no cost: nothing was billed and no usage reached the catalog.
		pctx.Measurements = plugin.Measurements{DurationMs: float64(latency.Microseconds()) / 1000.0}
		plugins.RunOnError(ctx, pctx)
	}

	// Bucket the label, not the log/span: model here is still the raw client
	// value on the "no provider supports this model" path.
	requestMetrics := metrics.ForRequest(provider, g.metricModel(model))
	// A failure is a request the gateway served, and how long it took to fail
	// belongs in the same histogram as how long a success took. Observing only
	// successes made a provider timing out at thirty seconds invisible while
	// cache hits pulled the distribution down, so the quantiles answered "how
	// fast are the requests that worked" and were read as "how fast is the
	// gateway".
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Error.Inc()
	recordProviderErrorCtx(ctx, provider, err)

	span.SetError(err)

	g.log.Ctx(ctx).Error("request failed",
		"model", model,
		"latency_ms", latency.Milliseconds(),
		"error", redact.ErrorMessage(err),
	)

	if hooksEnabled || obsEventsActive {
		he := failedEventData(
			logger.TraceIDFromContext(ctx),
			provider,
			model,
			err,
			latency,
			originalStream,
		)
		g.dispatchRequestEventWithABVariant(ctx, obs, hooksEnabled, obsEventsActive, he, abVariantLabel, hasABVariant)
	}
}

// runAfterPlugins runs the after_request stage (logging, caching) against resp,
// handing the stage what the gateway measured about the request. It returns the
// response the plugins left behind, which may be one they substituted.
//
// A plugin failure here aborts the request: the stage is counted against the
// counter that says why — a deliberate rejection and a broken plugin are
// different faults — and the on_error stage runs before the error is returned.
func (g *Gateway) runAfterPlugins(
	ctx context.Context,
	plugins *plugin.Manager,
	pctx *plugin.Context,
	resp *providers.Response,
	target string,
	measured plugin.Measurements,
) (*providers.Response, error) {
	if pctx == nil {
		return resp, nil
	}
	pctx.Response = resp
	pctx.Target = target
	pctx.Measurements = measured

	var err error
	trace.WithRegion(ctx, "gateway.route.plugins.after", func() {
		err = plugins.RunAfter(ctx, pctx)
	})
	if err != nil {
		recordPluginAbort(metrics.ForRequest(resp.Provider, g.metricModel(resp.Model)), err)
		pctx.Error = err
		plugins.RunOnError(ctx, pctx)
		return nil, err
	}
	if pctx.Response != nil {
		return pctx.Response, nil
	}
	return resp, nil
}

// cacheServedCost is what a request no provider was contacted for cost: nothing.
//
// Known, not unknown. Pricing the served tokens instead would be pricing a call
// that never left the process — the same double count the budget plugin already
// refuses on SkipProvider — so a prompt repeated a hundred times reported a
// hundred times the spend the gateway actually incurred. ModelFound is true
// because zero here is a measurement rather than a catalog miss; recording it as
// unknown would file every cache hit under "could not be priced" and understate
// nothing while explaining nothing.
//
// The tokens are still counted. Usage happened and cost did not, and they are
// different facts: the request-log row keeps the token counts that were served
// and pairs them with a cost of zero.
//
// Priced is true for the same reason ModelFound is: zero here is a price the
// gateway KNOWS, arrived at by measurement rather than by a catalog lookup that
// came back empty. Everything that reads Priced asks "is this figure a cost or a
// blank", and for a cache hit the answer is cost.
func cacheServedCost() models.CostResult {
	return models.CostResult{ModelFound: true, Priced: true}
}

// cacheServedMeasurements is the same fact in the shape the after_request stage
// takes, measured just before that stage runs like every other duration handed
// to a plugin.
func cacheServedMeasurements(start time.Time) plugin.Measurements {
	cost := cacheServedCost()
	return plugin.Measurements{
		DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
		CostUSD:    cost.TotalUSD,
		// Priced, not ModelFound — the field that says a figure exists rather
		// than that a catalog row does. They agree here by construction; reading
		// the same one everywhere is what stops a cataloged model with no price
		// being logged as a known $0.
		HasCost: cost.Priced,
	}
}

// calculateCost prices a response against the current model catalog.
//
// Split out because the after-request plugins need the result before
// recordSuccess runs — the request logger persists it — and pricing a response
// twice risks the persisted figure and the reported one disagreeing after a
// catalog refresh lands between them.
func (g *Gateway) calculateCost(resp *providers.Response, priceProvider, fallbackModel string) models.CostResult {
	g.mu.RLock()
	catalog := g.catalog
	g.mu.RUnlock()
	model := resp.Model
	if model == "" {
		model = fallbackModel
	}
	return models.Calculate(catalog, priceProvider+"/"+model, models.Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		ReasoningTokens:  resp.Usage.ReasoningTokens,
		CacheReadTokens:  resp.Usage.CacheReadTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens,
	})
}

// recordSuccess records a request a provider served.
func (g *Gateway) recordSuccess(ctx context.Context, span observability.Span, obs observability.Provider, resp *providers.Response, routedModel string, cost models.CostResult, latency time.Duration, abVariantLabel string, hasABVariant, originalStream, hooksEnabled, obsEventsActive bool) {
	g.recordOutcome(ctx, span, obs, resp, routedModel, cost, latency, resp.Provider, abVariantLabel, hasABVariant, originalStream, hooksEnabled, obsEventsActive)
}

// recordCacheHit records a request the response cache served, which is the same
// accounting minus one thing: the per-provider throughput, success and latency
// series are labelled metrics.CacheProviderLabel, because no provider was
// called. Everything that describes the RESPONSE — the span, the log line, the
// lifecycle event, the request-log row — still names the provider that produced
// it. See metrics.CacheProviderLabel for why the two differ.
func (g *Gateway) recordCacheHit(ctx context.Context, span observability.Span, obs observability.Provider, resp *providers.Response, latency time.Duration, originalStream, hooksEnabled, obsEventsActive bool) {
	g.recordOutcome(ctx, span, obs, resp, resp.Model, cacheServedCost(), latency, metrics.CacheProviderLabel, "", false, originalStream, hooksEnabled, obsEventsActive)
}

// recordOutcome emits Prometheus + cost metrics under metricProvider, stamps the
// root span with the resolved provider/model/usage/cost, logs at debug level,
// and dispatches the completed lifecycle event.
func (g *Gateway) recordOutcome(ctx context.Context, span observability.Span, obs observability.Provider, resp *providers.Response, routedModel string, cost models.CostResult, latency time.Duration, metricProvider, abVariantLabel string, hasABVariant, originalStream, hooksEnabled, obsEventsActive bool) {
	// Bound the metric label. Providers that accept any model ID echo the
	// caller's string back on success, so an unbounded label here would let a
	// client mint a new time series per request.
	requestMetrics := metrics.ForRequest(metricProvider, g.metricModel(resp.Model))
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Success.Inc()
	requestMetrics.TokensIn.Add(float64(resp.Usage.PromptTokens))
	requestMetrics.TokensOut.Add(float64(resp.Usage.CompletionTokens))

	if cost.TotalUSD > 0 {
		requestMetrics.CostUSD.Add(cost.TotalUSD)
	}

	// Stamp final usage + cost + resolved provider/model on the root span.
	span.SetAttribute(observability.AttrGenAISystem, resp.Provider)
	span.SetAttribute(observability.AttrGenAIResponseModel, routedModel)
	// Stamp the resolved target key (virtual key = provider name in this routing layer).
	if resp.Provider != "" {
		span.SetAttribute(observability.AttrFerroRoutingTargetKey, resp.Provider)
	}
	span.SetTokens(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.ReasoningTokens)
	span.SetCost(observability.CostBreakdown{
		TotalUSD:      cost.TotalUSD,
		InputUSD:      cost.InputUSD,
		OutputUSD:     cost.OutputUSD,
		CacheReadUSD:  cost.CacheReadUSD,
		CacheWriteUSD: cost.CacheWriteUSD,
		ReasoningUSD:  cost.ReasoningUSD,
		ModelFound:    cost.ModelFound,
	})

	if g.log.Enabled(ctx, logger.LevelDebug) {
		g.log.Ctx(ctx).Debug("request completed",
			"model", resp.Model,
			"provider", resp.Provider,
			"latency_ms", latency.Milliseconds(),
			"tokens_in", resp.Usage.PromptTokens,
			"tokens_out", resp.Usage.CompletionTokens,
			"cost_usd", cost.TotalUSD,
		)
	}

	if hooksEnabled || obsEventsActive {
		he := completedEventData(
			logger.TraceIDFromContext(ctx),
			resp.Provider,
			routedModel,
			latency,
			originalStream,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			cost,
		)
		g.dispatchRequestEventWithABVariant(ctx, obs, hooksEnabled, obsEventsActive, he, abVariantLabel, hasABVariant)
	}
}
