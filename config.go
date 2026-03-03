package aigateway

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
}

// StrategyConfig defines the routing strategy.
type StrategyConfig struct {
	Mode       StrategyMode `json:"mode" yaml:"mode"`
	Conditions []Condition  `json:"conditions,omitempty" yaml:"conditions,omitempty"` // For conditional routing
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
)

// Condition represents a condition for conditional routing.
type Condition struct {
	Key       string `json:"key" yaml:"key"`
	Value     string `json:"value" yaml:"value"`
	TargetKey string `json:"target_key" yaml:"target_key"`
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
