// Package main demonstrates using built-in guardrail plugins.
//
// Guardrails run before the request reaches the provider: the word-filter
// rejects blocked phrases, and max-token enforces token/message limits.
//
// OPENAI_API_KEY=sk-...        go run ./examples/with-guardrails
// ANTHROPIC_API_KEY=sk-ant-... go run ./examples/with-guardrails
// # (any of the 8 supported provider keys work)
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

	// Register built-in plugins so they can be loaded from config.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

func main() {
	provider := firstProvider()
	model := provider.SupportedModels()[0]

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: provider.Name()}},
		Plugins: []aigateway.PluginConfig{
			{
				Name:    "word-filter",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config: map[string]interface{}{
					"blocked_words":  []string{"password", "secret", "api_key"},
					"case_sensitive": false,
				},
			},
			{
				Name:    "max-token",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config: map[string]interface{}{
					"max_tokens":   4096,
					"max_messages": 50,
				},
			},
		},
	})
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}
	gw.RegisterProvider(provider)
	if err := gw.LoadPlugins(); err != nil {
		log.Fatalf("Failed to load plugins: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("Provider: %s  Model: %s\n", provider.Name(), model)

	// This request passes through — no blocked words.
	fmt.Println("\n--- Request 1: clean message (should pass) ---")
	resp, err := gw.Route(ctx, providers.Request{
		Model:    model,
		Messages: []providers.Message{{Role: "user", Content: "Tell me a joke about Go programming."}},
	})
	if err != nil {
		fmt.Printf("Rejected: %v\n", err)
	} else {
		out, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(out))
	}

	// This request is blocked by the word-filter.
	fmt.Println("\n--- Request 2: blocked word 'password' (should be rejected) ---")
	_, err = gw.Route(ctx, providers.Request{
		Model:    model,
		Messages: []providers.Message{{Role: "user", Content: "What is a secure password strategy?"}},
	})
	if err != nil {
		fmt.Printf("Rejected: %v\n", err)
	} else {
		fmt.Println("Passed (word-filter may not block this phrasing)")
	}
}

// firstProvider returns the first provider for which an API key is set.
func firstProvider() providers.Provider {
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
	for _, c := range candidates {
		if key := os.Getenv(c.env); key != "" {
			p, err := c.create(key)
			if err != nil {
				log.Fatalf("Failed to create provider for %s: %v", c.env, err)
			}
			return p
		}
	}
	log.Fatal("No provider key set. Set at least one of: OPENAI_API_KEY, ANTHROPIC_API_KEY, GROQ_API_KEY, GEMINI_API_KEY, MISTRAL_API_KEY, TOGETHER_API_KEY, COHERE_API_KEY, DEEPSEEK_API_KEY")
	return nil
}
