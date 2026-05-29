package observability

// Span attribute keys.
//
// Group A — OpenTelemetry GenAI semantic conventions.
// Keep in sync with https://opentelemetry.io/docs/specs/semconv/gen-ai/.
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
const (
	AttrFerroSchemaVersion            = "ferro.schema.version"
	AttrFerroGatewayTraceID           = "ferro.gateway.trace_id"
	AttrFerroGatewayVersion           = "ferro.gateway.version"
	AttrFerroRoutingStrategy          = "ferro.routing.strategy"
	AttrFerroRoutingTargetKey         = "ferro.routing.target_key"
	AttrFerroRoutingAttempt           = "ferro.routing.attempt"
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
)

// SchemaVersion is the ferro.observability.v1 schema version this
// build emits. Exporters MAY use this value to branch on schema
// migrations.
const SchemaVersion = "1.0.0-draft"
