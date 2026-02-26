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
}

// RetryConfig defines retry behavior.
type RetryConfig struct {
	Attempts int `json:"attempts" yaml:"attempts"`
}

// PluginConfig holds plugin configuration.
type PluginConfig struct {
	Name    string                 `json:"name" yaml:"name"`
	Type    string                 `json:"type" yaml:"type"`
	Stage   string                 `json:"stage" yaml:"stage"`
	Enabled bool                   `json:"enabled" yaml:"enabled"`
	Config  map[string]interface{} `json:"config" yaml:"config"`
}
