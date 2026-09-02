package aigateway

import (
	"context"
	"errors"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// The non-chat surfaces this file routes. The name reaches operators as the
// span's operation and as a metrics label, so it is fixed here rather than
// spelled out at each of its call sites.
const (
	surfaceEmbeddings    = "embeddings"
	surfaceImages        = "images"
	surfaceRerank        = "rerank"
	surfaceModeration    = "moderations"
	surfaceTranscription = "audio.transcriptions"
	surfaceSpeech        = "audio.speech"
)

// surfaceRecord is one completed embeddings/images request: which target served
// it, under which model, and what it consumed. The payload never appears here —
// nothing downstream of the provider call needs the vectors or the image URLs,
// and keeping them out is what lets one recording path serve both surfaces.
//
// This is the shape LiteLLM's StandardLoggingPayload and Portkey's LogObject
// both settled on: a normalized record of what happened, assembled per surface,
// rather than a union type wide enough to hold every surface's response.
type surfaceRecord struct {
	provider       string
	routedModel    string
	abVariantLabel string
	hasABVariant   bool
	// tokens is the chat-shaped token view: real for embeddings, zero for image
	// generation, which reports no usage at all.
	tokens providers.Usage
	// imageCount is what an image request is priced by, since it has no tokens.
	imageCount int
	cost       models.CostResult
}

// billable is the record in the shape the cost calculator prices.
func (r surfaceRecord) billable() models.Usage {
	return models.Usage{
		PromptTokens:     r.tokens.PromptTokens,
		CompletionTokens: r.tokens.CompletionTokens,
		ImageCount:       r.imageCount,
	}
}

// pluginView projects the record onto providers.Response, the only response
// shape plugin.Context can hold.
//
// It is not a pretend embedding: Choices is empty because there are no choices,
// and every field that is set — provider, model, token usage — is a fact about
// the request that just completed. That is exactly the set a request-log row
// records, and handing it over is what finally puts these two surfaces in the
// trail. Before this, plugin.Context.Response was nil on every embeddings and
// images success, so the request logger's after-request branch never fired and
// only their FAILURES were ever written — which made any error rate derived
// from request_logs report both surfaces as 100% failure.
func (r surfaceRecord) pluginView() *providers.Response {
	return &providers.Response{
		Model:    r.routedModel,
		Provider: r.provider,
		Usage:    r.tokens,
	}
}

// priceSurface completes a record with the cost of what the provider returned.
//
// Priced once, here, so the figure the request logger persists and the figure
// the metrics and the span report are the same number — pricing twice is how
// they come to disagree when a catalog refresh lands between the two calls.
// Chat splits calculateCost out of recordSuccess for the same reason.
func (g *Gateway) priceSurface(target routedTarget, routedModel string, tokens providers.Usage, imageCount int) surfaceRecord {
	record := surfaceRecord{provider: target.key, routedModel: routedModel, abVariantLabel: target.abVariantLabel, hasABVariant: target.hasABVariant, tokens: tokens, imageCount: imageCount}
	g.mu.RLock()
	catalog := g.catalog
	g.mu.RUnlock()
	record.cost = models.Calculate(catalog, target.priceProvider+"/"+target.upstreamModel, record.billable())
	return record
}

// surfaceMessages projects a non-chat surface's content — an embeddings input,
// an image prompt — onto the chat-shaped message list plugin.Context carries, so
// a content guardrail screens it exactly as it screens a prompt.
//
// inspectable is false when content is PRESENT but carries no readable text: an
// embeddings input given as token IDs. That is not a different modality — it is
// the same text under a lossless encoding, so a blocklist that waved it through
// would be evaded by one client-side tokenizer call. The gateway only reports
// the fact; plugin.MetadataUninspectableContent says who decides.
//
// Absent and empty content is inspectable-and-empty rather than uninspectable:
// there is nothing there for a blocked word to hide in, and it is refused on its
// own terms elsewhere (the handler rejects a nil input, the provider an empty
// array). core.CoerceEmbeddingInput is not reused here for exactly that reason —
// it is a validator, and folds "empty" in with "not text", which would answer a
// content-policy refusal to a caller whose mistake was sending [].
func surfaceMessages(content any) (msgs []providers.Message, inspectable bool) {
	switch v := content.(type) {
	case nil:
		return nil, true
	case string:
		return []providers.Message{{Role: roleUser, Content: v}}, true
	case []string:
		return textMessages(v), true
	case []any:
		// The shape a JSON array decodes to, which is what every request over
		// HTTP actually carries.
		texts := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			texts = append(texts, s)
		}
		return textMessages(texts), true
	default:
		return nil, false
	}
}

func textMessages(texts []string) []providers.Message {
	if len(texts) == 0 {
		return nil
	}
	msgs := make([]providers.Message, len(texts))
	for i, text := range texts {
		msgs[i] = providers.Message{Role: roleUser, Content: text}
	}
	return msgs
}

// elapsedMs is the millisecond duration plugins are handed, matching the
// resolution chat reports through plugin.Measurements.
func elapsedMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}

// runSurfaceGovernance runs the before/after/error plugin pipeline around a
// non-chat surface (embeddings, images) so per-key governance plugins — budget,
// rate-limit — and per-request observability plugins — the request logger —
// apply uniformly across every OpenAI surface, not just chat.
//
// It reuses the frozen plugin.Manager/Context without inventing a new lifecycle
// (plugins architecture §2). The Context is chat-shaped, and reconciling that
// with a surface whose payload it cannot hold is settled HERE, outside the
// routing walk, because it is a plugin-contract question rather than a pipeline
// one: what crosses the seam is a normalized record of the request (model,
// provider, token usage, duration, cost), never the embedding or the image.
// call performs the routed provider request and returns that record.
//
// span, when non-nil, is the surface's request root span; plugin invocations
// open per-plugin child spans through it, same as chat's runBeforePlugins.
//
// content is what the surface asks the provider to work on — an embeddings
// input, an image prompt — projected into the request the plugins see; call is
// handed the model the stage settled on. On failure the record is returned
// alongside the error so the caller can still label metrics with the target that
// failed.
func (g *Gateway) runSurfaceGovernance(ctx context.Context, surface string, span observability.Span, start time.Time, model string, content any, call func(context.Context, string) (surfaceRecord, error)) (surfaceRecord, error) {
	g.mu.RLock()
	plugins := g.plugins
	release := acquirePluginManager(plugins)
	g.mu.RUnlock()
	defer release()

	if !plugins.HasPlugins() {
		return call(ctx, model)
	}

	// The surface's request in the only request shape the seam can hold: the
	// model, and the content projected onto Messages.
	//
	// The model is what a request-log row needs to identify what was asked for —
	// on the error path it was the difference between a row saying "embeddings
	// request for text-embedding-3-small failed" and one saying nothing at all.
	// The content is what a guardrail needs to do its job: carrying the model
	// alone let a word filter inspect an empty message list, find nothing, and
	// pass blocked text through to the provider under a 200, on the two surfaces
	// an operator has no way to tell apart from chat in config. Projecting it is
	// what /v1/completions already does with a prompt, for the same reason — a
	// single user message is what a prompt IS once it reaches this shape.
	messages, inspectable := surfaceMessages(content)
	surfaceReq := providers.Request{Model: model, Messages: messages}
	// Never nil: the no-plugins case returned above.
	pctx := g.newPluginContext(ctx, plugins, span, &surfaceReq)
	defer plugin.PutContext(pctx)
	pctx.Metadata[plugin.MetadataSurface] = surface
	if !inspectable {
		// The gateway states the fact; a content guardrail decides what to do
		// about it. See plugin.MetadataUninspectableContent.
		pctx.Metadata[plugin.MetadataUninspectableContent] = true
	}

	// Admission before the plugin stage, so a model no target serves on this
	// surface cannot spend a rate-limit token or a budget on the way to its 404.
	// The on_error stage still runs, so the request is recorded. It stands down
	// when a transform plugin may still rewrite the model. See admitModel.
	if err := g.admitModel(ctx, plugins, model, surfaceGate(surface)); err != nil {
		pctx.Error = err
		pctx.Measurements = plugin.Measurements{DurationMs: elapsedMs(start)}
		plugins.RunOnError(ctx, pctx)
		return surfaceRecord{}, err
	}

	if err := plugins.RunBefore(ctx, pctx); err != nil {
		return surfaceRecord{}, err
	}
	// The model the stage settled on, which is not necessarily the one the caller
	// sent: Execute may mutate the request it is handed, and a transform is the
	// plugin type whose job that is. Admission already stands down for a
	// transform (see admitModel), so a request naming an alias reached the stage,
	// had it rewritten here, and was then routed under the ORIGINAL name anyway —
	// a 404 for a rewrite that had already happened. Chat reads the model back
	// from the same field; these surfaces did not.
	//
	// Only the model. A transform that rewrites Messages changes nothing on these
	// surfaces: the projection is one-way, and turning an edited message list back
	// into an embeddings input would have to guess whether the caller sent a
	// string or an array. Documented ceiling, not an oversight.
	model = surfaceReq.Model
	// Whatever the before stage left in the two provider-substitution fields is
	// dropped, and the provider is called regardless. A plugin that sets
	// SkipProvider is offering a providers.Response in place of the upstream
	// call, and there is no embedding or image inside one — so there is nothing
	// here to serve. Leaving the pair set would then be read downstream as "this
	// request was served from cache": budget bills nothing for a SkipProvider
	// request, so an embeddings call that did reach a provider would go
	// uncharged. Reject is the supported way to stop a request on this surface.
	//
	// A cache must therefore stand down on these surfaces, and the response cache
	// does, keyed on Metadata[plugin.MetadataSurface]. That is not an
	// optimisation: now that the content above is projected onto Messages, an
	// embeddings request for "hello" hashes exactly as a chat request whose one
	// message is "hello", so an entry stored here — the projection below, which
	// has zero Choices — would be served to the next chat completion asking that
	// question. The entry used to be inert only because the projection was empty,
	// which no chat request can produce.
	pctx.SkipProvider = false
	pctx.Response = nil

	record, err := call(ctx, model)
	// Set on both paths from the same field: on success it is the target that
	// answered, on failure the last one attempted. These two surfaces had the
	// same blind spot chat did — a failed embeddings request left a row that
	// could not say which provider failed.
	pctx.Target = record.provider
	if err != nil {
		pctx.Error = err
		// A failed request took time too, and how long it took to fail is worth
		// as much as how long a success took — a provider timing out at thirty
		// seconds and one refusing instantly are different incidents. It has no
		// cost: nothing was billed. Chat's routeError hands the on_error stage
		// the same measurement; these surfaces handed it none, which is why their
		// error rows carried a NULL duration.
		pctx.Measurements = plugin.Measurements{DurationMs: elapsedMs(start)}
		plugins.RunOnError(ctx, pctx)
		return record, err
	}

	pctx.Response = record.pluginView()
	pctx.Measurements = plugin.Measurements{
		DurationMs: elapsedMs(start),
		CostUSD:    record.cost.TotalUSD,
		HasCost:    record.cost.Priced,
	}
	// Kept alongside Response: budget reads Response.Usage first and
	// Metadata["usage"] second, and this additive channel is documented as the
	// way a non-chat surface reports usage, so a plugin written against it keeps
	// working now that the projection exists.
	pctx.Metadata["usage"] = record.tokens
	// Explicit after_request signal so stage detection does not depend on token
	// usage — image responses carry none. See budget.requestCompleted.
	pctx.Metadata["completed"] = true
	if err := plugins.RunAfter(ctx, pctx); err != nil {
		// Same order as chat's runAfterPlugins: a plugin that rejects or breaks
		// after the provider answered still reaches the on_error stage, so the
		// request is on record either way.
		pctx.Error = err
		plugins.RunOnError(ctx, pctx)
		return record, err
	}
	return record, nil
}

// Embed routes an embedding request across configured, capable targets using
// the gateway strategy (honouring strategy.mode, retry, and fallback — see
// surfaceTargetOrder and routeTargets), under the shared governance pipeline.
// It emits the same class of observability signal as chat's Route: a request
// span, Prometheus request metrics, cost accounting, and a completed/failed
// lifecycle event.
func (g *Gateway) Embed(ctx context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	log := g.log.Ctx(ctx)
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

	// See Route: exported entry point, so it seeds its own trace ID rather than
	// assuming HTTP middleware ran above it.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfaceEmbeddings,
		RequestModel:    req.Model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so embedding endpoints honour the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	var resp *providers.EmbeddingResponse
	record, err := g.runSurfaceGovernance(ctx, surfaceEmbeddings, span, start, req.Model, req.Input, func(ctx context.Context, model string) (surfaceRecord, error) {
		req.Model = model
		embedded, target, routeErr := g.routeEmbedding(ctx, req)
		if routeErr != nil {
			// target still names the last target attempted, which is what a
			// per-provider error series needs to be worth anything.
			return surfaceRecord{provider: target.key, routedModel: req.Model, abVariantLabel: target.abVariantLabel, hasABVariant: target.hasABVariant}, routeErr
		}
		resp = embedded
		embedded.Model = req.Model
		return g.priceSurface(target, req.Model, providers.Usage{
			PromptTokens: embedded.Usage.PromptTokens,
			TotalTokens:  embedded.Usage.TotalTokens,
		}, 0), nil
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, req.Model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		log.Error("embedding request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)

	log.Info("embedding request completed", "model", resp.Model, "tokens", resp.Usage.TotalTokens)
	return resp, nil
}

// GenerateImage routes an image request across configured, capable targets
// using the gateway strategy (see surfaceTargetOrder), under the shared
// governance pipeline. Cost accounting comes from models.Calculate, against the
// returned image count for a per-tile-priced model and against the provider's
// reported token usage for one the catalog prices per token (the gpt-image
// family). A provider that reports no usage for a token-priced model leaves the
// request unpriced rather than costed at zero, so the budget plugin gates but
// does not cost it — which is every image provider other than OpenAI and Azure
// OpenAI.
func (g *Gateway) GenerateImage(ctx context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	log := g.log.Ctx(ctx)
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

	// See Route: exported entry point, so it seeds its own trace ID rather than
	// assuming HTTP middleware ran above it.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       "images.generate",
		RequestModel:    req.Model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so image endpoints honour the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	var resp *providers.ImageResponse
	record, err := g.runSurfaceGovernance(ctx, surfaceImages, span, start, req.Model, req.Prompt, func(ctx context.Context, model string) (surfaceRecord, error) {
		req.Model = model
		generated, target, routeErr := g.routeImage(ctx, req)
		if routeErr != nil {
			return surfaceRecord{provider: target.key, routedModel: req.Model, abVariantLabel: target.abVariantLabel, hasABVariant: target.hasABVariant}, routeErr
		}
		resp = generated
		// The provider's own token counts, not an empty literal: the gpt-image
		// family carries no per-tile catalog price and is billed per token, so
		// discarding them here priced every such generation at nothing. A
		// provider that reports none leaves this zero and its request stays
		// unpriced, exactly as before.
		return g.priceSurface(target, req.Model, generated.Usage, len(generated.Data)), nil
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, req.Model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		log.Error("image generation request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)

	log.Info("image generation request completed", "model", req.Model, "images", len(resp.Data))
	return resp, nil
}

// Rerank routes a rerank request across configured, capable targets using the
// gateway strategy, under the shared governance pipeline — the same signals as
// Embed: a request span, request metrics, cost accounting (unpriced until the
// catalog carries rerank pricing), and a completed/failed lifecycle event.
func (g *Gateway) Rerank(ctx context.Context, req providers.RerankRequest) (*providers.RerankResponse, error) {
	log := g.log.Ctx(ctx)
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

	// See Route: exported entry point, so it seeds its own trace ID rather than
	// assuming HTTP middleware ran above it.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfaceRerank,
		RequestModel:    req.Model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so rerank honours the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	// A content guardrail screens the query and every document, exactly as it
	// screens an embeddings input — the query and documents are the request's
	// inspectable content.
	content := make([]string, 0, len(req.Documents)+1)
	content = append(content, req.Query)
	content = append(content, req.Documents...)

	var resp *providers.RerankResponse
	record, err := g.runSurfaceGovernance(ctx, surfaceRerank, span, start, req.Model, content, func(ctx context.Context, model string) (surfaceRecord, error) {
		req.Model = model
		reranked, target, routeErr := g.routeRerank(ctx, req)
		if routeErr != nil {
			// target still names the last target attempted, which a per-provider
			// error series needs to be worth anything.
			return surfaceRecord{provider: target.key, routedModel: req.Model, abVariantLabel: target.abVariantLabel, hasABVariant: target.hasABVariant}, routeErr
		}
		resp = reranked
		reranked.Model = req.Model
		// Rerank bills in search-units, which the catalog does not yet price, so
		// the request is left unpriced (zero tokens) rather than costed wrongly —
		// the same graceful-zero path an unpriced image model takes.
		return g.priceSurface(target, req.Model, providers.Usage{}, 0), nil
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, req.Model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		log.Error("rerank request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)

	log.Info("rerank request completed", "model", req.Model, "results", len(resp.Results))
	return resp, nil
}

// Moderate routes a moderation request across configured, capable targets using
// the gateway strategy, under the shared governance pipeline — the same signals
// as Embed. Moderation is unpriced (OpenAI's classifier is free); cost stays
// zero.
func (g *Gateway) Moderate(ctx context.Context, req providers.ModerationRequest) (*providers.ModerationResponse, error) {
	log := g.log.Ctx(ctx)
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

	// See Route: exported entry point, so it seeds its own trace ID rather than
	// assuming HTTP middleware ran above it.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfaceModeration,
		RequestModel:    req.Model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so moderation honours the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	var resp *providers.ModerationResponse
	record, err := g.runSurfaceGovernance(ctx, surfaceModeration, span, start, req.Model, req.Input, func(ctx context.Context, model string) (surfaceRecord, error) {
		req.Model = model
		moderated, target, routeErr := g.routeModeration(ctx, req)
		if routeErr != nil {
			return surfaceRecord{provider: target.key, routedModel: req.Model, abVariantLabel: target.abVariantLabel, hasABVariant: target.hasABVariant}, routeErr
		}
		resp = moderated
		moderated.Model = req.Model
		return g.priceSurface(target, req.Model, providers.Usage{}, 0), nil
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, req.Model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		log.Error("moderation request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)

	log.Info("moderation request completed", "model", req.Model, "results", len(resp.Results))
	return resp, nil
}

// Transcribe routes an audio transcription (or translation) request across
// configured, capable targets under the shared governance pipeline — the same
// signals as Embed. The audio bytes are opaque to a content guardrail; only the
// optional text prompt is projected for inspection. Transcription is unpriced.
//
//nolint:dupl // the non-chat surface methods are intentionally parallel to Embed/Moderate; a generic helper would obscure per-surface differences (content projection, span operation, logging)
func (g *Gateway) Transcribe(ctx context.Context, req providers.TranscriptionRequest) (*providers.TranscriptionResponse, error) {
	log := g.log.Ctx(ctx)
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

	// See Route: exported entry point, so it seeds its own trace ID rather than
	// assuming HTTP middleware ran above it.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfaceTranscription,
		RequestModel:    req.Model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so audio honours the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	var resp *providers.TranscriptionResponse
	record, err := g.runSurfaceGovernance(ctx, surfaceTranscription, span, start, req.Model, req.Prompt, func(ctx context.Context, model string) (surfaceRecord, error) {
		req.Model = model
		transcribed, target, routeErr := g.routeTranscription(ctx, req)
		if routeErr != nil {
			return surfaceRecord{provider: target.key, routedModel: req.Model, abVariantLabel: target.abVariantLabel, hasABVariant: target.hasABVariant}, routeErr
		}
		resp = transcribed
		return g.priceSurface(target, req.Model, providers.Usage{}, 0), nil
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, req.Model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		log.Error("transcription request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)

	log.Info("transcription request completed", "model", req.Model, "text_bytes", len(resp.Text))
	return resp, nil
}

// Speech routes a text-to-speech request across configured, capable targets
// under the shared governance pipeline — the same signals as Embed. The input
// text is projected for a content guardrail (unlike audio bytes, it is
// inspectable). Synthesis is unpriced.
//
//nolint:dupl // the non-chat surface methods are intentionally parallel to Embed/Moderate/Transcribe; a generic helper would obscure per-surface differences (content projection, span operation, logging)
func (g *Gateway) Speech(ctx context.Context, req providers.SpeechRequest) (*providers.SpeechResponse, error) {
	log := g.log.Ctx(ctx)
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

	// See Route: exported entry point, so it seeds its own trace ID rather than
	// assuming HTTP middleware ran above it.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfaceSpeech,
		RequestModel:    req.Model,
		TraceID:         logger.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so speech honours the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	var resp *providers.SpeechResponse
	record, err := g.runSurfaceGovernance(ctx, surfaceSpeech, span, start, req.Model, req.Input, func(ctx context.Context, model string) (surfaceRecord, error) {
		req.Model = model
		synthesized, target, routeErr := g.routeSpeech(ctx, req)
		if routeErr != nil {
			return surfaceRecord{provider: target.key, routedModel: req.Model, abVariantLabel: target.abVariantLabel, hasABVariant: target.hasABVariant}, routeErr
		}
		resp = synthesized
		return g.priceSurface(target, req.Model, providers.Usage{}, 0), nil
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, record.provider, req.Model, record.abVariantLabel, record.hasABVariant, err, latency, hooksEnabled, obsEventsActive)
		log.Error("speech request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, record, latency, hooksEnabled, obsEventsActive)

	log.Info("speech request completed", "model", req.Model, "audio_bytes", len(resp.Audio))
	return resp, nil
}

// recordSurfaceSuccess finalizes a completed embeddings/images request: emits
// Prometheus request metrics, stamps the span with the resolved
// provider/model/usage/cost, and dispatches the completed lifecycle event. It
// is the non-chat counterpart of recordSuccess (gateway_route_outcome.go),
// reusing its lifecycle-event plumbing (dispatchRequestEvent,
// completedEventData) rather than duplicating it.
//
// record.provider is the resolved target key; in this routing layer a target's
// virtual_key and its provider's registered Name() are the same string (see
// RegisterProvider).
func (g *Gateway) recordSurfaceSuccess(ctx context.Context, span observability.Span, obs observability.Provider, record surfaceRecord, latency time.Duration, hooksEnabled, obsEventsActive bool) {
	// Bound the metric label but keep the raw model on the span and the cost
	// result. Providers that accept any model ID (openrouter, ollama,
	// azure_openai, …) echo the caller's string back on success, so an unbounded
	// label here would let a client mint a new time series per request. The
	// streaming path bounds its label the same way.
	requestMetrics := metrics.ForRequest(record.provider, g.metricModel(record.routedModel))
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Success.Inc()
	requestMetrics.TokensIn.Add(float64(record.tokens.PromptTokens))
	requestMetrics.TokensOut.Add(float64(record.tokens.CompletionTokens))
	if record.cost.TotalUSD > 0 {
		requestMetrics.CostUSD.Add(record.cost.TotalUSD)
	}

	span.SetAttribute(observability.AttrGenAISystem, record.provider)
	span.SetAttribute(observability.AttrGenAIResponseModel, record.routedModel)
	if record.provider != "" {
		span.SetAttribute(observability.AttrFerroRoutingTargetKey, record.provider)
	}
	span.SetTokens(record.tokens.PromptTokens, record.tokens.CompletionTokens, record.tokens.ReasoningTokens)
	span.SetCost(observability.CostBreakdown{
		TotalUSD:      record.cost.TotalUSD,
		InputUSD:      record.cost.InputUSD,
		OutputUSD:     record.cost.OutputUSD,
		CacheReadUSD:  record.cost.CacheReadUSD,
		CacheWriteUSD: record.cost.CacheWriteUSD,
		ReasoningUSD:  record.cost.ReasoningUSD,
		ModelFound:    record.cost.ModelFound,
	})

	if hooksEnabled || obsEventsActive {
		he := completedEventData(
			logger.TraceIDFromContext(ctx),
			record.provider,
			record.routedModel,
			latency,
			false,
			record.tokens.PromptTokens,
			record.tokens.CompletionTokens,
			record.cost,
		)
		g.dispatchRequestEventWithABVariant(ctx, obs, hooksEnabled, obsEventsActive, he, record.abVariantLabel, record.hasABVariant)
	}
}

// isPluginAbort reports whether err is a plugin's own verdict or its own
// breakage, rather than a routing or provider failure. Both types are minted by
// plugin.Manager and by nothing else, so the test is exact.
func isPluginAbort(err error) bool {
	var rejection *plugin.RejectionError
	var failure *plugin.FailureError
	return errors.As(err, &rejection) || errors.As(err, &failure)
}

// recordSurfaceError finalizes a failed embeddings/images request: counts it
// against the counter that says why, observes how long it took to fail, stamps
// the span, and dispatches the failed lifecycle event. It is the non-chat
// counterpart of routeError (gateway_route_outcome.go), reusing its
// lifecycle-event plumbing (dispatchRequestEvent, failedEventData).
//
// provider is "" only when routing never resolved any target — a plugin denial,
// or a model no configured target serves — matching chat's empty-provider error
// label.
//
// It returns the redacted message for the caller's own log line; the lifecycle
// event is redacted independently by events.FailedRequest.
func (g *Gateway) recordSurfaceError(ctx context.Context, span observability.Span, obs observability.Provider, provider, model, abVariantLabel string, hasABVariant bool, err error, latency time.Duration, hooksEnabled, obsEventsActive bool) string {
	// Bucket the label, not the log or the span: model here is still the raw
	// client value on the "no provider serves this model" path.
	requestMetrics := metrics.ForRequest(provider, g.metricModel(model))
	// A failure is a request the gateway served, and how long it took to fail
	// belongs in the same histogram as how long a success took. Observing
	// successes only left a provider timing out at thirty seconds invisible on
	// these two surfaces, and made the quantiles answer "how fast are the
	// requests that worked" while being read as "how fast is the gateway".
	requestMetrics.Duration.Observe(latency.Seconds())

	// A request a plugin ended is not a provider failure. Counting a rate-limit
	// denial as an error is how a wave of throttled callers reads as a provider
	// outage — and the wire already tells them apart, since that caller got a
	// 429. The chat path has always made this distinction; these two surfaces
	// counted every abort as an error.
	if isPluginAbort(err) {
		recordPluginAbort(requestMetrics, err)
	} else {
		requestMetrics.Error.Inc()
		// These surfaces were absent from gateway_provider_errors_total
		// altogether, so a per-provider error alert could not see them fail.
		recordProviderErrorCtx(ctx, provider, err)
	}

	span.SetError(err)
	safeErr := redact.ErrorMessage(err)

	if hooksEnabled || obsEventsActive {
		he := failedEventData(
			logger.TraceIDFromContext(ctx),
			provider,
			model,
			err,
			latency,
			false,
		)
		g.dispatchRequestEventWithABVariant(ctx, obs, hooksEnabled, obsEventsActive, he, abVariantLabel, hasABVariant)
	}
	return safeErr
}

// embeddingCapable and imageCapable are the pipeline's candidacy gates for the
// two non-chat surfaces. They defer to providerSupportsSurface so "can this
// provider serve this surface" is answered in one place for both ranking and
// routing.
func embeddingCapable(p providers.Provider) bool {
	return providerSupportsSurface(p, surfaceEmbeddings)
}

func imageCapable(p providers.Provider) bool {
	return providerSupportsSurface(p, surfaceImages)
}

func rerankCapable(p providers.Provider) bool {
	return providerSupportsSurface(p, surfaceRerank)
}

func moderationCapable(p providers.Provider) bool {
	return providerSupportsSurface(p, surfaceModeration)
}

func transcriptionCapable(p providers.Provider) bool {
	return providerSupportsSurface(p, surfaceTranscription)
}

func speechCapable(p providers.Provider) bool {
	return providerSupportsSurface(p, surfaceSpeech)
}

// surfaceGate names a surface's candidacy gate as a value, so the pre-flight
// admission check tests candidacy exactly as the pipeline does. The results are
// package-level function values, so naming one allocates nothing.
func surfaceGate(surface string) capabilityGate {
	switch surface {
	case surfaceEmbeddings:
		return embeddingCapable
	case surfaceImages:
		return imageCapable
	case surfaceRerank:
		return rerankCapable
	case surfaceModeration:
		return moderationCapable
	case surfaceTranscription:
		return transcriptionCapable
	case surfaceSpeech:
		return speechCapable
	default:
		return nil
	}
}

// embed and generateImage are the surfaces' leaf calls. Package-level functions
// rather than closures, so the request rides through routeTargets as a value
// and the hot path allocates nothing to describe the call. The gate above has
// already run by the time either executes, so neither assertion can fail.
func embed(ctx context.Context, p providers.Provider, req providers.EmbeddingRequest, upstreamModel string) (*providers.EmbeddingResponse, error) {
	req.Model = upstreamModel
	provider, _ := providers.As[providers.EmbeddingProvider](p)
	return provider.Embed(ctx, req) // embeddingCapable gated candidacy
}

func generateImage(ctx context.Context, p providers.Provider, req providers.ImageRequest, upstreamModel string) (*providers.ImageResponse, error) {
	req.Model = upstreamModel
	provider, _ := providers.As[providers.ImageProvider](p)
	return provider.GenerateImage(ctx, req) // imageCapable gated candidacy
}

func rerank(ctx context.Context, p providers.Provider, req providers.RerankRequest, upstreamModel string) (*providers.RerankResponse, error) {
	req.Model = upstreamModel
	provider, _ := providers.As[providers.RerankProvider](p)
	return provider.Rerank(ctx, req) // rerankCapable gated candidacy
}

func moderate(ctx context.Context, p providers.Provider, req providers.ModerationRequest, upstreamModel string) (*providers.ModerationResponse, error) {
	req.Model = upstreamModel
	provider, _ := providers.As[providers.ModerationProvider](p)
	return provider.Moderate(ctx, req) // moderationCapable gated candidacy
}

func transcribe(ctx context.Context, p providers.Provider, req providers.TranscriptionRequest, upstreamModel string) (*providers.TranscriptionResponse, error) {
	req.Model = upstreamModel
	provider, _ := providers.As[providers.TranscriptionProvider](p)
	return provider.Transcribe(ctx, req) // transcriptionCapable gated candidacy
}

func speak(ctx context.Context, p providers.Provider, req providers.SpeechRequest, upstreamModel string) (*providers.SpeechResponse, error) {
	req.Model = upstreamModel
	provider, _ := providers.As[providers.SpeechProvider](p)
	return provider.Speech(ctx, req) // speechCapable gated candidacy
}

// routeEmbedding runs an embedding request through the shared request pipeline
// and returns the response together with the target that served it.
//
// It supplies only what is genuinely surface-specific — the candidate order,
// the capability gate, and the provider call. Target ordering, per-target
// retry, the circuit breaker, the concurrency limiter, latency accounting and
// "nothing here can serve this" are routeTargets', identically to chat: the
// three defects that came from this surface owning its own copy of that logic
// — retry honoured only under fallback, an unlabelled provider on failure, and
// a 500 where chat answered 404 — are now unrepresentable rather than fixed.
//
// If no configured target is viable for the model (wrong model, not an
// EmbeddingProvider, not registered) the request is refused: targets is an
// allowlist, so a provider that is registered but not listed never serves a
// request, on this surface or any other.
func (g *Gateway) routeEmbedding(ctx context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, routedTarget, error) {
	keys, err := g.surfaceTargetOrder(req.Model, surfaceEmbeddings)
	if err != nil {
		return nil, routedTarget{}, err
	}
	return routeTargets(ctx, g, g.planFor(req.Model, keys), req, embeddingCapable, embed)
}

// routeImage is routeEmbedding's counterpart for image generation; see its doc
// comment for the shared pipeline it attaches to.
func (g *Gateway) routeImage(ctx context.Context, req providers.ImageRequest) (*providers.ImageResponse, routedTarget, error) {
	keys, err := g.surfaceTargetOrder(req.Model, surfaceImages)
	if err != nil {
		return nil, routedTarget{}, err
	}
	return routeTargets(ctx, g, g.planFor(req.Model, keys), req, imageCapable, generateImage)
}

// routeRerank is routeEmbedding's counterpart for reranking; see its doc comment
// for the shared pipeline it attaches to.
func (g *Gateway) routeRerank(ctx context.Context, req providers.RerankRequest) (*providers.RerankResponse, routedTarget, error) {
	keys, err := g.surfaceTargetOrder(req.Model, surfaceRerank)
	if err != nil {
		return nil, routedTarget{}, err
	}
	return routeTargets(ctx, g, g.planFor(req.Model, keys), req, rerankCapable, rerank)
}

// routeModeration is routeEmbedding's counterpart for moderation; see its doc
// comment for the shared pipeline it attaches to.
func (g *Gateway) routeModeration(ctx context.Context, req providers.ModerationRequest) (*providers.ModerationResponse, routedTarget, error) {
	keys, err := g.surfaceTargetOrder(req.Model, surfaceModeration)
	if err != nil {
		return nil, routedTarget{}, err
	}
	return routeTargets(ctx, g, g.planFor(req.Model, keys), req, moderationCapable, moderate)
}

// routeTranscription is routeEmbedding's counterpart for audio transcription;
// see its doc comment for the shared pipeline it attaches to.
func (g *Gateway) routeTranscription(ctx context.Context, req providers.TranscriptionRequest) (*providers.TranscriptionResponse, routedTarget, error) {
	keys, err := g.surfaceTargetOrder(req.Model, surfaceTranscription)
	if err != nil {
		return nil, routedTarget{}, err
	}
	return routeTargets(ctx, g, g.planFor(req.Model, keys), req, transcriptionCapable, transcribe)
}

// routeSpeech is routeEmbedding's counterpart for text-to-speech; see its doc
// comment for the shared pipeline it attaches to.
func (g *Gateway) routeSpeech(ctx context.Context, req providers.SpeechRequest) (*providers.SpeechResponse, routedTarget, error) {
	keys, err := g.surfaceTargetOrder(req.Model, surfaceSpeech)
	if err != nil {
		return nil, routedTarget{}, err
	}
	return routeTargets(ctx, g, g.planFor(req.Model, keys), req, speechCapable, speak)
}

// surfaceTargetOrder resolves the candidate order for one non-chat request
// through the same strategy chat and streaming use, so a routing mode orders
// its targets identically on every surface. The surface narrows candidacy to
// the targets that can serve it (see strategyFor); whether the walk advances
// past a failure is planFor's question. Order is all this decides.
func (g *Gateway) surfaceTargetOrder(model, surface string) ([]string, error) {
	g.mu.RLock()
	mode := g.config.Strategy.Mode
	g.mu.RUnlock()
	if mode == config.ModeContentBased {
		// Content rules only look at req.Messages (prompt_contains /
		// prompt_not_contains / prompt_regex), and this surface has none —
		// Embed/GenerateImage requests carry no chat messages. prompt_contains
		// and prompt_regex correctly find no match against zero messages, but
		// strategies.ContentBased's prompt_not_contains rule is
		// `!anyUserMessageContains(...)`, which is vacuously TRUE over an
		// empty message set: it would match every embeddings/image request
		// and win routing outright, regardless of what the rule actually
		// says. No content rule can be meaningfully evaluated without a
		// prompt, so route in configured target order instead — exactly what
		// strategy.SelectTargets already falls back to when no rule matches.
		g.mu.Lock()
		g.ensureCircuitBreakersLocked()
		g.ensureProviderLimitersLocked()
		keys := make([]string, len(g.config.Targets))
		for i, t := range g.config.Targets {
			keys[i] = t.VirtualKey
		}
		g.mu.Unlock()
		return keys, nil
	}
	strategy, err := g.strategyFor(surface)
	if err != nil {
		return nil, err
	}
	return strategy.SelectTargets(providers.Request{Model: model})
}

func providerSupportsSurface(p providers.Provider, surface string) bool {
	switch surface {
	case surfaceEmbeddings:
		_, ok := providers.As[providers.EmbeddingProvider](p)
		return ok
	case surfaceImages:
		_, ok := providers.As[providers.ImageProvider](p)
		return ok
	case surfaceRerank:
		_, ok := providers.As[providers.RerankProvider](p)
		return ok
	case surfaceModeration:
		_, ok := providers.As[providers.ModerationProvider](p)
		return ok
	case surfaceTranscription:
		_, ok := providers.As[providers.TranscriptionProvider](p)
		return ok
	case surfaceSpeech:
		_, ok := providers.As[providers.SpeechProvider](p)
		return ok
	default:
		return false
	}
}
