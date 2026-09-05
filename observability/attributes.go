package observability

// Span attribute keys.
//
// Group A — OpenTelemetry GenAI semantic conventions.
// Keep in sync with https://opentelemetry.io/docs/specs/semconv/gen-ai/.
//
// Planned: the following constants are part of the published schema surface
// but are not yet wired into emitted spans — AttrGenAIRequestMaxTokens,
// AttrGenAIRequestTemperature, AttrGenAIRequestTopP, and
// AttrGenAIResponseFinishReasons.
//
//nolint:gosec // G101 false positives: these are attribute name constants, not credentials.
const (
	AttrGenAISystem                = "gen_ai.system"
	AttrGenAIOperationName         = "gen_ai.operation.name"
	AttrGenAIRequestModel          = "gen_ai.request.model"
	AttrGenAIResponseModel         = "gen_ai.response.model"
	AttrGenAIRequestMaxTokens      = "gen_ai.request.max_tokens"
	AttrGenAIRequestTemperature    = "gen_ai.request.temperature"
	AttrGenAIRequestTopP           = "gen_ai.request.top_p"
	AttrGenAIRequestIsStream       = "gen_ai.request.is_stream"
	AttrGenAIUsageInputTokens      = "gen_ai.usage.input_tokens"
	AttrGenAIUsageOutputTokens     = "gen_ai.usage.output_tokens"
	AttrGenAIUsageReasoningTokens  = "gen_ai.usage.reasoning_tokens"
	AttrGenAIResponseFinishReasons = "gen_ai.response.finish_reasons"
)

// Group B — Ferro extension attributes. ferro.* namespace.
//
// Planned: the following constants are part of the published schema surface
// but are not yet wired into emitted spans — AttrFerroGatewayVersion,
// AttrFerroRoutingABVariantLabel, AttrFerroCacheHit,
// AttrFerroCacheKind, AttrFerroMCPDepth, AttrFerroCircuitBreakerState,
// AttrFerroCircuitBreakerOpened, AttrFerroRequestAPIKeyID,
// AttrFerroRequestTenantID, AttrFerroErrorUpstreamStatus,
// AttrFerroErrorRetryCount, and AttrFerroForwardedParams.
const (
	AttrFerroSchemaVersion    = "ferro.schema.version"
	AttrFerroGatewayTraceID   = "ferro.gateway.trace_id"
	AttrFerroGatewayVersion   = "ferro.gateway.version"
	AttrFerroRoutingStrategy  = "ferro.routing.strategy"
	AttrFerroRoutingTargetKey = "ferro.routing.target_key"
	// AttrFerroRoutingAttempt is the routing-layer attempt count when the walk
	// ended — provider calls plus local breaker/concurrency refusals, retries
	// and failovers included — the same number X-Gateway-Attempts carries.
	AttrFerroRoutingAttempt = "ferro.routing.attempt"
	// AttrFerroRoutingSequence is the 1-based position of one attempt within
	// its request and AttrFerroRoutingOutcome is "success" or "error"; both
	// sit on the optional SpanNameRoutingAttempt span, next to the target key.
	AttrFerroRoutingSequence = "ferro.routing.sequence"
	AttrFerroRoutingOutcome  = "ferro.routing.outcome"
	// SpanNameRoutingAttempt names the per-attempt CLIENT span opened when
	// observability.tracing.attempt_spans is set. Same value as
	// SubjectRoutingAttempt (one literal, not two).
	SpanNameRoutingAttempt            = SubjectRoutingAttempt
	AttrFerroRoutingABVariantLabel    = "ferro.routing.ab_variant_label"
	AttrFerroCostUSD                  = "ferro.cost.usd"
	AttrFerroCostInputUSD             = "ferro.cost.input_usd"
	AttrFerroCostOutputUSD            = "ferro.cost.output_usd"
	AttrFerroCostCacheReadUSD         = "ferro.cost.cache_read_usd"
	AttrFerroCostCacheWriteUSD        = "ferro.cost.cache_write_usd"
	AttrFerroCostReasoningUSD         = "ferro.cost.reasoning_usd"
	AttrFerroCostModelFound           = "ferro.cost.model_found"
	AttrFerroCacheHit                 = "ferro.cache.hit"
	AttrFerroCacheKind                = "ferro.cache.kind"
	AttrFerroPluginName               = "ferro.plugin.name"
	AttrFerroPluginKind               = "ferro.plugin.kind"
	AttrFerroPluginStage              = "ferro.plugin.stage"
	AttrFerroPluginOutcome            = "ferro.plugin.outcome"
	AttrFerroPluginReason             = "ferro.plugin.reason"
	AttrFerroMCPServer                = "ferro.mcp.server"
	AttrFerroMCPTool                  = "ferro.mcp.tool"
	AttrFerroMCPDepth                 = "ferro.mcp.depth"
	AttrFerroMCPLatencyMs             = "ferro.mcp.latency_ms"
	AttrFerroStreamTimeToFirstTokenMs = "ferro.stream.time_to_first_token_ms"
	AttrFerroStreamTimeToLastTokenMs  = "ferro.stream.time_to_last_token_ms"
	AttrFerroCircuitBreakerState      = "ferro.circuit_breaker.state"
	AttrFerroCircuitBreakerOpened     = "ferro.circuit_breaker.opened"
	AttrFerroRequestAPIKeyID          = "ferro.request.api_key_id"
	AttrFerroRequestTenantID          = "ferro.request.tenant_id"
	AttrFerroErrorUpstreamStatus      = "ferro.error.upstream_status"
	AttrFerroErrorRetryCount          = "ferro.error.retry_count"
	// AttrFerroForwardedParams carries the sanitized NAMES (never values) of the
	// request parameters forwarded to the provider, for debug visibility of the
	// capability matrix. Planned: emission is deferred — the shared request
	// builder has no span in scope (see providers/core/openaicompat).
	AttrFerroForwardedParams = "ferro.forwarded_params"
)

// SchemaVersion is the ferro.observability.v1 schema version this
// build emits. Exporters MAY use this value to branch on schema
// migrations.
const SchemaVersion = "1.0.0-draft"

// SubjectRoutingAttempt identifies one physical routing attempt.
const SubjectRoutingAttempt = "gateway.routing.attempt"

// Group C — request identity. Standard OpenTelemetry names are used where one
// exists (enduser.id, session.id); metadata uses the ferro.* namespace.
const (
	// AttrEndUserID is the end-user id the caller supplied: the OpenAI `user`
	// field, the X-User-ID header, or the baggage entry user.id.
	AttrEndUserID = "enduser.id"
	// AttrSessionID groups the requests of one conversation: the X-Session-ID
	// header or the baggage entry session.id.
	AttrSessionID = "session.id"
	// AttrFerroRequestMetadataPrefix prefixes one attribute per
	// RequestIdentity.Metadata entry: ferro.request.metadata.<key>.
	AttrFerroRequestMetadataPrefix = "ferro.request.metadata."
)
