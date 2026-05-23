package aigateway

import "github.com/ferro-labs/ai-gateway/mcp"

// Config holds the configuration for the AI Gateway.
type Config struct {
	// Strategy defines how requests are routed (e.g., single, fallback, loadbalance).
	Strategy StrategyConfig `json:"strategy" yaml:"strategy"`
	// Targets is a list of provider targets to route requests to.
	Targets []Target `json:"targets" yaml:"targets"`
	// Plugins configuration (optional).
	Plugins []PluginConfig `json:"plugins,omitempty" yaml:"plugins,omitempty"`
	// Aliases maps friendly model names (e.g. "fast", "smart") to concrete model IDs.
	// Aliases are resolved before routing — they must not reference other aliases.
	Aliases map[string]string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	// MCPServers configures external MCP tool servers for agentic tool calling.
	// When set, the gateway injects discovered tools into every chat completion
	// request and executes an agentic loop when the LLM returns tool_calls.
	// FerroCloud populates this field from the tenant's mcp_servers table at
	// gateway.New() time — no separate MCPRegistry() public method is exposed.
	MCPServers []mcp.ServerConfig `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`
	// MCPToolCallAuditFn, if non-nil, is called after every MCP tool invocation.
	// This field cannot be set via JSON or YAML — set it programmatically before
	// calling New. FerroCloud uses it to write async audit entries to the
	// mcp_tool_call_logs table.
	MCPToolCallAuditFn mcp.ToolCallAuditFn `json:"-" yaml:"-"`
	// Observability configures OpenTelemetry tracing. When omitted the
	// gateway runs with a NoOp provider (zero allocations on the hot
	// path). See internal/otel.
	Observability ObservabilityConfig `json:"observability,omitempty" yaml:"observability,omitempty"`
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
	// Config is the exporter-specific configuration map. Passed
	// verbatim to Exporter.Init at gateway startup.
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// TracingConfig configures the OTLP tracing pipeline. All fields are
// optional; sensible defaults apply when omitted (see
// internal/otel.DefaultConfig).
type TracingConfig struct {
	// Enabled is the master switch. Defaults to true; the pipeline
	// still short-circuits to NoOp when no OTLP endpoint is configured.
	Enabled bool `json:"enabled" yaml:"enabled"`
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
	// ShutdownGrace is the maximum time the gateway waits for in-flight
	// OTel exports to drain during graceful shutdown. Accepts any Go
	// duration string, e.g. "10s", "500ms". Defaults to 10s when empty
	// or unparseable.
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
}

// StrategyConfig defines the routing strategy.
type StrategyConfig struct {
	Mode       StrategyMode `json:"mode" yaml:"mode"`
	Conditions []Condition  `json:"conditions,omitempty" yaml:"conditions,omitempty"` // For conditional routing
	// ContentConditions defines rules for the content-based routing strategy.
	// Rules are evaluated in order; the first match wins.
	ContentConditions []ContentCondition `json:"content_conditions,omitempty" yaml:"content_conditions,omitempty"`
	// ABVariants defines the weighted variants for the ab-test strategy.
	ABVariants []ABVariantConfig `json:"ab_variants,omitempty" yaml:"ab_variants,omitempty"`
}

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

// Condition represents a condition for conditional routing.
type Condition struct {
	Key       string `json:"key" yaml:"key"`
	Value     string `json:"value" yaml:"value"`
	TargetKey string `json:"target_key" yaml:"target_key"`
}

// ContentCondition maps a prompt-content matching rule to a routing target.
// Used with the "content-based" strategy mode.
//
// Supported types:
//   - "prompt_contains"     — case-insensitive substring match on user messages
//   - "prompt_not_contains" — true when NO user message contains the value
//   - "prompt_regex"        — Go regular expression match on user messages
type ContentCondition struct {
	// Type is the matching rule type.
	Type string `json:"type" yaml:"type"`
	// Value is the substring or regex pattern to match against.
	Value string `json:"value" yaml:"value"`
	// TargetKey is the virtual_key of the provider to route to when this rule matches.
	TargetKey string `json:"target_key" yaml:"target_key"`
}

// ABVariantConfig defines a single traffic variant for the "ab-test" strategy.
type ABVariantConfig struct {
	// TargetKey is the virtual_key of the provider for this variant.
	TargetKey string `json:"target_key" yaml:"target_key"`
	// Weight is the relative traffic share for this variant.
	// All weights are summed; each variant's fraction is Weight/Total.
	// Zero is treated as 1 (equal distribution).
	Weight float64 `json:"weight" yaml:"weight"`
	// Label is a short human-readable identifier (e.g. "control", "challenger").
	// It is logged with every routed request for observability.
	Label string `json:"label" yaml:"label"`
}

// Target represents a specific provider target.
type Target struct {
	// VirtualKey is the unique identifier for the provider (or a virtual key in the vault).
	VirtualKey string `json:"virtual_key" yaml:"virtual_key"`
	// Weight is used for load balancing.
	Weight float64 `json:"weight,omitempty" yaml:"weight,omitempty"`
	// Retry configuration for this target.
	Retry *RetryConfig `json:"retry,omitempty" yaml:"retry,omitempty"`
	// CircuitBreaker configuration for this target (optional).
	CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty" yaml:"circuit_breaker,omitempty"`
}

// RetryConfig defines retry behavior for the fallback strategy.
type RetryConfig struct {
	// Attempts is the maximum number of attempts per target (1 = no retries).
	Attempts int `json:"attempts" yaml:"attempts"`
	// OnStatusCodes, when non-empty, limits retries to the listed HTTP status
	// codes. A retry is skipped when the provider returns a code not in the
	// list, and the strategy moves on to the next target immediately.
	// Leave empty to retry on any error (default behaviour).
	// Example: [429, 502, 503]
	OnStatusCodes []int `json:"on_status_codes,omitempty" yaml:"on_status_codes,omitempty"`
	// InitialBackoffMs is the base backoff in milliseconds for the exponential
	// back-off formula: delay = InitialBackoffMs * 2^(attempt-1).
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
	// Timeout is the duration the circuit stays open before transitioning to
	// half-open (e.g. "30s"). Defaults to "30s".
	Timeout string `json:"timeout" yaml:"timeout"`
}

// PluginConfig holds plugin configuration.
type PluginConfig struct {
	Name    string                 `json:"name" yaml:"name"`
	Type    string                 `json:"type" yaml:"type"`
	Stage   string                 `json:"stage" yaml:"stage"`
	Enabled bool                   `json:"enabled" yaml:"enabled"`
	Config  map[string]interface{} `json:"config" yaml:"config"`
}
