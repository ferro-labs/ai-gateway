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

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// testAPIKey is the dummy credential handed to every provider under test.
	testAPIKey = "test-key"

	// wantContent is the assistant text every native fixture returns. Asserting
	// on it proves the content survived translation rather than being dropped.
	wantContent = "conformance ok"

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
func buildProvider(t *testing.T, id string, fx fixture, baseURL string) core.Provider {
	t.Helper()

	entry, ok := providers.GetProviderEntry(id)
	if !ok {
		t.Fatalf("provider %q has no ProviderEntry", id)
	}
	cfg := providers.ProviderConfig{
		providers.CfgKeyAPIKey:  testAPIKey,
		providers.CfgKeyBaseURL: baseURL,
	}
	for k, v := range fx.extraCfg {
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

			p := buildProvider(t, id, fx, srv.URL)

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

// TestConformanceCoverage is the drift guard: every registered provider must be
// either covered by a fixture or explicitly allowlisted with a reason. It also
// rejects stale entries naming providers that no longer exist.
func TestConformanceCoverage(t *testing.T) {
	covered := fixtures()
	uncovered := uncoveredProviders()

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
}
