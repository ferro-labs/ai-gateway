// Fixture data for the cross-provider conformance suite: each provider's native
// success payload for Complete and CompleteStream, plus the allowlist of
// providers whose transport genuinely cannot be pointed at an httptest stub.
//
// The harness and the assertions live in conformance_test.go; this file is data.
package conformance

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

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

	// baseURLKey overrides which ProviderConfig key receives the stub's URL.
	// Empty means providers.CfgKeyBaseURL — every provider but Ollama, whose
	// ProviderEntry reads CfgKeyHost instead.
	baseURLKey string
}

// openAICompatChatBody returns the OpenAI-compatible chat.completion payload
// shared by every provider whose Complete forwards to
// providers/internal/openaicompat.PostChat. id and model are threaded through
// so each fixture below still asserts a provider-specific response — the
// point of giving these providers their own fixture is proving THEIR
// ProviderEntry construction and stub wiring, not re-proving a decode groq's
// fixture already covers.
func openAICompatChatBody(id, model string) string {
	return `{
		"id": "` + id + `",
		"model": "` + model + `",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "` + wantContent + `"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
	}`
}

// fixtures maps a provider ID to its native upstream success payload.
//
// Anthropic, Gemini, Cohere, Replicate, AI21, Mistral, DeepSeek and OpenAI each
// own a bespoke translation from a non-OpenAI (or subtly-OpenAI) wire format,
// which is exactly where "advertise vs deliver" drift hides. The remaining
// entries share the providers/internal/openaicompat chat decode with groq, but
// each is still built through its OWN ProviderEntry against its OWN stub:
// env mappings, base-URL wiring and required extra config differ per
// provider even though the response decode does not. Only Bedrock and Vertex
// AI (see uncoveredProviders) genuinely cannot be pointed at an httptest stub.
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

		// The following share the openaicompat chat decode with groq above, but
		// each is still built through its own ProviderEntry against its own
		// stub, proving its own env mappings / base-URL wiring / ChatParams
		// construction rather than re-proving a decode groq already covers.
		providers.NameAzureFoundry: {
			model: "gpt-4o",
			body:  openAICompatChatBody(providers.NameAzureFoundry+"-conformance", "gpt-4o"),
		},
		// Azure OpenAI's SupportedModels() is just the configured deployment
		// name, so the fixture model and the deployment extraCfg must match.
		providers.NameAzureOpenAI: {
			model: "gpt-4o-mini",
			body:  openAICompatChatBody(providers.NameAzureOpenAI+"-conformance", "gpt-4o-mini"),
			extraCfg: providers.ProviderConfig{
				providers.CfgKeyDeployment: "gpt-4o-mini",
			},
		},
		providers.NameCerebras: {
			model: "llama-3.3-70b",
			body:  openAICompatChatBody(providers.NameCerebras+"-conformance", "llama-3.3-70b"),
		},
		providers.NameCloudflare: {
			model: "@cf/meta/llama-3.1-8b-instruct",
			body:  openAICompatChatBody(providers.NameCloudflare+"-conformance", "@cf/meta/llama-3.1-8b-instruct"),
		},
		providers.NameDatabricks: {
			model: "databricks-claude-sonnet-4-5",
			body:  openAICompatChatBody(providers.NameDatabricks+"-conformance", "databricks-claude-sonnet-4-5"),
		},
		providers.NameDeepInfra: {
			model: "deepseek-ai/DeepSeek-R1",
			body:  openAICompatChatBody(providers.NameDeepInfra+"-conformance", "deepseek-ai/DeepSeek-R1"),
		},
		providers.NameFireworks: {
			model: "accounts/fireworks/models/llama-v3p1-8b-instruct",
			body:  openAICompatChatBody(providers.NameFireworks+"-conformance", "accounts/fireworks/models/llama-v3p1-8b-instruct"),
		},
		providers.NameHuggingFace: {
			model: "meta-llama/Meta-Llama-3.1-8B-Instruct",
			body:  openAICompatChatBody(providers.NameHuggingFace+"-conformance", "meta-llama/Meta-Llama-3.1-8B-Instruct"),
		},
		providers.NameMoonshot: {
			model: "kimi-k2.5",
			body:  openAICompatChatBody(providers.NameMoonshot+"-conformance", "kimi-k2.5"),
		},
		providers.NameNovita: {
			model: "deepseek/deepseek-v3.2",
			body:  openAICompatChatBody(providers.NameNovita+"-conformance", "deepseek/deepseek-v3.2"),
		},
		providers.NameNVIDIANIM: {
			model: "meta/llama-3.1-8b-instruct",
			body:  openAICompatChatBody(providers.NameNVIDIANIM+"-conformance", "meta/llama-3.1-8b-instruct"),
		},
		// Ollama has no api_key; CfgKeyHost is the primary key its ProviderEntry
		// reads, so the stub URL must land there instead of CfgKeyBaseURL.
		providers.NameOllama: {
			model:      "llama3.2",
			body:       openAICompatChatBody(providers.NameOllama+"-conformance", "llama3.2"),
			baseURLKey: providers.CfgKeyHost,
		},
		providers.NameOllamaCloud: {
			model: "gpt-oss:120b",
			body:  openAICompatChatBody(providers.NameOllamaCloud+"-conformance", "gpt-oss:120b"),
		},
		providers.NameOpenRouter: {
			model: "openrouter/auto",
			body:  openAICompatChatBody(providers.NameOpenRouter+"-conformance", "openrouter/auto"),
		},
		providers.NamePerplexity: {
			model: "sonar",
			body:  openAICompatChatBody(providers.NamePerplexity+"-conformance", "sonar"),
		},
		providers.NameQwen: {
			model: "qwen-turbo",
			body:  openAICompatChatBody(providers.NameQwen+"-conformance", "qwen-turbo"),
		},
		providers.NameSambaNova: {
			model: "Meta-Llama-3.1-8B-Instruct",
			body:  openAICompatChatBody(providers.NameSambaNova+"-conformance", "Meta-Llama-3.1-8B-Instruct"),
		},
		providers.NameTogether: {
			model: "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
			body:  openAICompatChatBody(providers.NameTogether+"-conformance", "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo"),
		},
		providers.NameXAI: {
			model: "grok-2-latest",
			body:  openAICompatChatBody(providers.NameXAI+"-conformance", "grok-2-latest"),
		},
	}
}

// uncoveredProviders lists every provider deliberately absent from the fixture
// table, with the reason. TestConformanceCoverage fails on any provider that is
// in neither map, so conformance coverage stays honest as providers are added.
// Reserved for transports that genuinely cannot be pointed at an httptest
// stub — sharing a decode with another provider is not such a reason (see
// fixtures() for why the rest of the OpenAI-compatible fleet gets its own
// entry instead of being allowlisted here).
func uncoveredProviders() map[string]string {
	return map[string]string{
		providers.NameBedrock: "AWS SDK transport (SigV4-signed, endpoint resolved by the SDK); " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
		providers.NameVertexAI: "requires GCP OAuth (service account / ADC) and a project+region; " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
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

	// baseURLKey mirrors fixture.baseURLKey — empty means providers.CfgKeyBaseURL.
	baseURLKey string

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

// openAICompatStreamBody returns the OpenAI-compatible chat.completion.chunk
// SSE stream shared by every provider whose CompleteStream forwards to
// providers/internal/openaicompat.PostStream, in the same shape as the groq
// stream fixture: two content deltas, then one chunk carrying both
// finish_reason and usage together, then [DONE].
func openAICompatStreamBody(id, model string) string {
	return `data: {"id":"` + id + `","model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant","content":"` + wantContentPart1 + `"},"finish_reason":null}]}

data: {"id":"` + id + `","model":"` + model + `","choices":[{"index":0,"delta":{"content":"` + wantContentPart2 + `"},"finish_reason":null}]}

data: {"id":"` + id + `","model":"` + model + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}

data: [DONE]

`
}

// streamFixtures maps a provider ID to its native streaming conformance case.
// It covers every provider whose CompleteStream owns a genuinely distinct
// translation: OpenAI (chat.completion.chunk), Anthropic (Messages stream
// events), Gemini (streamGenerateContent SSE), Cohere (v2 stream events),
// Replicate (prediction output/done events), and DeepSeek (whose stream usage
// exercises core.Usage's alternate-shape folding). Every remaining provider
// shares the providers/internal/openaicompat.PostStream decode with groq, but
// (per the same reasoning as fixtures() above) is still built through its OWN
// ProviderEntry against its OWN stub, proving construction rather than
// re-proving a shared decode. Only Bedrock and Vertex AI are exempt — see
// uncoveredStreamProviders.
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

		// AI21 (Jamba) and Mistral each have their own Complete fixture because
		// their request construction differs; CompleteStream shares the
		// openaicompat decode with groq, but each is still built through its own
		// ProviderEntry against its own stub, same reasoning as the block below.
		providers.NameAI21: {
			model:   "jamba-mini-1.7",
			sseBody: openAICompatStreamBody(providers.NameAI21+"-conformance", "jamba-mini-1.7"),
		},
		providers.NameMistral: {
			model:   "mistral-small-latest",
			sseBody: openAICompatStreamBody(providers.NameMistral+"-conformance", "mistral-small-latest"),
		},

		// The following share the openaicompat.PostStream decode with groq
		// above, but each is still built through its own ProviderEntry against
		// its own stub, proving its own env mappings / base-URL wiring /
		// ChatParams construction rather than re-proving a decode groq already
		// covers.
		providers.NameAzureFoundry: {
			model:   "gpt-4o",
			sseBody: openAICompatStreamBody(providers.NameAzureFoundry+"-conformance", "gpt-4o"),
		},
		// Azure OpenAI's SupportedModels() is just the configured deployment
		// name, so the fixture model and the deployment extraCfg must match.
		providers.NameAzureOpenAI: {
			model:   "gpt-4o-mini",
			sseBody: openAICompatStreamBody(providers.NameAzureOpenAI+"-conformance", "gpt-4o-mini"),
			extraCfg: providers.ProviderConfig{
				providers.CfgKeyDeployment: "gpt-4o-mini",
			},
		},
		providers.NameCerebras: {
			model:   "llama-3.3-70b",
			sseBody: openAICompatStreamBody(providers.NameCerebras+"-conformance", "llama-3.3-70b"),
		},
		providers.NameCloudflare: {
			model:   "@cf/meta/llama-3.1-8b-instruct",
			sseBody: openAICompatStreamBody(providers.NameCloudflare+"-conformance", "@cf/meta/llama-3.1-8b-instruct"),
		},
		providers.NameDatabricks: {
			model:   "databricks-claude-sonnet-4-5",
			sseBody: openAICompatStreamBody(providers.NameDatabricks+"-conformance", "databricks-claude-sonnet-4-5"),
		},
		providers.NameDeepInfra: {
			model:   "deepseek-ai/DeepSeek-R1",
			sseBody: openAICompatStreamBody(providers.NameDeepInfra+"-conformance", "deepseek-ai/DeepSeek-R1"),
		},
		providers.NameFireworks: {
			model:   "accounts/fireworks/models/llama-v3p1-8b-instruct",
			sseBody: openAICompatStreamBody(providers.NameFireworks+"-conformance", "accounts/fireworks/models/llama-v3p1-8b-instruct"),
		},
		providers.NameHuggingFace: {
			model:   "meta-llama/Meta-Llama-3.1-8B-Instruct",
			sseBody: openAICompatStreamBody(providers.NameHuggingFace+"-conformance", "meta-llama/Meta-Llama-3.1-8B-Instruct"),
		},
		providers.NameMoonshot: {
			model:   "kimi-k2.5",
			sseBody: openAICompatStreamBody(providers.NameMoonshot+"-conformance", "kimi-k2.5"),
		},
		providers.NameNovita: {
			model:   "deepseek/deepseek-v3.2",
			sseBody: openAICompatStreamBody(providers.NameNovita+"-conformance", "deepseek/deepseek-v3.2"),
		},
		providers.NameNVIDIANIM: {
			model:   "meta/llama-3.1-8b-instruct",
			sseBody: openAICompatStreamBody(providers.NameNVIDIANIM+"-conformance", "meta/llama-3.1-8b-instruct"),
		},
		// Ollama has no api_key; CfgKeyHost is the primary key its ProviderEntry
		// reads, so the stub URL must land there instead of CfgKeyBaseURL.
		providers.NameOllama: {
			model:      "llama3.2",
			sseBody:    openAICompatStreamBody(providers.NameOllama+"-conformance", "llama3.2"),
			baseURLKey: providers.CfgKeyHost,
		},
		providers.NameOllamaCloud: {
			model:   "gpt-oss:120b",
			sseBody: openAICompatStreamBody(providers.NameOllamaCloud+"-conformance", "gpt-oss:120b"),
		},
		providers.NameOpenRouter: {
			model:   "openrouter/auto",
			sseBody: openAICompatStreamBody(providers.NameOpenRouter+"-conformance", "openrouter/auto"),
		},
		providers.NamePerplexity: {
			model:   "sonar",
			sseBody: openAICompatStreamBody(providers.NamePerplexity+"-conformance", "sonar"),
		},
		providers.NameQwen: {
			model:   "qwen-turbo",
			sseBody: openAICompatStreamBody(providers.NameQwen+"-conformance", "qwen-turbo"),
		},
		providers.NameSambaNova: {
			model:   "Meta-Llama-3.1-8B-Instruct",
			sseBody: openAICompatStreamBody(providers.NameSambaNova+"-conformance", "Meta-Llama-3.1-8B-Instruct"),
		},
		providers.NameTogether: {
			model:   "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
			sseBody: openAICompatStreamBody(providers.NameTogether+"-conformance", "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo"),
		},
		providers.NameXAI: {
			model:   "grok-2-latest",
			sseBody: openAICompatStreamBody(providers.NameXAI+"-conformance", "grok-2-latest"),
		},
	}
}

// uncoveredStreamProviders lists every provider deliberately absent from
// streamFixtures, with a reason specific to streaming (not a copy of the
// Complete-path reason, even where the underlying cause is the same
// transport). TestConformanceCoverage fails on any provider in neither map.
// Reserved for transports that genuinely cannot be pointed at an httptest
// stub — sharing a decode with groq is not such a reason (see streamFixtures
// for why the rest of the OpenAI-compatible fleet gets its own entry instead
// of being allowlisted here).
func uncoveredStreamProviders() map[string]string {
	return map[string]string{
		providers.NameBedrock: "AWS SDK transport (SigV4-signed, endpoint resolved by the SDK); " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
		providers.NameVertexAI: "requires GCP OAuth (service account / ADC) and a project+region; " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
	}
}

// newNativeStub starts a stub upstream that answers every path with the
// provider's native success payload. Path routing is the adapter's business and
// is already asserted by each provider's own tests; the conformance suite is
// only interested in the translation of the response.
