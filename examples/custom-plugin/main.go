// Package main demonstrates how to write and register a custom plugin with Ferro AI Gateway.
//
// This example:
//   - Implements the plugin.Plugin interface with a request-ID injector plugin
//   - Registers it programmatically (no config file needed)
//   - Routes a request through the gateway so the plugin executes
//
// OPENAI_API_KEY=sk-...        go run ./examples/custom-plugin
// ANTHROPIC_API_KEY=sk-ant-... go run ./examples/custom-plugin
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
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// requestIDPlugin stamps a trace ID onto every request via plugin.Context.Metadata.
type requestIDPlugin struct {
	prefix string
}

func (p *requestIDPlugin) Name() string            { return "request-id" }
func (p *requestIDPlugin) Type() plugin.PluginType { return plugin.TypeLogging }

func (p *requestIDPlugin) Init(config map[string]interface{}) error {
	if v, ok := config["prefix"].(string); ok && v != "" {
		p.prefix = v
	}
	return nil
}

func (p *requestIDPlugin) Execute(_ context.Context, pctx *plugin.Context) error {
	id := fmt.Sprintf("%s-%d", p.prefix, time.Now().UnixNano())
	pctx.Metadata["trace_id"] = id
	fmt.Printf("[request-id] trace_id=%s  model=%s\n", id, pctx.Request.Model)
	return nil
}

func main() {
	provider := firstProvider()
	model := provider.SupportedModels()[0]

	// 1. Create gateway with a single-provider strategy.
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: provider.Name()}},
	})
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}
	gw.RegisterProvider(provider)

	// 2. Instantiate and init the custom plugin.
	rp := &requestIDPlugin{}
	if err := rp.Init(map[string]interface{}{"prefix": "req"}); err != nil {
		log.Fatalf("Plugin init: %v", err)
	}

	// 3. Register it at the before_request stage.
	if err := gw.RegisterPlugin(plugin.StageBeforeRequest, rp); err != nil {
		log.Fatalf("Failed to register plugin: %v", err)
	}

	fmt.Printf("Provider: %s  Model: %s\n", provider.Name(), model)
	fmt.Println("Custom plugin registered. Routing request...")

	// 4. Route a request — the plugin fires automatically before the provider call.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := gw.Route(ctx, providers.Request{
		Model:     model,
		Messages:  []providers.Message{{Role: "user", Content: "Say 'plugin works!' and nothing else."}},
		MaxTokens: intPtr(10),
	})
	if err != nil {
		cancel()
		log.Printf("Route failed: %v", err)
		os.Exit(1) //nolint:gocritic
	}

	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
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
