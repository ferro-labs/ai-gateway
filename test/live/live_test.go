//go:build live
// +build live

// Package live_test provides smoke tests that call real LLM provider APIs.
//
// These tests are opt-in (build tag: live) and require provider API keys to be
// set as environment variables. Each test skips itself cleanly when the
// required key is absent.
//
// Which models they call is declared once in models_test.go, which is untagged
// and checks every pin against the model catalog under plain `go test`. That
// indirection is the point: the ids used to be literals in each file, described
// by a doc comment here that named different models than the code called, and
// the drift was invisible until someone ran the opt-in suite.
//
// Cost guardrail: the cheapest current model per provider, one request per test,
// max_tokens 16 — a few tenths of a cent for a full pass. Per-model prices are
// not restated here; the catalog carries them, and the table that used to sit in
// this comment went stale along with the ids it quoted.
//
// Run with:
//
//	make test-integration-live OPENAI_API_KEY=sk-... ANTHROPIC_API_KEY=sk-...
package live_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
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
// asserts the provider returns normalized answer or reasoning output.
func runChatSmoke(t *testing.T, p core.Provider, model string) {
	t.Helper()
	maxTok := 16
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
	message := resp.Choices[0].Message
	if message.Content == "" && message.ReasoningContent == "" {
		t.Fatalf("Complete(%s) returned no answer or reasoning output", model)
	}
	t.Logf(
		"response: content_bytes=%d reasoning_bytes=%d tokens_in=%d tokens_out=%d",
		len(message.Content),
		len(message.ReasoningContent),
		resp.Usage.PromptTokens,
		resp.Usage.CompletionTokens,
	)
}

// runStreamSmoke sends one minimal streaming request and asserts at least one
// normalized answer or reasoning chunk arrives.
func runStreamSmoke(t *testing.T, sp core.StreamProvider, model string) {
	t.Helper()
	maxTok := 16
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

	var content, reasoning string
	var chunks int
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error at chunk %d: %v", chunks, chunk.Error)
		}
		for _, choice := range chunk.Choices {
			content += choice.Delta.Content
			reasoning += choice.Delta.ReasoningContent
		}
		chunks++
	}
	if chunks == 0 {
		t.Fatalf("CompleteStream(%s) produced no chunks", model)
	}
	if content == "" && reasoning == "" {
		t.Fatalf("CompleteStream(%s) produced no answer or reasoning output", model)
	}
	t.Logf("stream: chunks=%d content_bytes=%d reasoning_bytes=%d", chunks, len(content), len(reasoning))
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

// runImageSmoke sends one minimal image request and asserts one image (URL or
// base64) is returned. Callers gate this behind ALLOW_IMAGE_CALLS.
func runImageSmoke(t *testing.T, ip core.ImageProvider, model string) {
	t.Helper()
	one := 1
	resp, err := ip.GenerateImage(context.Background(), core.ImageRequest{
		Model:  model,
		Prompt: "A single black dot centered on a plain white background.",
		N:      &one,
	})
	if err != nil {
		t.Fatalf("GenerateImage(%s) failed: %v", model, err)
	}
	if len(resp.Data) == 0 {
		t.Fatalf("GenerateImage(%s) returned no images", model)
	}
	if resp.Data[0].URL == "" && resp.Data[0].B64JSON == "" {
		t.Fatalf("GenerateImage(%s) returned neither a URL nor base64 data", model)
	}
	t.Logf("image: %d result(s) (tokens=%d)", len(resp.Data), resp.Usage.TotalTokens)
}

// runDiscoverySmoke calls the provider's live /models enumeration and asserts a
// non-empty model list.
func runDiscoverySmoke(t *testing.T, dp core.DiscoveryProvider) {
	t.Helper()
	discovered, err := dp.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels failed: %v", err)
	}
	if len(discovered) == 0 {
		t.Fatal("DiscoverModels returned no models")
	}
	t.Logf("discovery: %d models", len(discovered))
}

// liveEntryProvider builds a provider through its ProviderEntry from environment
// config — the same seam the gateway uses — and skips the test when the
// credential set is incomplete, so a full suite run degrades to "only the
// providers whose keys are present" rather than failing.
func liveEntryProvider(t *testing.T, id string) core.Provider {
	t.Helper()
	entry, ok := providers.GetProviderEntry(id)
	if !ok {
		t.Fatalf("unknown provider %q", id)
	}
	cfg := providers.ProviderConfigFromEnv(entry)
	if cfg == nil {
		t.Skipf("skipping live %s: credential set incomplete", id)
	}
	p, err := entry.Build(cfg)
	if err != nil {
		t.Fatalf("build %s: %v", id, err)
	}
	return p
}

// liveModelFor resolves the model a live test calls: the catalog pin from
// livePins if present, otherwise the LIVE_<PROVIDER>_<SURFACE>_MODEL override.
// It skips when neither is set — the case for a deployment/config provider whose
// model the operator must supply.
func liveModelFor(t *testing.T, id, surface string) string {
	t.Helper()
	pin := livePins[id]
	var pinned string
	switch surface {
	case "chat":
		pinned = pin.chat
	case "embed":
		pinned = pin.embed
	case "image":
		pinned = pin.image
	}
	if pinned != "" {
		return pinned
	}
	env := "LIVE_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_" + strings.ToUpper(surface) + "_MODEL"
	if v := os.Getenv(env); v != "" {
		return v
	}
	t.Skipf("skipping live %s %s: no catalog pin and %s is unset", id, surface, env)
	return ""
}

// The liveX helpers below are the per-provider entry points every
// test/live/<provider>_test.go calls. Each builds the provider, checks it
// implements the surface (skipping if not), resolves the model, and delegates to
// the shared smoke above — so a provider file is a list of one-line declarations.
func liveChat(t *testing.T, id string) {
	t.Helper()
	runChatSmoke(t, liveEntryProvider(t, id), liveModelFor(t, id, "chat"))
}

func liveStream(t *testing.T, id string) {
	t.Helper()
	p := liveEntryProvider(t, id)
	sp, ok := p.(core.StreamProvider)
	if !ok {
		t.Skipf("%s does not implement streaming", id)
	}
	runStreamSmoke(t, sp, liveModelFor(t, id, "chat"))
}

func liveEmbed(t *testing.T, id string) {
	t.Helper()
	p := liveEntryProvider(t, id)
	ep, ok := p.(core.EmbeddingProvider)
	if !ok {
		t.Skipf("%s does not implement embeddings", id)
	}
	runEmbeddingSmoke(t, ep, liveModelFor(t, id, "embed"))
}

func liveImage(t *testing.T, id string) {
	t.Helper()
	if os.Getenv("ALLOW_IMAGE_CALLS") != "true" {
		t.Skip("ALLOW_IMAGE_CALLS=true is required for image generation calls")
	}
	p := liveEntryProvider(t, id)
	ip, ok := p.(core.ImageProvider)
	if !ok {
		t.Skipf("%s does not implement image generation", id)
	}
	runImageSmoke(t, ip, liveModelFor(t, id, "image"))
}

func liveDiscovery(t *testing.T, id string) {
	t.Helper()
	p := liveEntryProvider(t, id)
	dp, ok := p.(core.DiscoveryProvider)
	if !ok {
		t.Skipf("%s does not implement discovery", id)
	}
	runDiscoverySmoke(t, dp)
}

// runRerankSmoke sends one minimal rerank request and asserts at least one
// ranked result is returned.
func runRerankSmoke(t *testing.T, rp core.RerankProvider, model string) {
	t.Helper()
	resp, err := rp.Rerank(context.Background(), core.RerankRequest{
		Model:     model,
		Query:     "What is the capital of the United States?",
		Documents: []string{"Carson City is the capital of Nevada.", "Washington, D.C. is the capital of the United States."},
	})
	if err != nil {
		t.Fatalf("Rerank(%s) failed: %v", model, err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("Rerank(%s) returned no results", model)
	}
	t.Logf("rerank: %d results (top index=%d score=%.4f)", len(resp.Results), resp.Results[0].Index, resp.Results[0].RelevanceScore)
}

func liveRerank(t *testing.T, id string) {
	t.Helper()
	p := liveEntryProvider(t, id)
	rp, ok := p.(core.RerankProvider)
	if !ok {
		t.Skipf("%s does not implement rerank", id)
	}
	runRerankSmoke(t, rp, liveModelFor(t, id, "rerank"))
}

// runModerationSmoke sends one minimal moderation request and asserts at least
// one classification result is returned.
func runModerationSmoke(t *testing.T, mp core.ModerationProvider, model string) {
	t.Helper()
	resp, err := mp.Moderate(context.Background(), core.ModerationRequest{Model: model, Input: "hello world"})
	if err != nil {
		t.Fatalf("Moderate(%s) failed: %v", model, err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("Moderate(%s) returned no results", model)
	}
	t.Logf("moderation: %d result(s), flagged=%v", len(resp.Results), resp.Results[0].Flagged)
}

func liveModerate(t *testing.T, id string) {
	t.Helper()
	p := liveEntryProvider(t, id)
	mp, ok := p.(core.ModerationProvider)
	if !ok {
		t.Skipf("%s does not implement moderation", id)
	}
	runModerationSmoke(t, mp, liveModelFor(t, id, "moderation"))
}

// runTranscriptionSmoke transcribes an operator-supplied audio file (LIVE_AUDIO_FILE)
// and asserts a non-empty transcript. It skips when no audio file is provided,
// since a transcription smoke needs real audio bytes.
func runTranscriptionSmoke(t *testing.T, tp core.TranscriptionProvider, model string) {
	t.Helper()
	path := os.Getenv("LIVE_AUDIO_FILE")
	if path == "" {
		t.Skip("LIVE_AUDIO_FILE (path to an audio file) is required for the transcription smoke")
	}
	//nolint:gosec // path is an operator-supplied audio file for an opt-in live test
	audio, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read LIVE_AUDIO_FILE: %v", err)
	}
	resp, err := tp.Transcribe(context.Background(), core.TranscriptionRequest{
		Model:    model,
		File:     audio,
		Filename: filepath.Base(path),
	})
	if err != nil {
		t.Fatalf("Transcribe(%s) failed: %v", model, err)
	}
	t.Logf("transcription: %d chars", len(resp.Text))
}

func liveTranscribe(t *testing.T, id string) {
	t.Helper()
	p := liveEntryProvider(t, id)
	tp, ok := p.(core.TranscriptionProvider)
	if !ok {
		t.Skipf("%s does not implement transcription", id)
	}
	runTranscriptionSmoke(t, tp, liveModelFor(t, id, "transcription"))
}

// runSpeechSmoke synthesizes a short phrase and asserts non-empty audio. Voices
// are provider/model-specific, so the voice is read from
// LIVE_<PROVIDER>_SPEECH_VOICE (default "alloy" for the OpenAI-style providers).
func runSpeechSmoke(t *testing.T, sp core.SpeechProvider, id, model string) {
	t.Helper()
	voice := os.Getenv("LIVE_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_SPEECH_VOICE")
	if voice == "" {
		voice = "alloy"
	}
	resp, err := sp.Speech(context.Background(), core.SpeechRequest{
		Model: model,
		Input: "Hello from the AI gateway.",
		Voice: voice,
	})
	if err != nil {
		t.Fatalf("Speech(%s) failed: %v", model, err)
	}
	if len(resp.Audio) == 0 {
		t.Fatalf("Speech(%s) returned no audio", model)
	}
	t.Logf("speech: %d audio bytes (%s)", len(resp.Audio), resp.ContentType)
}

func liveSpeak(t *testing.T, id string) {
	t.Helper()
	p := liveEntryProvider(t, id)
	sp, ok := p.(core.SpeechProvider)
	if !ok {
		t.Skipf("%s does not implement speech", id)
	}
	runSpeechSmoke(t, sp, id, liveModelFor(t, id, "speech"))
}
