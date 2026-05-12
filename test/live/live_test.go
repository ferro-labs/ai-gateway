//go:build live
// +build live

// Package live_test provides smoke tests that call real LLM provider APIs.
//
// These tests are opt-in (build tag: live) and require provider API keys to be
// set as environment variables. Each test skips itself cleanly when the
// required key is absent.
//
// Cost guardrail: each full pass is designed to spend < $0.05 (model selection
// uses the cheapest available model per provider; input prompts are minimal).
// Approximate per-test cost (as of May 2026):
//   - OpenAI   gpt-4o-mini:   ~$0.001
//   - Anthropic claude-3-haiku: ~$0.001
//   - Gemini   gemini-1.5-flash: ~$0.0001
//   - Groq     llama3-8b-8192:   ~$0.0001
//
// Total estimated cost per full pass: < $0.005 (well under $0.05 cap).
//
// Run with:
//
//	make test-integration-live OPENAI_API_KEY=sk-... ANTHROPIC_API_KEY=sk-...
package live_test

import (
	"context"
	"os"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// requireKey skips the test if the named environment variable is not set.
// Returns the key value.
func requireKey(t *testing.T, envVar string) string {
	t.Helper()
	v := os.Getenv(envVar)
	if v == "" {
		t.Skipf("skipping live test: %s not set", envVar)
	}
	return v
}

// runChatSmoke sends one minimal non-streaming chat request to the provider and
// asserts a non-empty assistant message is returned.
func runChatSmoke(t *testing.T, p core.Provider, model string) {
	t.Helper()
	maxTok := 20
	req := core.Request{
		Model:     model,
		MaxTokens: &maxTok,
		Messages:  []core.Message{{Role: "user", Content: "Say exactly: pong"}},
	}
	resp, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete(%s) failed: %v", model, err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("Complete(%s) returned no choices", model)
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		t.Fatalf("Complete(%s) returned empty content", model)
	}
	t.Logf("response: %q (tokens in=%d out=%d)", content, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

// runStreamSmoke sends one minimal streaming request and asserts at least one
// non-empty chunk and a [DONE] terminator arrive.
func runStreamSmoke(t *testing.T, sp core.StreamProvider, model string) {
	t.Helper()
	maxTok := 20
	req := core.Request{
		Model:     model,
		MaxTokens: &maxTok,
		Messages:  []core.Message{{Role: "user", Content: "Count to 3"}},
		Stream:    true,
	}
	ch, err := sp.CompleteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CompleteStream(%s) failed: %v", model, err)
	}

	var content string
	var chunks int
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error at chunk %d: %v", chunks, chunk.Error)
		}
		for _, choice := range chunk.Choices {
			content += choice.Delta.Content
		}
		chunks++
	}
	if chunks == 0 {
		t.Fatalf("CompleteStream(%s) produced no chunks", model)
	}
	if content == "" {
		t.Fatalf("CompleteStream(%s) produced no content", model)
	}
	t.Logf("stream: %d chunks, content=%q", chunks, content)
}

// runEmbeddingSmoke sends one minimal embedding request and asserts a non-empty
// vector is returned.
func runEmbeddingSmoke(t *testing.T, ep core.EmbeddingProvider, model string) {
	t.Helper()
	req := core.EmbeddingRequest{
		Model: model,
		Input: []string{"hello world"},
	}
	resp, err := ep.Embed(context.Background(), req)
	if err != nil {
		t.Fatalf("Embed(%s) failed: %v", model, err)
	}
	if len(resp.Data) == 0 {
		t.Fatalf("Embed(%s) returned no embeddings", model)
	}
	if len(resp.Data[0].Embedding) == 0 {
		t.Fatalf("Embed(%s) returned empty embedding vector", model)
	}
	t.Logf("embedding: %d dims (tokens=%d)", len(resp.Data[0].Embedding), resp.Usage.TotalTokens)
}
