package aigateway

// Config holds the configuration for the AI Gateway.
type Config struct {
	// Strategy defines how requests are routed (e.g., single, fallback, loadbalance).
	Strategy StrategyConfig `json:"strategy" yaml:"strategy"`
	// Targets is a list of provider targets to route requests to.
	Targets []Target `json:"targets" yaml:"targets"`
	// Plugins configuration (optional).
	Plugins []PluginConfig `json:"plugins,omitempty" yaml:"plugins,omitempty"`
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
	ModeSingle      StrategyMode = "single"
	ModeFallback    StrategyMode = "fallback"
	ModeLoadBalance StrategyMode = "loadbalance"
	ModeConditional StrategyMode = "conditional"
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

// RetryConfig defines retry behavior.
type RetryConfig struct {
	Attempts int `json:"attempts" yaml:"attempts"`
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
