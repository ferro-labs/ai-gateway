// Fixture metadata for the cross-provider conformance suite. The native upstream
// payloads themselves live next to each provider, in
// providers/<dir>/testdata/{chat.response.json,chat.stream.sse}, and are loaded
// by loadTestdata below. This file carries only what the harness needs to BUILD
// and INTERPRET each case — the request model, extra ProviderConfig, and the
// streaming invariants a payload cannot express — plus the allowlist of
// providers whose transport genuinely cannot be pointed at an httptest stub.
//
// The harness and the assertions live in conformance_test.go.
package conformance

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// providerDirForID maps a provider id to its subpackage directory: the id with
// hyphens turned back into underscores (azure-openai -> azure_openai).
func providerDirForID(id string) string { return strings.ReplaceAll(id, "-", "_") }

// loadTestdata reads one native fixture payload from the provider's own
// testdata directory. It resolves the path from this source file rather than the
// working directory, so it is correct however `go test` is invoked.
func loadTestdata(t *testing.T, id, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate testdata")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "providers", providerDirForID(id), "testdata", name)
	//nolint:gosec // path is composed from the test-tree layout, a provider id from the fixtures table, and a fixed filename — never external input.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("load testdata for %q: %v", id, err)
	}
	return string(b)
}

// fixture is one provider's conformance case. The native success payload it must
// translate is loaded from providers/<dir>/testdata/chat.response.json; this
// struct carries only the inputs the harness needs to build the provider and
// pick a supported model.
type fixture struct {
	// model is the request model. It must be advertised by ConfiguredModels()
	// where the provider implements it, and accepted by SupportsModel.
	model string

	// extraCfg carries ProviderConfig keys beyond api_key/base_url that the
	// entry needs (a Required EnvMapping, or a key the model resolution reads).
	extraCfg providers.ProviderConfig

	// baseURLKey overrides which ProviderConfig key receives the stub's URL.
	// Empty means providers.CfgKeyBaseURL — every provider but Ollama, whose
	// ProviderEntry reads CfgKeyHost instead.
	baseURLKey string
}

// fixtures maps a provider ID to its conformance case. Every provider but
// Bedrock and Vertex AI (see uncoveredProviders) is covered: each is built
// through its OWN ProviderEntry against its OWN stub — env mappings, base-URL
// wiring and required extra config differ per provider even where the response
// decode is the shared providers/core/openaicompat one. The native payload each
// stub serves lives in that provider's testdata/chat.response.json.
func fixtures() map[string]fixture {
	return map[string]fixture{
		providers.NameOpenAI:    {model: "gpt-4o-mini"},
		providers.NameAnthropic: {model: "claude-sonnet-4-6"},
		providers.NameGemini:    {model: "gemini-2.5-flash"},
		providers.NameCohere:    {model: "command-r"},

		// Replicate's primary key is api_token, not api_key, and its model set is
		// config-driven, so the deployment id must be declared to the entry too.
		providers.NameReplicate: {
			model: "meta/meta-llama-3-8b-instruct",
			extraCfg: providers.ProviderConfig{
				providers.CfgKeyAPIToken:   testAPIKey,
				providers.CfgKeyTextModels: "meta/meta-llama-3-8b-instruct",
			},
		},

		providers.NameAI21:     {model: "jamba-mini-1.7"},
		providers.NameMistral:  {model: "mistral-small-latest"},
		providers.NameDeepSeek: {model: "deepseek-chat"},
		providers.NameGroq:     {model: "llama-3.1-8b-instant"},

		providers.NameAzureFoundry: {model: "gpt-4o"},
		// Azure OpenAI's ConfiguredModels() is just the configured deployment
		// name, so the fixture model and the deployment extraCfg must match.
		providers.NameAzureOpenAI: {
			model:    "gpt-4o-mini",
			extraCfg: providers.ProviderConfig{providers.CfgKeyDeployment: "gpt-4o-mini"},
		},
		providers.NameCerebras: {model: "llama-3.3-70b"},
		// account_id is a declared required key, so the factory refuses a config
		// without it exactly as it refuses a missing credential.
		providers.NameCloudflare: {
			model:    "@cf/meta/llama-3.1-8b-instruct",
			extraCfg: providers.ProviderConfig{providers.CfgKeyAccountID: "conformance-account"},
		},
		providers.NameDatabricks:  {model: "databricks-claude-sonnet-4-5"},
		providers.NameDeepInfra:   {model: "deepseek-ai/DeepSeek-R1"},
		providers.NameFireworks:   {model: "accounts/fireworks/models/llama-v3p1-8b-instruct"},
		providers.NameHuggingFace: {model: "meta-llama/Meta-Llama-3.1-8B-Instruct"},
		providers.NameMoonshot:    {model: "kimi-k2.5"},
		providers.NameNovita:      {model: "deepseek/deepseek-v3.2"},
		providers.NameNVIDIANIM:   {model: "meta/llama-3.1-8b-instruct"},
		// Ollama has no api_key; CfgKeyHost is the primary key its ProviderEntry
		// reads, so the stub URL must land there instead of CfgKeyBaseURL.
		providers.NameOllama:      {model: "llama3.2", baseURLKey: providers.CfgKeyHost},
		providers.NameOllamaCloud: {model: "gpt-oss:120b"},
		providers.NameOpenRouter:  {model: "openrouter/auto"},
		providers.NamePerplexity:  {model: "sonar"},
		providers.NameQwen:        {model: "qwen-turbo"},
		providers.NameSambaNova:   {model: "Meta-Llama-3.1-8B-Instruct"},
		providers.NameTogether:    {model: "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo"},
		providers.NameXAI:         {model: "grok-2-latest"},
	}
}

// uncoveredProviders lists every provider deliberately absent from the fixture
// table, with the reason. TestConformanceCoverage fails on any provider that is
// in neither map, so conformance coverage stays honest as providers are added.
// Reserved for transports that genuinely cannot be pointed at an httptest stub.
func uncoveredProviders() map[string]string {
	return map[string]string{
		providers.NameBedrock: "AWS SDK transport (SigV4-signed, endpoint resolved by the SDK); " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
		providers.NameVertexAI: "requires GCP OAuth (service account / ADC) and a project+region; " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
	}
}

// streamFixture is one provider's streaming conformance case. The native SSE
// event stream its CompleteStream must translate lives in
// providers/<dir>/testdata/chat.stream.sse (loaded by the harness); this struct
// carries the streaming invariants a payload cannot express.
type streamFixture struct {
	// model is the request model; it must be advertised by ConfiguredModels().
	model string

	// extraCfg mirrors fixture.extraCfg.
	extraCfg providers.ProviderConfig

	// baseURLKey mirrors fixture.baseURLKey — empty means providers.CfgKeyBaseURL.
	baseURLKey string

	// newStub builds the upstream stub when a static chat.stream.sse cannot
	// represent the provider's transport. Only Replicate sets this — its async
	// prediction API needs a submit, a stream leg, and a metrics leg wired to
	// one server. When set, the harness ignores the (absent) testdata file.
	newStub func(t *testing.T) *httptest.Server

	// noUsage marks a provider whose streaming path never reports usage at all,
	// so the generic usage assertion is skipped rather than failing on a gap the
	// adapter's own tests already document.
	noUsage bool

	// wantID, when set, is the response id the provider must stamp on every
	// chunk — the id the fixture's native stream reports.
	wantID string

	// wantCacheReadTokens/wantReasoningTokens assert the extended usage fields
	// core.Usage.UnmarshalJSON folds from alternate wire shapes survive the
	// generic openaicompat stream decode, not just the bespoke non-streaming one.
	wantCacheReadTokens int
	wantReasoningTokens int
}

// streamFixtures maps a provider ID to its streaming conformance case. It covers
// every provider but Bedrock and Vertex AI (see uncoveredStreamProviders); each
// is built through its OWN ProviderEntry against its OWN stub. The native SSE
// each stub serves lives in that provider's testdata/chat.stream.sse, except
// Replicate, whose three-leg transport is wired by newStub instead.
func streamFixtures() map[string]streamFixture {
	return map[string]streamFixture{
		providers.NameOpenAI:    {model: "gpt-4o-mini"},
		providers.NameAnthropic: {model: "claude-sonnet-4-6"},
		// Gemini and Cohere stamp a response id the client correlates on; assert
		// the exact id the native stream reports, not merely that one is present.
		providers.NameGemini: {model: "gemini-2.5-flash", wantID: "gemini-stream-conformance"},
		providers.NameCohere: {model: "command-r", wantID: "cohere-stream-conformance"},

		// Replicate — a three-phase transport: CompleteStream submits a prediction
		// (JSON), GETs the URL it returns for the event/data SSE stream, and then
		// reads the prediction once more for its token metrics, which arrive only
		// after the stream completes. newStub wires all three legs against the
		// same httptest.Server, so this fixture asserts the usage the streaming
		// path now reports rather than documenting its absence.
		providers.NameReplicate: {
			model: "meta/meta-llama-3-8b-instruct",
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
					// The metrics read the stream makes once it is done. It has to
					// be answered as JSON: served the SSE body below, it fails to
					// unmarshal and usage goes silently absent, which is what this
					// fixture used to record as a gap.
					if r.URL.Path == "/v1/predictions/pred-conformance" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"pred-conformance","status":"succeeded","metrics":{"input_token_count":` +
							strconv.Itoa(wantPromptTokens) + `,"output_token_count":` + strconv.Itoa(wantCompletionTokens) + `}}`))
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

		// DeepSeek routes through the generic providers/core/openaicompat stream
		// decode (unlike its bespoke Complete decode), so this fixture proves
		// core.Usage's alternate-shape folding (prompt_cache_hit_tokens,
		// completion_tokens_details.reasoning_tokens) also survives streaming.
		providers.NameDeepSeek: {model: "deepseek-chat", wantCacheReadTokens: 4, wantReasoningTokens: 3},

		providers.NameGroq:    {model: "llama-3.1-8b-instant"},
		providers.NameAI21:    {model: "jamba-mini-1.7"},
		providers.NameMistral: {model: "mistral-small-latest"},

		providers.NameAzureFoundry: {model: "gpt-4o"},
		providers.NameAzureOpenAI: {
			model:    "gpt-4o-mini",
			extraCfg: providers.ProviderConfig{providers.CfgKeyDeployment: "gpt-4o-mini"},
		},
		providers.NameCerebras: {model: "llama-3.3-70b"},
		providers.NameCloudflare: {
			model:    "@cf/meta/llama-3.1-8b-instruct",
			extraCfg: providers.ProviderConfig{providers.CfgKeyAccountID: "conformance-account"},
		},
		providers.NameDatabricks:  {model: "databricks-claude-sonnet-4-5"},
		providers.NameDeepInfra:   {model: "deepseek-ai/DeepSeek-R1"},
		providers.NameFireworks:   {model: "accounts/fireworks/models/llama-v3p1-8b-instruct"},
		providers.NameHuggingFace: {model: "meta-llama/Meta-Llama-3.1-8B-Instruct"},
		providers.NameMoonshot:    {model: "kimi-k2.5"},
		providers.NameNovita:      {model: "deepseek/deepseek-v3.2"},
		providers.NameNVIDIANIM:   {model: "meta/llama-3.1-8b-instruct"},
		providers.NameOllama:      {model: "llama3.2", baseURLKey: providers.CfgKeyHost},
		providers.NameOllamaCloud: {model: "gpt-oss:120b"},
		providers.NameOpenRouter:  {model: "openrouter/auto"},
		providers.NamePerplexity:  {model: "sonar"},
		providers.NameQwen:        {model: "qwen-turbo"},
		providers.NameSambaNova:   {model: "Meta-Llama-3.1-8B-Instruct"},
		providers.NameTogether:    {model: "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo"},
		providers.NameXAI:         {model: "grok-2-latest"},
	}
}

// uncoveredStreamProviders lists every provider deliberately absent from
// streamFixtures, with a reason specific to streaming. TestConformanceCoverage
// fails on any provider in neither map.
func uncoveredStreamProviders() map[string]string {
	return map[string]string{
		providers.NameBedrock: "AWS SDK transport (SigV4-signed, endpoint resolved by the SDK); " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
		providers.NameVertexAI: "requires GCP OAuth (service account / ADC) and a project+region; " +
			"its ProviderEntry exposes no base-URL key, so it cannot be pointed at an httptest stub",
	}
}
