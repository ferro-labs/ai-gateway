// Package main demonstrates gateway event hooks.
//
// Hooks are functions registered via gw.AddHook(fn) that receive gateway
// events asynchronously after every request. Two subjects are emitted:
//
//   - gateway.request.completed — successful response with latency and token counts
//   - gateway.request.failed    — error with latency and error message
//
// Hooks are useful for custom telemetry, audit logging, cost tracking, or
// sending events to an external system without modifying the core routing path.
//
// OPENAI_API_KEY=sk-...        go run ./examples/with-hooks
// ANTHROPIC_API_KEY=sk-ant-... go run ./examples/with-hooks
// # (any of the 8 supported provider keys work)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
)

func main() {
	provider := firstProvider()
	model := provider.SupportedModels()[0]

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: provider.Name()}},
	})
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}
	gw.RegisterProvider(provider)

	// eventLog collects hook payloads so we can print them after routing.
	var (
		mu       sync.Mutex
		received []hookEvent
	)

	// Register a hook. The function is called in a goroutine after each
	// request so it must not block the caller.
	gw.AddHook(func(_ context.Context, subject string, data map[string]interface{}) {
		mu.Lock()
		received = append(received, hookEvent{Subject: subject, Data: data})
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("Provider: %s  Model: %s\n\n", provider.Name(), model)

	// --- Request 1: successful ---
	fmt.Println("--- Request 1: successful request ---")
	resp, err := gw.Route(ctx, providers.Request{
		Model: model,
		Messages: []providers.Message{
			{Role: "user", Content: "Reply with exactly three words."},
		},
		MaxTokens: intPtr(20),
	})
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		out, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(out))
	}

	// --- Request 2: validation failure (no messages) ---
	fmt.Println("\n--- Request 2: invalid request (empty messages) ---")
	_, err = gw.Route(ctx, providers.Request{
		Model:    model,
		Messages: []providers.Message{},
	})
	if err != nil {
		fmt.Printf("Error (expected): %v\n", err)
	}

	// Hooks run asynchronously; give them a moment to fire before printing.
	time.Sleep(100 * time.Millisecond)

	// Print every event the hooks received.
	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("\n--- Hook events received: %d ---\n", len(received))
	for i, e := range received {
		out, _ := json.MarshalIndent(e, "", "  ")
		fmt.Printf("\nEvent %d:\n%s\n", i+1, string(out))
	}
}

type hookEvent struct {
	Subject string                 `json:"subject"`
	Data    map[string]interface{} `json:"data"`
}

func intPtr(i int) *int { return &i }

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
