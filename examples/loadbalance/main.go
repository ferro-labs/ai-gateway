// Package main demonstrates weighted load balancing across multiple providers.
//
// Requests are distributed probabilistically by weight. Requires at least two
// provider keys to be set.
//
// OPENAI_API_KEY=sk-... ANTHROPIC_API_KEY=sk-ant-... go run ./examples/loadbalance
// GROQ_API_KEY=gsk-...  MISTRAL_API_KEY=...           go run ./examples/loadbalance
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
	if len(configured) < 2 {
		log.Fatal("Load balance requires at least 2 provider keys. Set any two of: OPENAI_API_KEY, ANTHROPIC_API_KEY, GROQ_API_KEY, GEMINI_API_KEY, MISTRAL_API_KEY, TOGETHER_API_KEY, COHERE_API_KEY, DEEPSEEK_API_KEY")
	}

	// Assign weights: first provider gets 70%, rest split the remaining 30%.
	targets := make([]aigateway.Target, len(configured))
	targets[0] = aigateway.Target{VirtualKey: configured[0].Name(), Weight: 70}
	remaining := 30.0 / float64(len(configured)-1)
	for i := 1; i < len(configured); i++ {
		targets[i] = aigateway.Target{VirtualKey: configured[i].Name(), Weight: remaining}
	}

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeLoadBalance},
		Targets:  targets,
	})
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}

	weightInfo := ""
	for i, p := range configured {
		gw.RegisterProvider(p)
		weightInfo += fmt.Sprintf("  %s (weight=%.0f)\n", p.Name(), targets[i].Weight)
	}
	fmt.Printf("Load balancing across:\n%s\n", weightInfo)

	model := configured[0].SupportedModels()[0]
	fmt.Printf("Sending 5 requests (model=%s)...\n\n", model)

	for i := 1; i <= 5; i++ {
		req := providers.Request{
			Model: model,
			Messages: []providers.Message{
				{Role: "user", Content: fmt.Sprintf("Say 'request %d ok' and nothing else.", i)},
			},
			MaxTokens: intPtr(20),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := gw.Route(ctx, req)
		cancel()

		if err != nil {
			fmt.Printf("  Request %d: ERROR %v\n", i, err)
			continue
		}

		out, _ := json.Marshal(map[string]string{
			"id":       resp.ID,
			"provider": resp.Provider,
			"model":    resp.Model,
			"reply":    resp.Choices[0].Message.Content,
		})
		fmt.Printf("  Request %d: %s\n", i, out)
	}
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
