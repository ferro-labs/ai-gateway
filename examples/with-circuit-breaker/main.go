// Package main demonstrates per-provider circuit breaker configuration.
//
// A circuit breaker wraps each provider target. After a configurable number of
// consecutive failures it "opens" (blocks requests to that provider for a
// cooldown period) and then moves to "half-open" to probe recovery.
//
// This example sets up a two-provider fallback chain. The primary provider has
// an aggressive circuit breaker (threshold 2, 10 s timeout) so you can see the
// state machine in action when the provider is unavailable.
//
// OPENAI_API_KEY=sk-... ANTHROPIC_API_KEY=sk-ant-... go run ./examples/with-circuit-breaker
// GROQ_API_KEY=gsk-...  MISTRAL_API_KEY=...          go run ./examples/with-circuit-breaker
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
)

func main() {
	configured := configuredProviders()
	if len(configured) == 0 {
		log.Fatal("No provider key set. Set at least one of: OPENAI_API_KEY, ANTHROPIC_API_KEY, GROQ_API_KEY, GEMINI_API_KEY, MISTRAL_API_KEY, TOGETHER_API_KEY, COHERE_API_KEY, DEEPSEEK_API_KEY")
	}

	// Build targets. Primary gets a circuit breaker; secondary is the fallback.
	targets := make([]aigateway.Target, len(configured))
	for i, p := range configured {
		targets[i] = aigateway.Target{VirtualKey: p.Name()}
	}

	// Aggressive circuit breaker on the primary: open after 2 consecutive
	// failures, stay open for 10 s, then allow one probe in half-open state.
	targets[0].CircuitBreaker = &aigateway.CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          "10s",
	}
	// Also retry up to 2 times on the primary before the circuit opens.
	targets[0].Retry = &aigateway.RetryConfig{Attempts: 2}

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  targets,
	})
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}
	for _, p := range configured {
		gw.RegisterProvider(p)
		fmt.Printf("Registered: %s (models: %d)\n", p.Name(), len(p.SupportedModels()))
	}

	model := configured[0].SupportedModels()[0]
	req := providers.Request{
		Model: model,
		Messages: []providers.Message{
			{Role: "user", Content: "Say hello in one sentence."},
		},
		MaxTokens: intPtr(30),
	}

	fmt.Printf("\nPrimary provider : %s  (circuit breaker: threshold=2, timeout=10s)\n", configured[0].Name())
	if len(configured) > 1 {
		fmt.Printf("Fallback provider: %s\n", configured[1].Name())
	}
	fmt.Printf("Model            : %s\n\n", model)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Normal request — circuit is closed, routes to primary provider.
	fmt.Println("--- Request 1: normal (circuit closed) ---")
	resp, err := gw.Route(ctx, req)
	if err != nil {
		fmt.Printf("Failed: %v\n\n", err)
	} else {
		out, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(out))
		fmt.Println()
	}

	// Tip: to observe the circuit opening, run the gateway with an invalid
	// primary API key (e.g. OPENAI_API_KEY=invalid) alongside a valid fallback
	// key. After 2 failures the primary circuit opens and all subsequent
	// requests route directly to the fallback until the 10 s cooldown expires.
	fmt.Println("Tip: set the primary key to an invalid value and rerun — after")
	fmt.Println("     2 failures the circuit opens and the fallback takes over.")
}

func intPtr(i int) *int { return &i }

// configuredProviders returns a provider instance for every key that is set.
func configuredProviders() []providers.Provider {
	type entry struct {
		env    string
		create func(key string) (providers.Provider, error)
	}
	candidates := []entry{
		{"OPENAI_API_KEY", func(k string) (providers.Provider, error) { return providers.NewOpenAI(k, "") }},
		{"ANTHROPIC_API_KEY", func(k string) (providers.Provider, error) { return providers.NewAnthropic(k, "") }},
		{"GROQ_API_KEY", func(k string) (providers.Provider, error) { return providers.NewGroq(k, "") }},
		{"GEMINI_API_KEY", func(k string) (providers.Provider, error) { return providers.NewGemini(k, "") }},
		{"MISTRAL_API_KEY", func(k string) (providers.Provider, error) { return providers.NewMistral(k, "") }},
		{"TOGETHER_API_KEY", func(k string) (providers.Provider, error) { return providers.NewTogether(k, "") }},
		{"COHERE_API_KEY", func(k string) (providers.Provider, error) { return providers.NewCohere(k, "") }},
		{"DEEPSEEK_API_KEY", func(k string) (providers.Provider, error) { return providers.NewDeepSeek(k, "") }},
	}
	var result []providers.Provider
	for _, c := range candidates {
		if key := os.Getenv(c.env); key != "" {
			p, err := c.create(key)
			if err != nil {
				log.Fatalf("Failed to create provider for %s: %v", c.env, err)
			}
			result = append(result, p)
		}
	}
	return result
}
