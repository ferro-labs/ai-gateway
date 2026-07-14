// Package conformance holds the cross-provider conformance suite.
//
// The suite proves that each provider adapter TRANSLATES its native upstream
// response into a correct OpenAI-shaped core.Response. Every provider is built
// through its providers.ProviderEntry — the same seam the gateway uses — and
// pointed at an httptest stub that replies with that provider's *native*
// success payload. The assertions then check the translated core.Response
// against the invariants every provider must satisfy, closing the gap between
// what a provider advertises and what it actually delivers.
//
// The suite needs no network, no Docker and no credentials, so it carries no
// build tag and runs with the default unit-test target.
package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// testAPIKey is the dummy credential handed to every provider under test.
	testAPIKey = "test-key"

	// wantContent is the assistant text every native fixture returns. Asserting
	// on it proves the content survived translation rather than being dropped.
	wantContent = "conformance ok"

	// wantContentPart1 and wantContentPart2 are the two SSE deltas every stream
	// fixture splits wantContent across. Asserting their concatenation proves
	// the suite exercises multi-frame accumulation rather than a single lucky
	// chunk that happens to carry the whole string.
	wantContentPart1 = "conformance"
	wantContentPart2 = " ok"

	// Token counts every fixture reports, so a provider that drops usage during
	// translation is caught.
	wantPromptTokens     = 11
	wantCompletionTokens = 7

	// Canonical request parameters shared by every provider subtest.
	reqMaxTokens   = 64
	reqTemperature = 0.2
	reqPrompt      = "Say hello."
)

// canonicalFinishReasons is the OpenAI finish_reason vocabulary. A provider that
// leaks a native token (Anthropic's end_turn, Gemini's STOP, Cohere's COMPLETE,
// …) fails the FinishReason invariant.
var canonicalFinishReasons = []string{
	core.FinishReasonStop,
	core.FinishReasonLength,
	core.FinishReasonToolCalls,
	core.FinishReasonContentFilter,
}

// fixture is one provider's conformance case: the native upstream success
// payload its adapter must translate, plus the inputs needed to build it.
func newNativeStub(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// canonicalRequest is the single core.Request every provider is sent.
func canonicalRequest(model string) core.Request {
	maxTokens := reqMaxTokens
	temperature := reqTemperature
	return core.Request{
		Model:       model,
		Messages:    []core.Message{{Role: core.RoleUser, Content: reqPrompt}},
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
	}
}

// buildProvider constructs the provider through its ProviderEntry — the seam the
// gateway itself uses — pointed at the stub server. baseURLKey redirects the
// stub URL to a ProviderConfig key other than CfgKeyBaseURL (Ollama's
// ProviderEntry reads CfgKeyHost instead); an empty baseURLKey means
// CfgKeyBaseURL.
func buildProvider(t *testing.T, id string, extraCfg providers.ProviderConfig, baseURLKey, baseURL string) core.Provider {
	t.Helper()

	entry, ok := providers.GetProviderEntry(id)
	if !ok {
		t.Fatalf("provider %q has no ProviderEntry", id)
	}
	if baseURLKey == "" {
		baseURLKey = providers.CfgKeyBaseURL
	}
	cfg := providers.ProviderConfig{
		providers.CfgKeyAPIKey: testAPIKey,
		baseURLKey:             baseURL,
	}
	for k, v := range extraCfg {
		cfg[k] = v
	}
	p, err := entry.Build(cfg)
	if err != nil {
		t.Fatalf("entry.Build(%q): %v", id, err)
	}
	return p
}

// TestProviderResponseConformance asserts the OpenAI-shape invariants on the
// core.Response each adapter produces from its native upstream payload.
func TestProviderResponseConformance(t *testing.T) {
	for id, fx := range fixtures() {
		t.Run(id, func(t *testing.T) {
			srv := newNativeStub(fx.body)
			defer srv.Close()

			p := buildProvider(t, id, fx.extraCfg, fx.baseURLKey, srv.URL)

			// The canonical request must use a model the provider advertises,
			// so the suite exercises a supported path rather than a lucky one.
			if !slices.Contains(p.SupportedModels(), fx.model) {
				t.Fatalf("fixture model %q is not in %s SupportedModels() = %v", fx.model, id, p.SupportedModels())
			}

			resp, err := p.Complete(context.Background(), canonicalRequest(fx.model))
			if err != nil {
				t.Fatalf("Complete(): %v", err)
			}

			// 1. Content survived translation.
			if len(resp.Choices) == 0 {
				t.Fatalf("Choices is empty; the upstream text was dropped in translation")
			}
			if got := resp.Choices[0].Message.Content; got != wantContent {
				t.Errorf("Choices[0].Message.Content = %q, want %q", got, wantContent)
			}

			// 2. Role is the canonical assistant role.
			if got := resp.Choices[0].Message.Role; got != core.RoleAssistant {
				t.Errorf("Choices[0].Message.Role = %q, want %q", got, core.RoleAssistant)
			}

			// 3. finish_reason is canonical OpenAI, not a raw provider token.
			if got := resp.Choices[0].FinishReason; !slices.Contains(canonicalFinishReasons, got) {
				t.Errorf("Choices[0].FinishReason = %q, want one of %v (a native token leaked through translation)",
					got, canonicalFinishReasons)
			}

			// 4. Usage survived translation — every fixture supplies token counts.
			if resp.Usage.PromptTokens != wantPromptTokens {
				t.Errorf("Usage.PromptTokens = %d, want %d", resp.Usage.PromptTokens, wantPromptTokens)
			}
			if resp.Usage.CompletionTokens != wantCompletionTokens {
				t.Errorf("Usage.CompletionTokens = %d, want %d", resp.Usage.CompletionTokens, wantCompletionTokens)
			}

			// 5. Model is set.
			if strings.TrimSpace(resp.Model) == "" {
				t.Errorf("Model is empty")
			}

			// 6. Provider identifies the adapter that produced the response.
			if resp.Provider != p.Name() {
				t.Errorf("Provider = %q, want %q", resp.Provider, p.Name())
			}

			// 7. ID is set.
			if strings.TrimSpace(resp.ID) == "" {
				t.Errorf("ID is empty")
			}
		})
	}
}

// streamDrainTimeout bounds how long a stream conformance subtest waits for
// the channel to close. A hang here is exactly the goroutine leak this
// invariant guards against, so it fails fast instead of running out the full
// go test timeout.
const streamDrainTimeout = 5 * time.Second

// collectStream drains ch and returns every chunk received before it closes.
// Timing out is treated as a hard failure: the producer goroutine is stuck
// (most likely blocked on a send with no receiver, or on a body read that
// never completes), which is the leak TestProviderStreamConformance exists to
// catch.
func collectStream(t *testing.T, ch <-chan core.StreamChunk) []core.StreamChunk {
	t.Helper()

	var chunks []core.StreamChunk
	deadline := time.After(streamDrainTimeout)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return chunks
			}
			if c.Error != nil {
				t.Fatalf("stream chunk carried error: %v", c.Error)
			}
			chunks = append(chunks, c)
		case <-deadline:
			t.Fatalf("stream channel did not close within %s; the producer goroutine is likely blocked", streamDrainTimeout)
			return nil
		}
	}
}

// TestProviderStreamConformance is TestProviderResponseConformance's streaming
// counterpart: it drives each fixtured provider's CompleteStream against a
// stub serving that provider's NATIVE SSE event stream and asserts the
// translated <-chan core.StreamChunk sequence, closing the gap left by a suite
// that only ever exercised Complete. SSE framing, finish_reason on the
// terminal chunk, and usage on the final chunk are exactly where streaming
// translation bugs hide that a non-streaming fixture cannot catch.
func TestProviderStreamConformance(t *testing.T) {
	for id, fx := range streamFixtures() {
		t.Run(id, func(t *testing.T) {
			var srv *httptest.Server
			if fx.newStub != nil {
				srv = fx.newStub(t)
			} else {
				srv = newNativeStub(fx.sseBody)
			}
			defer srv.Close()

			p := buildProvider(t, id, fx.extraCfg, fx.baseURLKey, srv.URL)
			sp, ok := p.(core.StreamProvider)
			if !ok {
				t.Fatalf("provider %q is in streamFixtures() but does not implement core.StreamProvider", id)
			}

			if !slices.Contains(sp.SupportedModels(), fx.model) {
				t.Fatalf("fixture model %q is not in %s SupportedModels() = %v", fx.model, id, sp.SupportedModels())
			}

			// streamCtx bounds the producer goroutine to this subtest: canceling it
			// once collection finishes (success or the collectStream timeout below)
			// unblocks a pending send inside CompleteStream, so a stuck producer
			// cannot leak past the subtest or make the deferred srv.Close() above
			// block forever waiting on an outstanding request.
			streamCtx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ch, err := sp.CompleteStream(streamCtx, canonicalRequest(fx.model))
			if err != nil {
				t.Fatalf("CompleteStream(): %v", err)
			}
			chunks := collectStream(t, ch)
			if len(chunks) == 0 {
				t.Fatalf("no chunks received; the upstream stream was dropped in translation")
			}

			// 1. Content survived translation — concatenated across every delta,
			// not just the first chunk, proving multi-frame accumulation works.
			var content strings.Builder
			var finishReason string
			var usage *core.Usage
			var finishIndices, usageIndices []int
			lastChoiceIdx := -1
			for i, c := range chunks {
				for _, choice := range c.Choices {
					lastChoiceIdx = i
					content.WriteString(choice.Delta.Content)
					if choice.FinishReason != "" {
						finishReason = choice.FinishReason
						finishIndices = append(finishIndices, i)
					}
				}
				if c.Usage != nil {
					usage = c.Usage
					usageIndices = append(usageIndices, i)
				}
			}
			if got := content.String(); got != wantContent {
				t.Errorf("concatenated content = %q, want %q", got, wantContent)
			}

			// 2. finish_reason is canonical OpenAI (not a raw provider token —
			// Anthropic's end_turn, Gemini's STOP, Cohere's COMPLETE) and terminal.
			// EVERY chunk carrying one is checked, not just the last: a premature
			// finish_reason followed by a duplicate correct one on the true terminal
			// chunk must still fail here, and so must any content delta that arrives
			// after ANY of them. An OpenAI-compatible client stops reading the stream
			// the moment the first finish_reason arrives, so content the translator
			// emits after it would never reach the client.
			if !slices.Contains(canonicalFinishReasons, finishReason) {
				t.Errorf("terminal FinishReason = %q, want one of %v (a native token leaked through translation)",
					finishReason, canonicalFinishReasons)
			}
			for _, idx := range finishIndices {
				if idx != lastChoiceIdx {
					t.Errorf("finish_reason set on chunk %d, but chunk %d is the last one carrying a choice; finish_reason must be terminal",
						idx, lastChoiceIdx)
				}
				for i := idx + 1; i < len(chunks); i++ {
					for _, choice := range chunks[i].Choices {
						if choice.Delta.Content != "" {
							t.Errorf("chunk %d carries content %q after the finish_reason chunk %d; the stream ended early",
								i, choice.Delta.Content, idx)
						}
					}
				}
			}

			// 3. Usage never precedes the finish: EVERY chunk that carries usage is
			// checked against the true terminal finish_reason chunk, not just the last
			// usage chunk — a premature usage chunk followed by a duplicate correct one
			// at the terminal must still fail here. This matches every real wire shape
			// (OpenAI-compatible: a trailing usage-only chunk after the finish chunk;
			// Anthropic/Gemini/Cohere: finish_reason and usage share one chunk).
			// Replicate's noUsage documents the one streaming path that genuinely never
			// assembles usage at all, rather than asserting around the gap.
			terminalFinishIdx := -1
			if len(finishIndices) > 0 {
				terminalFinishIdx = finishIndices[len(finishIndices)-1]
			}
			switch {
			case fx.noUsage:
				if usage != nil {
					t.Errorf("fixture documents noUsage but a chunk carried usage: %+v", usage)
				}
			case usage == nil:
				t.Errorf("no chunk carried usage; want prompt=%d completion=%d", wantPromptTokens, wantCompletionTokens)
			default:
				for _, idx := range usageIndices {
					if idx < terminalFinishIdx {
						t.Errorf("usage landed on chunk %d, before the finish_reason chunk %d", idx, terminalFinishIdx)
					}
				}
				if usage.PromptTokens != wantPromptTokens {
					t.Errorf("Usage.PromptTokens = %d, want %d", usage.PromptTokens, wantPromptTokens)
				}
				if usage.CompletionTokens != wantCompletionTokens {
					t.Errorf("Usage.CompletionTokens = %d, want %d", usage.CompletionTokens, wantCompletionTokens)
				}
				if fx.wantCacheReadTokens != 0 && usage.CacheReadTokens != fx.wantCacheReadTokens {
					t.Errorf("Usage.CacheReadTokens = %d, want %d", usage.CacheReadTokens, fx.wantCacheReadTokens)
				}
				if fx.wantReasoningTokens != 0 && usage.ReasoningTokens != fx.wantReasoningTokens {
					t.Errorf("Usage.ReasoningTokens = %d, want %d", usage.ReasoningTokens, fx.wantReasoningTokens)
				}
			}
		})
	}
}

// TestConformanceCoverage is the drift guard: every registered provider must be
// either covered by a fixture or explicitly allowlisted with a reason. It also
// rejects stale entries naming providers that no longer exist.
func TestConformanceCoverage(t *testing.T) {
	covered := fixtures()
	uncovered := uncoveredProviders()
	streamCovered := streamFixtures()
	streamUncovered := uncoveredStreamProviders()

	for _, entry := range providers.AllProviders() {
		_, hasFixture := covered[entry.ID]
		_, isAllowlisted := uncovered[entry.ID]
		switch {
		case hasFixture && isAllowlisted:
			t.Errorf("provider %q is both covered by a fixture and allowlisted as uncovered; remove one", entry.ID)
		case !hasFixture && !isAllowlisted:
			t.Errorf("provider %q has no conformance fixture and is not in uncoveredProviders(); "+
				"add a fixture with its native upstream payload, or allowlist it with a reason", entry.ID)
		}

		_, hasStreamFixture := streamCovered[entry.ID]
		_, isStreamAllowlisted := streamUncovered[entry.ID]
		switch {
		case hasStreamFixture && isStreamAllowlisted:
			t.Errorf("provider %q is both covered by a stream fixture and allowlisted as stream-uncovered; remove one", entry.ID)
		case !hasStreamFixture && !isStreamAllowlisted:
			t.Errorf("provider %q has no stream conformance fixture and is not in uncoveredStreamProviders(); "+
				"add a streamFixture with its native SSE payload, or allowlist it with a reason", entry.ID)
		}
	}

	for id := range covered {
		if _, ok := providers.GetProviderEntry(id); !ok {
			t.Errorf("fixture %q names a provider that is no longer registered", id)
		}
	}
	for id, reason := range uncovered {
		if _, ok := providers.GetProviderEntry(id); !ok {
			t.Errorf("uncoveredProviders() entry %q names a provider that is no longer registered", id)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("uncoveredProviders()[%q] has no reason", id)
		}
	}

	for id := range streamCovered {
		if _, ok := providers.GetProviderEntry(id); !ok {
			t.Errorf("stream fixture %q names a provider that is no longer registered", id)
		}
	}
	for id, reason := range streamUncovered {
		if _, ok := providers.GetProviderEntry(id); !ok {
			t.Errorf("uncoveredStreamProviders() entry %q names a provider that is no longer registered", id)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("uncoveredStreamProviders()[%q] has no reason", id)
		}
	}
}
