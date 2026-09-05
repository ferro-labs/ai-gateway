// Package config defines the AI Gateway configuration schema (Config and its
// sub-types) and the loader/validator (LoadConfig, ValidateConfig). These types
// moved here from the root aigateway package in v1.4.0; embedders import this
// package directly. The gateway is still built with aigateway.New, which takes a
// config.Config.
package config

import "github.com/ferro-labs/ai-gateway/mcp"

// DefaultMaxRequestBytes is the default per-request body-size cap (10 MiB).
// Operators may lower or raise this via Config.MaxRequestBytes.
const DefaultMaxRequestBytes int64 = 10 * 1024 * 1024

// CurrentAPIVersion is the config schema version this build understands.
// Normalize stamps it onto Config.APIVersion when the field is omitted.
const CurrentAPIVersion = "v1"

// Config holds the configuration for the AI Gateway.
type Config struct {
	// APIVersion is an optional, advisory config schema version (e.g. "v1").
	// It is informational only: an empty value defaults to CurrentAPIVersion in
	// Normalize, a recognized value is accepted as-is, and an unrecognized value
	// is preserved and only logged as a warning so newer config files remain
	// forward-compatible with older binaries. It never causes a load to fail.
	APIVersion string `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	// Strategy defines how requests are routed (e.g., single, fallback, loadbalance).
	Strategy StrategyConfig `json:"strategy" yaml:"strategy"`
	// Targets is a list of provider targets to route requests to.
	Targets []Target `json:"targets" yaml:"targets"`
	// MaxRequestBytes caps the size of incoming request bodies on data-plane routes
	// (/v1/*) and admin write endpoints. Requests that exceed the limit receive
	// HTTP 413 Request Entity Too Large before any LLM call is attempted.
	// 0 (the default when omitted) applies DefaultMaxRequestBytes (10 MiB), which
	// is well above any realistic chat completion payload.
	MaxRequestBytes int64 `json:"max_request_bytes,omitempty" yaml:"max_request_bytes,omitempty"`
	// RequestTimeout bounds a single non-streaming request end to end — plugin
	// stages, provider call, and every retry and fallback attempt combined — as a
	// Go duration string (e.g. "30s"). Omitted or empty means no gateway-imposed
	// deadline; the provider HTTP clients' own timeouts still apply.
	//
	// Streaming requests are exempt: a stream legitimately outlives any fixed
	// deadline, and its idle bound is enforced by the streaming write deadline.
	//
	// One exception, and it is not a loophole: when MCP tool servers are
	// configured, a stream request runs the agentic loop to completion and is
	// delivered as a single chunk. That is a non-streaming request wearing a
	// stream's clothes, so the deadline applies to it like any other.
	RequestTimeout string `json:"request_timeout,omitempty" yaml:"request_timeout,omitempty"`
	// Plugins configuration (optional).
	Plugins []PluginConfig `json:"plugins,omitempty" yaml:"plugins,omitempty"`
	// Aliases maps friendly model names (e.g. "fast", "smart") to concrete model IDs.
	// Aliases are resolved before routing — they must not reference other aliases.
	Aliases map[string]string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	// MCPServers configures external MCP tool servers for agentic tool calling.
	// When set, the gateway injects discovered tools into every chat completion
	// request and executes an agentic loop when the LLM returns tool_calls.
	// It is read once at New() time, so a caller embedding the gateway can
	// populate it from its own source instead of a config file.
	MCPServers []mcp.ServerConfig `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`
	// MCPToolCallAuditFn, if non-nil, is called after every MCP tool invocation,
	// giving an embedding caller a hook to record tool use. It cannot be set via
	// JSON or YAML — set it programmatically before calling New. It runs off the
	// request path, so it must not block.
	MCPToolCallAuditFn mcp.ToolCallAuditFn `json:"-" yaml:"-"`
	// Observability configures OpenTelemetry tracing. When omitted the
	// gateway runs with a NoOp provider (zero allocations on the hot
	// path). See internal/otel.
	Observability ObservabilityConfig `json:"observability,omitempty" yaml:"observability,omitempty"`
	// Compatibility configures how the gateway treats request parameters a
	// target provider cannot express. Omitted (the default) means warn.
	Compatibility CompatibilityConfig `json:"compatibility,omitempty" yaml:"compatibility,omitempty"`
	// BatchTarget names the target (a targets[].virtual_key) that serves the
	// batch and files surfaces — /v1/files* and /v1/batches*. Those endpoints
	// carry no model to route on and reference opaque, provider-scoped ids, so a
	// single backend serves them all; the ids stay native and every follow-up
	// call (retrieve, cancel, download) resolves against the same backend without
	// any gateway-held state. Omitted disables the surface (the endpoints return
	// 501). The named target's provider must implement batch pass-through
	// (CapabilityBatch); openai, azure-openai, groq, novita and qwen do.
	BatchTarget string `json:"batch_target,omitempty" yaml:"batch_target,omitempty"`
	// ResponsesTarget names the target that serves the stateful Responses id
	// sub-routes — GET/DELETE /v1/responses/{id}, /cancel, /input_items. Those
	// carry no model and reference an opaque, provider-scoped response id, so a
	// single backend serves them (the same reasoning as BatchTarget). POST
	// /v1/responses is unaffected: it carries a model and routes normally.
	// Omitted disables only the id sub-routes (they return 501); the create
	// endpoint still works. Should name the provider a deployment creates
	// responses against (openai or xai).
	ResponsesTarget string `json:"responses_target,omitempty" yaml:"responses_target,omitempty"`
}

// CompatibilityConfig controls the gateway's handling of OpenAI request
// parameters that a routed provider does not support (per the capability
// matrix in providers/capabilities).
type CompatibilityConfig struct {
	// OnUnsupportedParam selects the behaviour for an unsupported parameter:
	// "warn" (default) forwards it and logs, "drop" removes it from the
	// upstream request and logs, and "reject" fails the request with HTTP 400.
	// An empty value is treated as "warn".
	//
	// warn and drop diverge only for providers reached over an
	// OpenAI-compatible request body, where warn really does forward the
	// parameter. A provider with a native wire format builds a payload that has
	// nowhere to carry it, so warn and drop produce byte-identical upstream
	// requests there and both log "dropping" — see
	// core.EnforceUnsupportedParams. Only "reject" guarantees the caller learns
	// that a parameter was not honoured.
	OnUnsupportedParam string `json:"on_unsupported_param,omitempty" yaml:"on_unsupported_param,omitempty"`
}

// Normalize applies config-level defaults in a single place. It is idempotent
// and mutates the receiver. LoadConfig calls it after decoding so a loaded
// Config carries its effective defaults; callers that build a Config
// programmatically may call it before New for the same result.
//
// Scope is deliberately limited to top-level defaults that would otherwise be
// applied inline during load/validate. Strategy-internal defaults (e.g. retry
// backoff) are applied where the strategy runs, not here.
func (c *Config) Normalize() {
	if c.APIVersion == "" {
		c.APIVersion = CurrentAPIVersion
	}
	if c.Strategy.Mode == "" {
		c.Strategy.Mode = ModeSingle
	}
}

// ObservabilityConfig is the user-facing observability section of
// gateway config. It mirrors internal/otel.Config but lives here so
// the public Config schema does not pull in internal packages.
//
// Standard OTEL_* environment variables (notably
// OTEL_EXPORTER_OTLP_ENDPOINT) always take precedence — this matches
// the OTel SDK convention required for predictable container
// deployments.
type ObservabilityConfig struct {
	// Tracing holds the OTLP tracing configuration. v1.1.0 ships
	// tracing only; metrics and logs exporters arrive in later
	// releases (see docs/OSS-ECOSYSTEM-ROADMAP.md).
	Tracing TracingConfig `json:"tracing,omitempty" yaml:"tracing,omitempty"`
	// Exporters lists the plugin observability exporters that should
	// receive gateway events (request completed / request failed).
	// Each entry names an exporter registered via
	// observability.RegisterExporter and carries its own Config block.
	// Exporters that are not registered at startup emit a warning and
	// are skipped — they do not prevent the gateway from starting.
	Exporters []ExporterConfig `json:"exporters,omitempty" yaml:"exporters,omitempty"`
}

// ExporterConfig configures a single observability plugin exporter.
// Plugin authors register their factory via observability.RegisterExporter
// in their package init(); gateway operators then reference the name here.
//
// Example (YAML):
//
//	exporters:
//	  - name: langsmith
//	    enabled: true
//	    config:
//	      api_key: "${LANGSMITH_API_KEY}"
type ExporterConfig struct {
	// Name is the canonical exporter name, e.g. "langsmith".
	// Must match the name passed to observability.RegisterExporter.
	Name string `json:"name" yaml:"name"`
	// Enabled gates the exporter. Set to false to temporarily disable
	// without removing the config block.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Config is the exporter-specific configuration map. String values may
	// reference environment variables using ${VAR} — only the braced form is a
	// reference, a bare $ is literal data, and an undefined variable is an
	// error. References are resolved when the exporter is constructed, not when
	// the config is loaded, so the Config never carries a materialised secret
	// into the config-history store or GET /admin/config. Resolved values are
	// passed to Exporter.Init at gateway startup.
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// TracingConfig configures the OTLP tracing pipeline. All fields are
// optional; sensible defaults apply when omitted (see
// internal/otel.DefaultConfig).
type TracingConfig struct {
	// Enabled is the master switch, a tri-state pointer:
	//   nil   — infer activation from a configured endpoint/exporters
	//           (the default when the key is omitted).
	//   false — hard off: tracing stays disabled even when an endpoint or
	//           OTEL_EXPORTER_OTLP_ENDPOINT is set.
	//   true  — force on.
	// The pipeline still short-circuits to NoOp when nothing is configured.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// Endpoint overrides OTEL_EXPORTER_OTLP_ENDPOINT (host:port form).
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	// Protocol selects the OTLP transport: "grpc" (default) or "http/protobuf".
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	// ServiceName populates the OTel service.name resource attribute.
	ServiceName string `json:"service_name,omitempty" yaml:"service_name,omitempty"`
	// SampleRatio is the head sampler ratio (0.0–1.0). Pointer so an
	// explicit 0.0 (sample nothing) is distinguishable from an omitted
	// field; nil falls back to the default of 1.0 (sample everything).
	SampleRatio *float64 `json:"sample_ratio,omitempty" yaml:"sample_ratio,omitempty"`
	// PrivacyLevel controls whether prompt/response content is exported.
	// One of: "none", "metadata" (default), "full".
	PrivacyLevel string `json:"privacy_level,omitempty" yaml:"privacy_level,omitempty"`
	// ShutdownGrace is the maximum time each OTel shutdown stage waits.
	// Exporter shutdown and TracerProvider shutdown each receive this
	// budget, so total telemetry shutdown may take up to twice this value.
	// Accepts any Go duration string, e.g. "10s", "500ms". Defaults to 10s
	// when empty or unparseable.
	ShutdownGrace string `json:"shutdown_grace,omitempty" yaml:"shutdown_grace,omitempty"`
	// Headers are additional HTTP/gRPC metadata headers sent with every OTLP
	// export request. Use this to authenticate with managed backends such as
	// Datadog, New Relic, Honeycomb, or Grafana Cloud.
	//
	// SECURITY: prefer ${ENV_VAR} references for secret values — only the
	// template (e.g. "${DATADOG_API_KEY}") is persisted in config and returned
	// by the admin config API; the secret is resolved from the environment at
	// export time and never stored. A literal value IS persisted verbatim and
	// exposed via /admin/config, so do not hard-code raw secrets here. The
	// standard OTEL_EXPORTER_OTLP_HEADERS environment variable also applies per
	// OTel convention.
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	// AttemptSpans opens one CLIENT child span, gateway.routing.attempt, per
	// routing-layer attempt — retries and failovers included — carrying
	// ferro.routing.target_key, ferro.routing.sequence and
	// ferro.routing.outcome, so a trace shows which targets a request tried
	// and in what order. Off by default: a request that retried produces
	// several, and a dashboard counting spans per request would over-count.
	AttemptSpans bool `json:"attempt_spans,omitempty" yaml:"attempt_spans,omitempty"`
	// PropagatePassthrough injects the W3C traceparent (and tracestate) into
	// requests the /v1/* pass-through and the fixed-target forwards
	// (/v1/responses create and its id sub-routes, /v1/files, /v1/batches)
	// forward upstream, so a provider that records traces joins the
	// gateway's. The caller's baggage, X-User-ID and X-Session-ID headers
	// address the gateway, not the provider, and their own traceparent and
	// tracestate belong to a trace the provider is not part of; all five are
	// always stripped before forwarding regardless of this setting — it is
	// never a pass-through of the caller's own headers. Injection itself only
	// happens when the request carries a span this gateway opened; the
	// fixed-target forwards open none, so those routes forward no trace
	// context at all — never the caller's own, even with this on. A pointer
	// so the default is true when the key is omitted; set false to stop
	// injecting trace context.
	PropagatePassthrough *bool `json:"propagate_passthrough,omitempty" yaml:"propagate_passthrough,omitempty"`
}

// PropagatesPassthrough reports whether the pass-through proxy injects trace
// context upstream: true unless propagate_passthrough is explicitly false.
func (t TracingConfig) PropagatesPassthrough() bool {
	return t.PropagatePassthrough == nil || *t.PropagatePassthrough
}

// StrategyConfig defines the routing strategy.
type StrategyConfig struct {
	Mode       StrategyMode `json:"mode" yaml:"mode"`
	Conditions []Condition  `json:"conditions,omitempty" yaml:"conditions,omitempty"` // For conditional routing
	// UnpricedStrategy controls how cost-optimized routing treats providers with
	// missing catalog pricing: "fallback" (default), "skip", or "allow".
	UnpricedStrategy string `json:"unpriced_strategy,omitempty" yaml:"unpriced_strategy,omitempty"`
	// ContentConditions defines rules for the content-based routing strategy.
	// Rules are evaluated in order; the first match wins.
	ContentConditions []ContentCondition `json:"content_conditions,omitempty" yaml:"content_conditions,omitempty"`
	// ABVariants defines the weighted variants for the ab-test strategy.
	ABVariants []ABVariantConfig `json:"ab_variants,omitempty" yaml:"ab_variants,omitempty"`
	// FailoverOnStatusCodes adds upstream HTTP status codes a pool mode, or
	// a rule chain, treats as failover-safe — for an operator-specific
	// upstream whose "try elsewhere" answer is not one the built-in classes
	// cover. Each code is an integer in 100–599. The protected classes cannot
	// be added: the caller's cancellation or deadline always stops routing,
	// and the deterministic client errors 400, 401, 403, 404 and 422 stay
	// with the target that answered them, since re-sending a malformed or
	// unauthorised request to every target changes nothing but the bill.
	FailoverOnStatusCodes []int `json:"failover_on_status_codes,omitempty" yaml:"failover_on_status_codes,omitempty"`
	// Sticky pins a request to the same target for the same key under
	// loadbalance and ab-test, so a conversation keeps its provider prompt
	// cache and a multi-turn A/B session keeps its variant. Stateless: a
	// hash of the key decides the draw, so it needs no shared state.
	Sticky *StickyConfig `json:"sticky,omitempty" yaml:"sticky,omitempty"`
}

// StickyConfig configures sticky hashing for loadbalance and ab-test.
type StickyConfig struct {
	// On names the request field hashed. Only "user" is supported: the
	// request's `user` field, which is what provider prompt caches and a
	// session are scoped by. A request without one draws at random.
	On string `json:"on" yaml:"on"`
	// TTL, a Go duration, rotates assignments: a pin lasts at most one TTL
	// window, after which the key may hash to another target. Omitted means
	// a pin holds for as long as the config does.
	TTL string `json:"ttl,omitempty" yaml:"ttl,omitempty"`
}

// ProtectedFailoverStatusCodes are the upstream statuses
// StrategyConfig.FailoverOnStatusCodes may not add: deterministic client
// errors that a different target cannot fix.
func ProtectedFailoverStatusCodes() []int { return []int{400, 401, 403, 404, 422} }

// StickyOnUser is the one accepted StickyConfig.On value.
const StickyOnUser = "user"

// StrategyMode represents the routing strategy mode.
type StrategyMode string

// StrategyMode constants define the supported routing strategies.
const (
	ModeSingle        StrategyMode = "single"
	ModeFallback      StrategyMode = "fallback"
	ModeLoadBalance   StrategyMode = "loadbalance"
	ModeConditional   StrategyMode = "conditional"
	ModeLatency       StrategyMode = "least-latency"
	ModeCostOptimized StrategyMode = "cost-optimized"
	ModeContentBased  StrategyMode = "content-based"
	ModeABTest        StrategyMode = "ab-test"
)

// UnpricedStrategy* are the accepted values for StrategyConfig.UnpricedStrategy,
// controlling how cost-optimized routing treats providers with missing catalog
// pricing.
const (
	UnpricedStrategyFallback = "fallback"
	UnpricedStrategySkip     = "skip"
	UnpricedStrategyAllow    = "allow"
)

// MaxTargetConcurrency is the highest value ValidateConfig accepts for a target's
// max_concurrency or queue_size.
//
// The bound is about intent, not memory: slots is a chan struct{}, whose zero-size
// element means capacity costs no buffer at all. What an absurd value does instead
// is admit every request, so the cap the operator asked for silently stops applying.
// Real per-target concurrency is bounded by what the upstream provider will accept —
// orders of magnitude below this — so a larger value is a typo, not a deployment.
const MaxTargetConcurrency = 10_000

// Condition represents a condition for conditional routing.
type Condition struct {
	// Key selects what the rule matches on. It must be one of ConditionKey*;
	// ValidateConfig rejects anything else, because the matcher's switch has no
	// dynamic arm — an unrecognised key would match nothing and silently send the
	// rule's traffic to the fallback target instead.
	Key   string `json:"key" yaml:"key"`
	Value string `json:"value" yaml:"value"`
	// Field names the metadata entry a `key: metadata` rule reads; required
	// there and refused elsewhere.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`
	// TargetKey names the one target this rule routes to; it must be a
	// configured targets[].virtual_key. Sugar for a one-entry TargetKeys.
	TargetKey string `json:"target_key,omitempty" yaml:"target_key,omitempty"`
	// TargetKeys is the rule's ordered target chain. The walk tries them in
	// order, advancing only on a failover-safe failure and skipping one whose
	// circuit is open, that is saturated, or that is parked after a 429. The
	// chain is a boundary: no target outside it is ever substituted, so a
	// rule with one target is exact. Exactly one of TargetKey and TargetKeys
	// is set.
	TargetKeys []string `json:"target_keys,omitempty" yaml:"target_keys,omitempty"`
}

// ConditionKey* are the accepted values for Condition.Key. The set is closed:
// internal/strategies.Conditional matches on exactly these and nothing resolves
// at runtime, so a value outside the set can only ever be a typo.
//
// The set is bounded on purpose. `user`, `stream` and `has_tools` are fields
// every chat request already carries; `metadata` reads one entry of the
// single allow-listed X-Gateway-Metadata header. Arbitrary request headers
// are never exposed to a predicate.
const (
	ConditionKeyModel       = "model"
	ConditionKeyModelPrefix = "model_prefix"
	// ConditionKeyUser matches the request's `user` field exactly.
	ConditionKeyUser = "user"
	// ConditionKeyStream matches whether the request streams: value "true"
	// or "false".
	ConditionKeyStream = "stream"
	// ConditionKeyHasTools matches whether the request carries tools: value
	// "true" or "false".
	ConditionKeyHasTools = "has_tools"
	// ConditionKeyMetadata matches one entry of the X-Gateway-Metadata
	// header, named by Condition.Field, against Value.
	ConditionKeyMetadata = "metadata"
)

// ConditionKeys returns the accepted Condition.Key values in a stable order.
func ConditionKeys() []string {
	return []string{ConditionKeyModel, ConditionKeyModelPrefix, ConditionKeyUser, ConditionKeyStream, ConditionKeyHasTools, ConditionKeyMetadata}
}

// ContentCondition maps a prompt-content matching rule to a routing target.
// Used with the "content-based" strategy mode.
//
// Supported types:
//   - "prompt_contains"     — case-insensitive substring match on user messages
//   - "prompt_not_contains" — true when NO user message contains the value
//   - "prompt_regex"        — Go regular expression match on user messages
type ContentCondition struct {
	// Type is the matching rule type. It must be one of ContentConditionType*;
	// see Condition.Key for why the set is closed.
	Type string `json:"type" yaml:"type"`
	// Value is the substring or regex pattern to match against.
	Value string `json:"value" yaml:"value"`
	// TargetKey is the virtual_key of the provider to route to when this rule
	// matches. It must name one of the configured targets. Sugar for a
	// one-entry TargetKeys.
	TargetKey string `json:"target_key,omitempty" yaml:"target_key,omitempty"`
	// TargetKeys is the rule's ordered target chain; see Condition.TargetKeys.
	TargetKeys []string `json:"target_keys,omitempty" yaml:"target_keys,omitempty"`
}

// Chain returns the rule's target chain: TargetKeys, or TargetKey as a
// one-entry chain.
func (c Condition) Chain() []string { return ruleChain(c.TargetKey, c.TargetKeys) }

// Chain returns the rule's target chain: TargetKeys, or TargetKey as a
// one-entry chain.
func (c ContentCondition) Chain() []string { return ruleChain(c.TargetKey, c.TargetKeys) }

func ruleChain(targetKey string, targetKeys []string) []string {
	if len(targetKeys) > 0 {
		return targetKeys
	}
	if targetKey == "" {
		return nil
	}
	return []string{targetKey}
}

// ContentConditionType* are the accepted values for ContentCondition.Type.
// The set is closed — see ConditionKeys.
const (
	ContentConditionPromptContains    = "prompt_contains"
	ContentConditionPromptNotContains = "prompt_not_contains"
	ContentConditionPromptRegex       = "prompt_regex"
)

// ContentConditionTypes returns the accepted ContentCondition.Type values in a
// stable order.
func ContentConditionTypes() []string {
	return []string{
		ContentConditionPromptContains,
		ContentConditionPromptNotContains,
		ContentConditionPromptRegex,
	}
}

// ABVariantConfig defines a single traffic variant for the "ab-test" strategy.
type ABVariantConfig struct {
	// TargetKey is the virtual_key of the provider for this variant.
	TargetKey string `json:"target_key" yaml:"target_key"`
	// Weight is the relative traffic share for this variant.
	// All weights are summed; each variant's fraction is Weight/Total.
	//
	// Zero means zero: a zero-weight variant receives no traffic at all, which is
	// what makes it usable to drain a variant before removing it. Negative is a
	// config error, and so is an all-zero set — with nothing left to select, the
	// gateway would answer every request 404 while reporting itself ready.
	Weight float64 `json:"weight" yaml:"weight"`
	// Label is required. It is the value emitted as ferro.routing.ab_variant_label on every
	// routing attempt and terminal event for a request assigned to this variant.
	Label string `json:"label" yaml:"label"`
}

// Target represents a specific provider target.
type Target struct {
	// VirtualKey is the unique identifier for the provider (or a virtual key in the vault).
	VirtualKey string `json:"virtual_key" yaml:"virtual_key"`
	// Weight is the target's relative share under mode: loadbalance, and the
	// tie-break among equal-cost targets under mode: cost-optimized. Every
	// other mode ignores it.
	//
	// Zero means zero — see ABVariantConfig.Weight. Draining a target ahead of
	// revoking its credential is the reason the value exists, so a zero that
	// still received traffic would break live requests at precisely the moment
	// the operator was trying to avoid that.
	Weight float64 `json:"weight,omitempty" yaml:"weight,omitempty"`
	// Models are additional model IDs this target serves, declared by the
	// operator. They join the routing index alongside the model catalog and live
	// discovery, and are advertised by /v1/models.
	//
	// It is the provider-agnostic lever for a target whose real inventory neither
	// of the automatic sources can see: a provider that exposes no /models
	// endpoint to enumerate, a model newer than the catalog, a regional or
	// preview id, a self-hosted deployment behind <PROVIDER>_BASE_URL. Every
	// target has it, so a gap never has to wait for provider-specific work.
	//
	// It is ADDITIVE, never restrictive. The list extends what the catalog and
	// discovery already give the target; it does not narrow them, so adding one
	// id cannot silently unroute the rest. There is deliberately no allowlist
	// spelling here — a target that must serve less is a target you remove.
	//
	// Entries are exact model IDs. Wildcards are rejected at load: the routing
	// index is an exact-match map, so a pattern would match nothing while looking
	// like it matched everything. A provider whose upstream genuinely accepts ids
	// nothing can enumerate declares core.AnyModelProvider instead.
	//
	// Declaring an id the catalog or discovery already reports is a harmless
	// no-op — the sources are merged and de-duplicated — so pinning one against a
	// catalog that may change is safe.
	Models []string `json:"models,omitempty" yaml:"models,omitempty"`
	// ModelMap maps routed model IDs to exact model IDs sent to this target.
	// Keys join the target's additive model inventory. Global aliases resolve
	// before this mapping, so an alias name cannot be used as a key.
	ModelMap map[string]string `json:"model_map,omitempty" yaml:"model_map,omitempty"`
	// Timeout bounds one physical attempt against this target, as a Go
	// duration ("8s"). A unary attempt is bounded through its response; a
	// streaming attempt only until the provider answers, since a stream that
	// has begun cannot be replayed elsewhere. It sits inside request_timeout,
	// which stays authoritative for the request as a whole. An attempt that
	// times out is a failover-safe failure: pool modes move to the next target.
	// Omitted means no per-attempt bound beyond request_timeout.
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	// Retry configuration for this target.
	Retry *RetryConfig `json:"retry,omitempty" yaml:"retry,omitempty"`
	// CircuitBreaker configuration for this target (optional).
	CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty" yaml:"circuit_breaker,omitempty"`
	// Concurrency bounds simultaneous in-flight requests to this target (optional).
	Concurrency *ConcurrencyConfig `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

// ConcurrencyConfig bounds how many requests may be in flight against a single
// target at once. Providers often cap simultaneous connections independently of
// any RPM/TPM quota; without a gate the gateway can saturate one and collect
// 429s or connection resets that needlessly trip its circuit breaker.
type ConcurrencyConfig struct {
	// MaxConcurrency is the maximum number of simultaneous in-flight requests to
	// this target. It must be positive; omit the whole block to leave a target
	// unlimited. A streaming request holds its slot until the stream ends.
	MaxConcurrency int `json:"max_concurrency" yaml:"max_concurrency"`
	// QueueSize is how many requests may wait for a slot once MaxConcurrency is
	// reached. Requests beyond it fail fast with HTTP 429 rather than blocking.
	// 0 (the default when omitted) applies DefaultConcurrencyQueueSize.
	QueueSize int `json:"queue_size,omitempty" yaml:"queue_size,omitempty"`
}

// RetryConfig defines per-target retry behavior. It applies under every routing
// mode, not only fallback: retry is resolved by the request pipeline from the
// target being attempted, and the strategy contributes target order alone.
type RetryConfig struct {
	// Attempts is the maximum number of attempts per target (1 = no retries).
	Attempts int `json:"attempts" yaml:"attempts"`
	// OnStatusCodes, when non-empty, limits retries to the listed HTTP status
	// codes. A retry is skipped when the provider returns a code not in the
	// list, and the strategy moves on to the next target immediately.
	// Leave empty for the default policy: retry transport failures and HTTP
	// 408/429/5xx; a deterministic 4xx (e.g. 400/401/404) is not retried.
	// Example: [429, 502, 503]
	OnStatusCodes []int `json:"on_status_codes,omitempty" yaml:"on_status_codes,omitempty"`
	// InitialBackoffMs is the base backoff in milliseconds for exponential
	// back-off with FULL JITTER: the wait before attempt N is drawn uniformly
	// from [0, InitialBackoffMs * 2^(N-1)), not fixed at the upper bound. Full
	// jitter spreads a herd of retrying clients; a fixed exponential
	// re-synchronises them. An upstream Retry-After hint, when present, takes
	// precedence over the computed wait.
	// Defaults to 100 ms when unset or zero.
	InitialBackoffMs int `json:"initial_backoff_ms,omitempty" yaml:"initial_backoff_ms,omitempty"`
}

// CircuitBreakerConfig configures the per-provider circuit breaker.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of consecutive failures before the circuit
	// opens. Defaults to 5.
	FailureThreshold int `json:"failure_threshold" yaml:"failure_threshold"`
	// SuccessThreshold is the number of consecutive successes in half-open state
	// required to close the circuit. Defaults to 1.
	SuccessThreshold int `json:"success_threshold" yaml:"success_threshold"`
	// MaxHalfThreshold is the maximum number of concurrent in-flight probes
	// allowed while the circuit is half-open. Zero or negative values default to 1.
	MaxHalfThreshold int `json:"max_half_threshold" yaml:"max_half_threshold"`
	// Timeout is the duration the circuit stays open before transitioning to
	// half-open (e.g. "30s"). Defaults to "30s".
	Timeout string `json:"timeout" yaml:"timeout"`
}

// PluginConfig holds plugin configuration. String values in Config may
// reference environment variables using ${VAR} — only the braced form is a
// reference, a bare $ is literal data, and an undefined variable is an error.
// References are resolved when the plugin is constructed, not when the config
// is loaded, so a secret never reaches the config-history store.
type PluginConfig struct {
	Name    string         `json:"name" yaml:"name"`
	Type    string         `json:"type" yaml:"type"`
	Stage   string         `json:"stage" yaml:"stage"`
	Enabled bool           `json:"enabled" yaml:"enabled"`
	Config  map[string]any `json:"config" yaml:"config"`
}
