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
type fixture struct {
	// model is the request model. It must be advertised by SupportedModels().
	model string

	// body is the provider's NATIVE upstream success payload — the exact wire
	// shape its own adapter and unit tests decode. It is never OpenAI-shaped
	// unless the provider's upstream genuinely is.
	body string

	// extraCfg carries ProviderConfig keys beyond api_key/base_url that the
	// entry needs (a Required EnvMapping, or a key the model resolution reads).
	extraCfg providers.ProviderConfig
}

// fixtures maps a provider ID to its native upstream success payload.
//
// Roughly twenty providers share the providers/internal/openaicompat chat
// translation path; groq covers that shared path on their behalf. The remaining
// entries each own a bespoke translation from a non-OpenAI wire format, which is
// exactly where "advertise vs deliver" drift hides.
func fixtures() map[string]fixture {
	return map[string]fixture{
		// OpenAI — native OpenAI chat completion (the reference shape).
		providers.NameOpenAI: {
			model: "gpt-4o-mini",
			body: `{
				"id": "chatcmpl-conformance",
				"object": "chat.completion",
				"created": 1700000000,
				"model": "gpt-4o-mini",
				"choices": [{"index": 0, "message": {"role": "assistant", "content": "` + wantContent + `"}, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
			}`,
		},

		// Anthropic — native Messages API: content blocks + stop_reason "end_turn".
		providers.NameAnthropic: {
			model: "claude-sonnet-4-6",
			body: `{
				"id": "msg_conformance",
				"type": "message",
				"role": "assistant",
				"model": "claude-sonnet-4-6",
				"content": [{"type": "text", "text": "` + wantContent + `"}],
				"stop_reason": "end_turn",
				"usage": {"input_tokens": 11, "output_tokens": 7}
			}`,
		},

		// Gemini — native generateContent: candidates/parts + finishReason "STOP".
		providers.NameGemini: {
			model: "gemini-2.5-flash",
			body: `{
				"responseId": "gemini-conformance",
				"candidates": [{
					"content": {"role": "model", "parts": [{"text": "` + wantContent + `"}]},
					"finishReason": "STOP"
				}],
				"usageMetadata": {"promptTokenCount": 11, "candidatesTokenCount": 7, "totalTokenCount": 18}
			}`,
		},

		// Cohere — native v2 chat: message.content blocks + finish_reason "COMPLETE".
		providers.NameCohere: {
			model: "command-r",
			body: `{
				"id": "cohere-conformance",
				"finish_reason": "COMPLETE",
				"message": {"role": "assistant", "content": [{"type": "text", "text": "` + wantContent + `"}]},
				"usage": {
					"billed_units": {"input_tokens": 11, "output_tokens": 7},
					"tokens": {"input_tokens": 11, "output_tokens": 7}
				}
			}`,
		},

		// Replicate — native prediction object. "Prefer: wait" makes the submit
		// response already terminal, so the adapter never enters its poll loop
		// and the subtest needs no sleeps.
		providers.NameReplicate: {
			model: "meta/meta-llama-3-8b-instruct",
			body: `{
				"id": "pred-conformance",
				"status": "succeeded",
				"output": "` + wantContent + `",
				"metrics": {"input_token_count": 11, "output_token_count": 7}
			}`,
			extraCfg: providers.ProviderConfig{
				providers.CfgKeyAPIToken:   testAPIKey, // Replicate's primary key is api_token, not api_key.
				providers.CfgKeyTextModels: "meta/meta-llama-3-8b-instruct",
			},
		},

		// AI21 — Jamba models speak the OpenAI-compatible chat endpoint.
		providers.NameAI21: {
			model: "jamba-mini-1.7",
			body: `{
				"id": "ai21-conformance",
				"model": "jamba-mini-1.7",
				"choices": [{"index": 0, "message": {"role": "assistant", "content": "` + wantContent + `"}, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
			}`,
		},

		// Mistral — OpenAI-compatible, but returns the native finish reason
		// "model_length" for a truncated completion, which must normalize to "length".
		providers.NameMistral: {
			model: "mistral-small-latest",
			body: `{
				"id": "mistral-conformance",
				"model": "mistral-small-latest",
				"choices": [{"index": 0, "message": {"role": "assistant", "content": "` + wantContent + `"}, "finish_reason": "model_length"}],
				"usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
			}`,
		},

		// DeepSeek — OpenAI-compatible with extended cache/reasoning usage, decoded
		// by its own adapter rather than the shared helper.
		providers.NameDeepSeek: {
			model: "deepseek-chat",
			body: `{
				"id": "deepseek-conformance",
				"model": "deepseek-chat",
				"choices": [{"index": 0, "message": {"role": "assistant", "content": "` + wantContent + `"}, "finish_reason": "stop"}],
				"usage": {
					"prompt_tokens": 11,
					"completion_tokens": 7,
					"total_tokens": 18,
					"prompt_cache_hit_tokens": 4,
					"completion_tokens_details": {"reasoning_tokens": 3}
				}
			}`,
		},

		// Groq — stands in for every provider on the shared
		// providers/internal/openaicompat chat path (see uncoveredProviders).
		providers.NameGroq: {
			model: "llama-3.1-8b-instant",
			body: `{
				"id": "groq-conformance",
				"model": "llama-3.1-8b-instant",
				"choices": [{"index": 0, "message": {"role": "assistant", "content": "` + wantContent + `"}, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
			}`,
		},
	}
}

// uncoveredProviders lists every provider deliberately absent from the fixture
// table, with the reason. TestConformanceCoverage fails on any provider that is
// in neither map, so conformance coverage stays honest as providers are added.
func uncoveredProviders() map[string]string {
	const sharedOpenAICompat = "shares the providers/internal/openaicompat chat translation path, covered by the groq fixture"
	return map[string]string{
		providers.NameBedrock: "AWS SDK transport (SigV4-signed, endpoint resolved by the SDK); " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
		providers.NameVertexAI: "requires GCP OAuth (service account / ADC) and a project+region; " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",

		providers.NameAzureFoundry: sharedOpenAICompat,
		providers.NameAzureOpenAI:  sharedOpenAICompat,
		providers.NameCerebras:     sharedOpenAICompat,
		providers.NameCloudflare:   sharedOpenAICompat,
		providers.NameDatabricks:   sharedOpenAICompat,
		providers.NameDeepInfra:    sharedOpenAICompat,
		providers.NameFireworks:    sharedOpenAICompat,
		providers.NameHuggingFace:  sharedOpenAICompat,
		providers.NameMoonshot:     sharedOpenAICompat,
		providers.NameNovita:       sharedOpenAICompat,
		providers.NameNVIDIANIM:    sharedOpenAICompat,
		providers.NameOllama:       sharedOpenAICompat,
		providers.NameOllamaCloud:  sharedOpenAICompat,
		providers.NameOpenRouter:   sharedOpenAICompat,
		providers.NamePerplexity:   sharedOpenAICompat,
		providers.NameQwen:         sharedOpenAICompat,
		providers.NameSambaNova:    sharedOpenAICompat,
		providers.NameTogether:     sharedOpenAICompat,
		providers.NameXAI:          sharedOpenAICompat,
	}
}

// streamFixture is one provider's streaming conformance case: the native SSE
// event stream its adapter's CompleteStream must translate into the canonical
// <-chan core.StreamChunk sequence.
type streamFixture struct {
	// model is the request model; it must be advertised by SupportedModels().
	model string

	// sseBody is the provider's NATIVE streaming wire format for the single
	// HTTP request its CompleteStream issues against baseURL. Every fixture
	// but Replicate's fits this shape (Replicate's async prediction API needs a
	// second endpoint for the stream itself, wired by newStub instead).
	sseBody string

	// extraCfg mirrors fixture.extraCfg — ProviderConfig keys beyond
	// api_key/base_url that the entry needs.
	extraCfg providers.ProviderConfig

	// newStub builds the upstream stub when sseBody/newNativeStub cannot
	// represent the provider's transport. Only Replicate sets this.
	newStub func(t *testing.T) *httptest.Server

	// noUsage marks a provider whose streaming path never reports usage at
	// all, so the generic usage assertion is skipped rather than failing on a
	// gap the adapter's own tests already document.
	noUsage bool

	// wantCacheReadTokens/wantReasoningTokens assert the extended usage fields
	// core.Usage.UnmarshalJSON folds from alternate wire shapes (DeepSeek's
	// flat prompt_cache_hit_tokens and nested completion_tokens_details.
	// reasoning_tokens) survive the generic openaicompat stream decode, not
	// just the bespoke non-streaming one.
	wantCacheReadTokens int
	wantReasoningTokens int
}

// streamFixtures maps a provider ID to its native streaming conformance case.
// It covers every provider whose CompleteStream owns a genuinely distinct
// translation: OpenAI (chat.completion.chunk), Anthropic (Messages stream
// events), Gemini (streamGenerateContent SSE), Cohere (v2 stream events),
// Replicate (prediction output/done events), and DeepSeek (whose stream usage
// exercises core.Usage's alternate-shape folding). Groq stands in for the
// providers/internal/openaicompat.PostStream path shared by AI21, Mistral and
// the twenty-odd providers already lumped under sharedOpenAICompat for
// Complete — see uncoveredStreamProviders.
func streamFixtures() map[string]streamFixture {
	return map[string]streamFixture{
		// OpenAI — native chat.completion.chunk frames, content split across two
		// deltas, terminal finish_reason chunk followed by a usage-only chunk.
		providers.NameOpenAI: {
			model: "gpt-4o-mini",
			sseBody: `data: {"id":"chatcmpl-conformance","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"` + wantContentPart1 + `"},"finish_reason":null}]}

data: {"id":"chatcmpl-conformance","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"` + wantContentPart2 + `"},"finish_reason":null}]}

data: {"id":"chatcmpl-conformance","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-conformance","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}

data: [DONE]

`,
		},

		// Anthropic — message_start carries input_tokens, two content_block_delta
		// text frames, message_delta carries stop_reason + output_tokens.
		providers.NameAnthropic: {
			model: "claude-sonnet-4-6",
			sseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_conformance","model":"claude-sonnet-4-6","role":"assistant","usage":{"input_tokens":11}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + wantContentPart1 + `"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + wantContentPart2 + `"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}

`,
		},

		// Gemini — streamGenerateContent SSE: two candidate chunks, the final one
		// carrying finishReason "STOP" and usageMetadata together, exactly as the
		// real API emits them.
		providers.NameGemini: {
			model: "gemini-2.5-flash",
			sseBody: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"` + wantContentPart1 + `"}]},"finishReason":""}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"` + wantContentPart2 + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}

`,
		},

		// Cohere — v2 stream events: two content-delta events, then message-end
		// carrying finish_reason "COMPLETE" and the tokens usage object.
		providers.NameCohere: {
			model: "command-r",
			sseBody: `data: {"type":"content-delta","delta":{"message":{"content":{"text":"` + wantContentPart1 + `"}}}}

data: {"type":"content-delta","delta":{"message":{"content":{"text":"` + wantContentPart2 + `"}}}}

data: {"type":"message-end","delta":{"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":11,"output_tokens":7}}}}

`,
		},

		// Replicate — a two-phase transport: CompleteStream submits a prediction
		// (JSON) and then GETs the URL it returns for the actual event/data SSE
		// stream. newStub wires both legs against the same httptest.Server.
		// Replicate's readStream never assembles a usage object (its Metrics are
		// only read on the polled Complete path), so noUsage documents that gap
		// rather than asserting around it.
		providers.NameReplicate: {
			model:   "meta/meta-llama-3-8b-instruct",
			noUsage: true,
			extraCfg: providers.ProviderConfig{
				providers.CfgKeyAPIToken:   testAPIKey,
				providers.CfgKeyTextModels: "meta/meta-llama-3-8b-instruct",
			},
			newStub: func(t *testing.T) *httptest.Server {
				t.Helper()
				var srv *httptest.Server
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"pred-conformance","status":"starting","urls":{"stream":"` + srv.URL + `/stream"}}`))
						return
					}
					// wantContentPart2 already carries its own leading space (see the
					// constant doc); "data: " + wantContentPart2 lays down two spaces
					// after the colon, matching Replicate's own parser
					// (strings.CutPrefix(line, "data:") then TrimPrefix(value, " ")),
					// which strips exactly one and leaves the intended " ok" delta.
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(
						"event: output\ndata: " + wantContentPart1 + "\n\n" +
							"event: output\ndata: " + wantContentPart2 + "\n\n" +
							"event: done\ndata: {}\n\n"))
				}))
				return srv
			},
		},

		// DeepSeek — routes through the generic providers/internal/openaicompat
		// stream decode (unlike its bespoke Complete decode), so this fixture
		// proves core.Usage's alternate-shape folding (prompt_cache_hit_tokens,
		// completion_tokens_details.reasoning_tokens) also survives streaming.
		providers.NameDeepSeek: {
			model:               "deepseek-chat",
			wantCacheReadTokens: 4,
			wantReasoningTokens: 3,
			sseBody: `data: {"id":"deepseek-conformance","model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"` + wantContentPart1 + `"},"finish_reason":null}]}

data: {"id":"deepseek-conformance","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"` + wantContentPart2 + `"},"finish_reason":null}]}

data: {"id":"deepseek-conformance","model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_cache_hit_tokens":4,"completion_tokens_details":{"reasoning_tokens":3}}}

data: [DONE]

`,
		},

		// Groq — stands in for every provider on the shared
		// providers/internal/openaicompat.PostStream path (see
		// uncoveredStreamProviders), which decodes core.StreamChunk directly with
		// no provider-specific override.
		providers.NameGroq: {
			model: "llama-3.1-8b-instant",
			sseBody: `data: {"id":"groq-conformance","model":"llama-3.1-8b-instant","choices":[{"index":0,"delta":{"role":"assistant","content":"` + wantContentPart1 + `"},"finish_reason":null}]}

data: {"id":"groq-conformance","model":"llama-3.1-8b-instant","choices":[{"index":0,"delta":{"content":"` + wantContentPart2 + `"},"finish_reason":null}]}

data: {"id":"groq-conformance","model":"llama-3.1-8b-instant","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}

data: [DONE]

`,
		},
	}
}

// uncoveredStreamProviders lists every provider deliberately absent from
// streamFixtures, with a reason specific to streaming (not a copy of the
// Complete-path reason, even where the underlying cause is the same
// transport). TestConformanceCoverage fails on any provider in neither map.
func uncoveredStreamProviders() map[string]string {
	const sharedOpenAICompatStream = "CompleteStream forwards to the identical providers/internal/openaicompat.PostStream " +
		"+ StreamSSE decode as groq; ChatParams differ per provider but only affect the request, never the response " +
		"translation asserted here, so the groq stream fixture already covers this path"
	return map[string]string{
		providers.NameBedrock: "AWS SDK transport (SigV4-signed, endpoint resolved by the SDK); " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
		providers.NameVertexAI: "requires GCP OAuth (service account / ADC) and a project+region; " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",

		// AI21 (Jamba) and Mistral each have their own Complete fixture because
		// their request construction differs, but CompleteStream for both routes
		// through the exact same shared decode Groq's stream fixture exercises.
		providers.NameAI21:    sharedOpenAICompatStream,
		providers.NameMistral: sharedOpenAICompatStream,

		providers.NameAzureFoundry: sharedOpenAICompatStream,
		providers.NameAzureOpenAI:  sharedOpenAICompatStream,
		providers.NameCerebras:     sharedOpenAICompatStream,
		providers.NameCloudflare:   sharedOpenAICompatStream,
		providers.NameDatabricks:   sharedOpenAICompatStream,
		providers.NameDeepInfra:    sharedOpenAICompatStream,
		providers.NameFireworks:    sharedOpenAICompatStream,
		providers.NameHuggingFace:  sharedOpenAICompatStream,
		providers.NameMoonshot:     sharedOpenAICompatStream,
		providers.NameNovita:       sharedOpenAICompatStream,
		providers.NameNVIDIANIM:    sharedOpenAICompatStream,
		providers.NameOllama:       sharedOpenAICompatStream,
		providers.NameOllamaCloud:  sharedOpenAICompatStream,
		providers.NameOpenRouter:   sharedOpenAICompatStream,
		providers.NamePerplexity:   sharedOpenAICompatStream,
		providers.NameQwen:         sharedOpenAICompatStream,
		providers.NameSambaNova:    sharedOpenAICompatStream,
		providers.NameTogether:     sharedOpenAICompatStream,
		providers.NameXAI:          sharedOpenAICompatStream,
	}
}

// newNativeStub starts a stub upstream that answers every path with the
// provider's native success payload. Path routing is the adapter's business and
// is already asserted by each provider's own tests; the conformance suite is
// only interested in the translation of the response.
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
// gateway itself uses — pointed at the stub server.
func buildProvider(t *testing.T, id string, extraCfg providers.ProviderConfig, baseURL string) core.Provider {
	t.Helper()

	entry, ok := providers.GetProviderEntry(id)
	if !ok {
		t.Fatalf("provider %q has no ProviderEntry", id)
	}
	cfg := providers.ProviderConfig{
		providers.CfgKeyAPIKey:  testAPIKey,
		providers.CfgKeyBaseURL: baseURL,
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

			p := buildProvider(t, id, fx.extraCfg, srv.URL)

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

			p := buildProvider(t, id, fx.extraCfg, srv.URL)
			sp, ok := p.(core.StreamProvider)
			if !ok {
				t.Fatalf("provider %q is in streamFixtures() but does not implement core.StreamProvider", id)
			}

			if !slices.Contains(sp.SupportedModels(), fx.model) {
				t.Fatalf("fixture model %q is not in %s SupportedModels() = %v", fx.model, id, sp.SupportedModels())
			}

			ch, err := sp.CompleteStream(context.Background(), canonicalRequest(fx.model))
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
			finishIdx, usageIdx, lastChoiceIdx := -1, -1, -1
			for i, c := range chunks {
				for _, choice := range c.Choices {
					lastChoiceIdx = i
					content.WriteString(choice.Delta.Content)
					if choice.FinishReason != "" {
						finishReason = choice.FinishReason
						finishIdx = i
					}
				}
				if c.Usage != nil {
					usage = c.Usage
					usageIdx = i
				}
			}
			if got := content.String(); got != wantContent {
				t.Errorf("concatenated content = %q, want %q", got, wantContent)
			}

			// 2. finish_reason is canonical OpenAI (not a raw provider token —
			// Anthropic's end_turn, Gemini's STOP, Cohere's COMPLETE) and terminal: it
			// lands on the last chunk that carries a choice, and no chunk after it
			// carries a content delta. An OpenAI-compatible client stops reading the
			// stream once finish_reason arrives, so content the translator emits after
			// it would never reach the client.
			if !slices.Contains(canonicalFinishReasons, finishReason) {
				t.Errorf("terminal FinishReason = %q, want one of %v (a native token leaked through translation)",
					finishReason, canonicalFinishReasons)
			}
			if finishIdx != -1 {
				if finishIdx != lastChoiceIdx {
					t.Errorf("finish_reason set on chunk %d, but chunk %d is the last one carrying a choice; finish_reason must be terminal",
						finishIdx, lastChoiceIdx)
				}
				for i := finishIdx + 1; i < len(chunks); i++ {
					for _, choice := range chunks[i].Choices {
						if choice.Delta.Content != "" {
							t.Errorf("chunk %d carries content %q after the finish_reason chunk %d; the stream ended early",
								i, choice.Delta.Content, finishIdx)
						}
					}
				}
			}

			// 3. Usage never precedes the finish: the chunk that carries it is at or
			// after the chunk carrying finish_reason, matching every real wire shape
			// (OpenAI-compatible: a trailing usage-only chunk after the finish chunk;
			// Anthropic/Gemini/Cohere: finish_reason and usage share one chunk).
			// Replicate's noUsage documents the one streaming path that genuinely never
			// assembles usage at all, rather than asserting around the gap.
			switch {
			case fx.noUsage:
				if usage != nil {
					t.Errorf("fixture documents noUsage but a chunk carried usage: %+v", usage)
				}
			case usage == nil:
				t.Errorf("no chunk carried usage; want prompt=%d completion=%d", wantPromptTokens, wantCompletionTokens)
			default:
				if usageIdx < finishIdx {
					t.Errorf("usage landed on chunk %d, before the finish_reason chunk %d", usageIdx, finishIdx)
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
