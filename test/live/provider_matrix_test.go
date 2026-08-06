//go:build live

package live_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

func liveMatrixProvider(t *testing.T) (core.Provider, providers.ProviderEntry) {
	t.Helper()

	providerID := os.Getenv("LIVE_PROVIDER")
	if providerID == "" {
		t.Skip("LIVE_PROVIDER not set; the matrix harness runs one provider at a time")
	}
	entry, ok := providers.GetProviderEntry(providerID)
	if !ok {
		t.Fatalf("LIVE_PROVIDER names an unknown provider")
	}
	cfg := providers.ProviderConfigFromEnv(entry)
	if cfg == nil {
		t.Fatalf("%s credential set is incomplete", providerID)
	}
	p, err := entry.Build(cfg)
	if err != nil {
		t.Fatalf("construct %s provider: %s", providerID, liveErrorClass(err))
	}
	if p.Name() != providerID {
		t.Fatalf("constructed provider name mismatch")
	}
	return p, entry
}

func liveErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	if status := core.ParseStatusCode(err); status != 0 {
		return fmt.Sprintf("http_%d", status)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return fmt.Sprintf("%T", err)
	}
}

func liveTimeout(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), timeout)
}

func TestLiveMatrix_Construct(t *testing.T) {
	p, entry := liveMatrixProvider(t)
	t.Logf("provider=%s capabilities=%v constructed=true", p.Name(), entry.Capabilities)
}

func TestLiveMatrix_Discovery(t *testing.T) {
	p, _ := liveMatrixProvider(t)
	discovery, ok := p.(core.DiscoveryProvider)
	if !ok {
		t.Skip("provider does not implement discovery")
	}

	ctx, cancel := liveTimeout(t, 30*time.Second)
	defer cancel()
	got, err := discovery.DiscoverModels(ctx)
	if err != nil {
		t.Fatalf("discovery failed: %s", liveErrorClass(err))
	}
	if len(got) == 0 {
		t.Fatal("discovery returned no models")
	}
	ids := make([]string, 0, len(got))
	for _, model := range got {
		if model.ID != "" {
			ids = append(ids, model.ID)
		}
	}
	sort.Strings(ids)
	t.Logf("provider=%s discovered_model_count=%d model_ids=%q", p.Name(), len(ids), ids)
}

func TestLiveMatrix_Chat(t *testing.T) {
	p, _ := liveMatrixProvider(t)
	model := os.Getenv("LIVE_CHAT_MODEL")
	if model == "" {
		t.Skip("LIVE_CHAT_MODEL not set")
	}
	maxTokens := 16
	ctx, cancel := liveTimeout(t, 30*time.Second)
	defer cancel()
	resp, err := p.Complete(ctx, core.Request{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages:  []core.Message{{Role: "user", Content: "Say exactly: pong"}},
	})
	if err != nil {
		t.Fatalf("chat failed: %s", liveErrorClass(err))
	}
	if len(resp.Choices) == 0 {
		t.Fatal("chat returned no choices")
	}
	message := resp.Choices[0].Message
	if message.Content == "" && message.ReasoningContent == "" && len(message.ToolCalls) == 0 {
		t.Fatal("chat returned no normalized output")
	}
	t.Logf("provider=%s model=%s content_bytes=%d reasoning_bytes=%d tool_calls=%d tokens_in=%d tokens_out=%d",
		p.Name(), model, len(message.Content), len(message.ReasoningContent), len(message.ToolCalls),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

func TestLiveMatrix_Stream(t *testing.T) {
	p, _ := liveMatrixProvider(t)
	streamer, ok := p.(core.StreamProvider)
	if !ok {
		t.Fatal("provider does not implement streaming")
	}
	model := os.Getenv("LIVE_CHAT_MODEL")
	if model == "" {
		t.Skip("LIVE_CHAT_MODEL not set")
	}
	maxTokens := 16
	ctx, cancel := liveTimeout(t, 60*time.Second)
	defer cancel()
	chunks, err := streamer.CompleteStream(ctx, core.Request{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages:  []core.Message{{Role: "user", Content: "Count to 3"}},
		Stream:    true,
	})
	if err != nil {
		t.Fatalf("stream failed: %s", liveErrorClass(err))
	}
	var chunkCount, contentBytes, reasoningBytes, toolCalls int
	for chunk := range chunks {
		if chunk.Error != nil {
			t.Fatalf("stream chunk failed: %s", liveErrorClass(chunk.Error))
		}
		for _, choice := range chunk.Choices {
			contentBytes += len(choice.Delta.Content)
			reasoningBytes += len(choice.Delta.ReasoningContent)
			toolCalls += len(choice.Delta.ToolCalls)
		}
		chunkCount++
	}
	if chunkCount == 0 || contentBytes+reasoningBytes+toolCalls == 0 {
		t.Fatal("stream returned no normalized output")
	}
	t.Logf("provider=%s model=%s chunks=%d content_bytes=%d reasoning_bytes=%d tool_calls=%d",
		p.Name(), model, chunkCount, contentBytes, reasoningBytes, toolCalls)
}

func TestLiveMatrix_Embedding(t *testing.T) {
	p, _ := liveMatrixProvider(t)
	embedder, ok := p.(core.EmbeddingProvider)
	if !ok {
		t.Fatal("provider does not implement embeddings")
	}
	model := os.Getenv("LIVE_EMBED_MODEL")
	if model == "" {
		t.Skip("LIVE_EMBED_MODEL not set")
	}
	ctx, cancel := liveTimeout(t, 30*time.Second)
	defer cancel()
	resp, err := embedder.Embed(ctx, core.EmbeddingRequest{
		Model:     model,
		Input:     "hello world",
		InputType: os.Getenv("LIVE_EMBED_INPUT_TYPE"),
	})
	if err != nil {
		t.Fatalf("embedding failed: %s", liveErrorClass(err))
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		t.Fatal("embedding returned no vector")
	}
	t.Logf("provider=%s model=%s vectors=%d dimensions=%d tokens=%d",
		p.Name(), model, len(resp.Data), len(resp.Data[0].Embedding), resp.Usage.TotalTokens)
}

func TestLiveMatrix_Image(t *testing.T) {
	if os.Getenv("ALLOW_IMAGE_CALLS") != "true" {
		t.Skip("ALLOW_IMAGE_CALLS=true not set; image generation is opt-in (it costs money)")
	}
	p, _ := liveMatrixProvider(t)
	generator, ok := p.(core.ImageProvider)
	if !ok {
		t.Skip("provider does not implement image generation")
	}
	model := os.Getenv("LIVE_IMAGE_MODEL")
	if model == "" {
		t.Skip("LIVE_IMAGE_MODEL not set")
	}
	one := 1
	ctx, cancel := liveTimeout(t, 120*time.Second)
	defer cancel()
	resp, err := generator.GenerateImage(ctx, core.ImageRequest{
		Model:   model,
		Prompt:  "A single black dot centered on a plain white background.",
		N:       &one,
		Size:    os.Getenv("LIVE_IMAGE_SIZE"),
		Quality: os.Getenv("LIVE_IMAGE_QUALITY"),
	})
	if err != nil {
		t.Fatalf("image generation failed: %s", liveErrorClass(err))
	}
	if len(resp.Data) == 0 {
		t.Fatal("image generation returned no images")
	}
	var urls, base64Items, encodedBytes int
	for _, item := range resp.Data {
		if item.URL == "" && item.B64JSON == "" {
			t.Fatal("image result has neither URL nor base64 data")
		}
		if item.URL != "" {
			urls++
		}
		if item.B64JSON != "" {
			base64Items++
			encodedBytes += len(item.B64JSON)
		}
	}
	t.Logf("provider=%s model=%s images=%d urls=%d base64_items=%d encoded_bytes=%d tokens=%d",
		p.Name(), model, len(resp.Data), urls, base64Items, encodedBytes, resp.Usage.TotalTokens)
}
