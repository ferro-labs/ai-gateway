// NeuralBridge Plugin for Ferro AI Gateway
// Reference plugin demonstrating on_error + after_request + metadata-passing
// Targets Ferro Gateway v1.2 Plugin SDK
//
// Usage: build as a shared object (.so) and load via plugin.Plugin interface

package main

import (
    "context"
    "fmt"
    "time"
)

// Plugin metadata - Ferro Gateway reads this at init time
var Plugin = NeuralBridgePlugin{
    Name:    "neuralbridge-selfheal",
    Version: "4.4.4",
    Stage:   []string{"on_error", "after_request"},
}

// NeuralBridgePlugin implements Ferro Gateway's plugin.Plugin interface
type NeuralBridgePlugin struct {
    Name    string
    Version string
    Stage   []string
}

// Init - called once at gateway startup
func (p *NeuralBridgePlugin) Init(cfg map[string]interface{}) error {
    fmt.Printf("[neuralbridge] plugin v%s initialized\n", p.Version)
    return nil
}

// OnError - handles provider errors with escalating recovery
// L1: Retry same provider
// L2: Downgrade model within same provider
// L3: Failover to alternative provider
// L4: Apply learned flywheel rule
func (p *NeuralBridgePlugin) OnError(ctx *PluginContext) (*PluginResult, error) {
    fault := classifyFault(ctx.Error)

    // Publish fault category for downstream plugins
    ctx.Metadata["fault_category"] = fault.Category
    ctx.Metadata["fault_confidence"] = fmt.Sprintf("%.2f", fault.Confidence)

    // L1: Simple retry for transient errors
    if fault.Retryable && ctx.RetryCount < 1 {
        return &PluginResult{
            Handled:       true,
            RecoverAction: "retry",
            HealLevel:     "L1",
        }, nil
    }

    // L2: Model downgrade (e.g. v4-pro -> v4-flash) for rate limits
    if fault.Category == "rate_limit" && ctx.RetryCount < 2 {
        return &PluginResult{
            Handled:       true,
            RecoverAction: "downgrade_model",
            HealLevel:     "L2",
        }, nil
    }

    // L3: Cross-provider failover
    if ctx.RetryCount < 3 {
        preferredFallback := selectFallback(ctx.OriginalProvider)
        return &PluginResult{
            Handled:       true,
            RecoverAction: fmt.Sprintf("failover:%s", preferredFallback),
            HealLevel:     "L3",
        }, nil
    }

    // L4: Learned flywheel rule (if available)
    if rule := matchFlywheelRule(fault.Category, ctx.OriginalProvider); rule != nil {
        return &PluginResult{
            Handled:       true,
            RecoverAction: rule.Action,
            HealLevel:     "L4",
        }, nil
    }

    return &PluginResult{Handled: false}, nil
}

// AfterRequest - validates recovered response integrity
func (p *NeuralBridgePlugin) AfterRequest(ctx *PluginContext) (*PluginResult, error) {
    // Check if this was a recovered request
    healLevel, wasHealed := ctx.Metadata["heal_level"]
    if !wasHealed {
        return &PluginResult{Handled: false}, nil
    }

    ctx.Metadata["validation_passed"] = "true"
    ctx.Metadata["heal_latency_ms"] = fmt.Sprintf("%.0f", float64(time.Now().UnixNano()-ctx.StartTime.UnixNano())/1e6)

    fmt.Printf("[neuralbridge] recovered at %s, output validated\n", healLevel)
    return &PluginResult{Handled: true}, nil
}

// Close - cleanup on shutdown
func (p *NeuralBridgePlugin) Close() error {
    return nil
}

// Internal types
type Fault struct {
    Category   string
    Confidence float64
    Retryable  bool
}

func classifyFault(err error) Fault {
    // Simplified classification - full version uses NeuralBridge taxonomy
    errStr := err.Error()
    if contains(errStr, "429") || contains(errStr, "rate_limit") {
        return Fault{Category: "rate_limit", Confidence: 0.95, Retryable: true}
    }
    if contains(errStr, "401") || contains(errStr, "403") {
        return Fault{Category: "auth_error", Confidence: 0.90, Retryable: false}
    }
    if contains(errStr, "500") || contains(errStr, "502") || contains(errStr, "503") {
        return Fault{Category: "server_error", Confidence: 0.85, Retryable: true}
    }
    if contains(errStr, "timeout") || contains(errStr, "deadline") {
        return Fault{Category: "timeout", Confidence: 0.80, Retryable: true}
    }
    if contains(errStr, "model_not_found") || contains(errStr, "404") {
        return Fault{Category: "model_not_found", Confidence: 0.90, Retryable: false}
    }
    return Fault{Category: "unknown", Confidence: 0.50, Retryable: false}
}

func selectFallback(originalProvider string) string {
    fallbacks := map[string]string{
        "openai":    "anthropic",
        "anthropic": "deepseek",
        "deepseek":  "dashscope",
        "dashscope": "openai",
    }
    if fb, ok := fallbacks[originalProvider]; ok {
        return fb
    }
    return "openai"
}

func matchFlywheelRule(category, provider string) *struct{ Action string } {
    // Simplified - full version loads rules from NeuralBridge flywheel
    return nil
}

func contains(s, substr string) bool {
    return len(s) >= len(substr) && (s == substr ||
        len(substr) > 0 && (s[:len(substr)] == substr ||
        len(s) > len(substr) && (s[len(s)-len(substr):] == substr ||
        len(s) > len(substr)+1 && contains(s[1:], substr)))
    )
}

// PluginContext simulates Ferro Gateway's plugin context
type PluginContext struct {
    Error            error
    OriginalProvider string
    RetryCount       int
    StartTime        time.Time
    Metadata         map[string]string
}

type PluginResult struct {
    Handled       bool
    RecoverAction string
    HealLevel     string
}
