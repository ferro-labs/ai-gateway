// Package main demonstrates sending a request directly to any configured LLM provider.
//
// Set at least one provider key and run:
//
// OPENAI_API_KEY=sk-...       go run ./examples/basic
// ANTHROPIC_API_KEY=sk-ant-... go run ./examples/basic
// GROQ_API_KEY=gsk_...        go run ./examples/basic
// # (any of the 8 supported provider keys work)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"

	anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"
	coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"
	deepseekpkg "github.com/ferro-labs/ai-gateway/providers/deepseek"
	geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"
	groqpkg "github.com/ferro-labs/ai-gateway/providers/groq"
	mistralpkg "github.com/ferro-labs/ai-gateway/providers/mistral"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
	togetherpkg "github.com/ferro-labs/ai-gateway/providers/together"
)

func main() {
	// Pick the first provider that has a key configured.
	provider := firstProvider()

	model := provider.SupportedModels()[0]
	req := providers.Request{
		Model: model,
		Messages: []providers.Message{
			{Role: "user", Content: "Hello, tell me a short joke about programming."},
		},
	}

	if err := req.Validate(); err != nil {
		log.Fatalf("Invalid request: %v", err)
	}

	fmt.Printf("Provider: %s  Model: %s\n", provider.Name(), model)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Complete(ctx, req)
	if err != nil {
		cancel()
		log.Printf("Request failed: %v", err)
		os.Exit(1) //nolint:gocritic
	}

	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
}

// firstProvider returns the first provider for which an API key is set.
func firstProvider() providers.Provider {
	type entry struct {
		env    string
		create func(key string) (providers.Provider, error)
	}
	candidates := []entry{
		{"OPENAI_API_KEY", func(k string) (providers.Provider, error) { return openaipkg.New(k, "") }},
		{"ANTHROPIC_API_KEY", func(k string) (providers.Provider, error) { return anthropicpkg.New(k, "") }},
		{"GROQ_API_KEY", func(k string) (providers.Provider, error) { return groqpkg.New(k, "") }},
		{"GEMINI_API_KEY", func(k string) (providers.Provider, error) { return geminipkg.New(k, "") }},
		{"MISTRAL_API_KEY", func(k string) (providers.Provider, error) { return mistralpkg.New(k, "") }},
		{"TOGETHER_API_KEY", func(k string) (providers.Provider, error) { return togetherpkg.New(k, "") }},
		{"COHERE_API_KEY", func(k string) (providers.Provider, error) { return coherepkg.New(k, "") }},
		{"DEEPSEEK_API_KEY", func(k string) (providers.Provider, error) { return deepseekpkg.New(k, "") }},
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
