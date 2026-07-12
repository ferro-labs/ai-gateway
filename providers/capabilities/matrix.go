// Package capabilities declares, from a single source of truth, which OpenAI
// chat-completion parameters each provider can express.
//
// The matrix is DERIVED from the parameter lists the providers already declare
// in their own request builders (the `supported` slices passed to
// core.WarnUnsupportedParams). It records exceptions only: any parameter not
// listed for a provider defaults to Forward, and any provider without an entry
// forwards everything. Nothing here invents support a provider does not have —
// each entry mirrors the provider's existing declared behaviour.
package capabilities

// Support classifies how a provider handles a given OpenAI chat parameter.
type Support int

const (
	// Forward means the parameter is passed through to the provider unchanged.
	Forward Support = iota
	// Translate means the adapter maps the parameter onto a provider-native
	// equivalent (e.g. Gemini's response_format → responseMimeType).
	Translate
	// Unsupported means the provider cannot express the parameter; it is
	// warned, dropped, or rejected per the configured compatibility mode.
	Unsupported
)

// String returns the lowercase wire name of the Support value
// ("forward", "translate", "unsupported"), used by the /v1/capabilities API.
func (s Support) String() string {
	switch s {
	case Translate:
		return "translate"
	case Unsupported:
		return "unsupported"
	default:
		return "forward"
	}
}

// Profile maps OpenAI chat parameter names to their Support for one provider.
// It records exceptions only; absent params default to Forward.
type Profile map[string]Support

// AllParams is the canonical set of OpenAI chat-completion parameters, taken
// from the JSON tags on core.Request. ProfileOf materialises a full profile
// over this set for the /v1/capabilities response.
var AllParams = []string{
	"temperature",
	"top_p",
	"n",
	"seed",
	"max_tokens",
	"max_completion_tokens",
	"presence_penalty",
	"frequency_penalty",
	"stop",
	"tools",
	"tool_choice",
	"response_format",
	"logprobs",
	"top_logprobs",
	"stream",
	"stream_options",
	"parallel_tool_calls",
	"user",
	"logit_bias",
}

// unsupported builds a Profile marking each named parameter Unsupported.
func unsupported(params ...string) Profile {
	p := make(Profile, len(params))
	for _, name := range params {
		p[name] = Unsupported
	}
	return p
}

// matrix holds per-provider exceptions to the Forward default. Provider IDs are
// the canonical Name constants, kept as literals to avoid an import cycle:
// some native providers (e.g. ai21) import the shared openaicompat builder,
// which imports this package. The drift-guard test cross-checks every key
// against providers.AllProviders(), and the unit tests pin each Unsupported set.
//
// Each entry is the complement of the provider's declared supported list:
//   - anthropic → providers/anthropic anthropicSupportedParams
//   - bedrock   → providers/bedrock bedrockSupportedParams (model-dependent; see below)
//   - cohere    → providers/cohere cohereSupportedParams
//   - gemini    → providers/gemini geminiSupportedParams
//   - replicate → providers/replicate forwardedTextParams
//   - ai21      → providers/ai21 inline WarnUnsupportedParams list
//
// Only the parameters governed by the providers' warn mechanism are derived
// here; stream, stream_options, and parallel_tool_calls fall outside it
// (streaming is handled natively) and default to Forward.
var matrix = map[string]Profile{
	"anthropic": unsupported(
		"n", "seed", "max_completion_tokens", "presence_penalty",
		"frequency_penalty", "response_format", "logprobs", "top_logprobs", "logit_bias",
	),
	// Bedrock parameter support is model-dependent (Anthropic, Titan, Nova, and
	// Llama families each expose a different set). The matrix is provider-level
	// for now: it encodes the common/base intersection across families
	// (temperature, top_p, max_tokens), so anything outside that is Unsupported
	// at the provider level.
	"bedrock": unsupported(
		"n", "seed", "max_completion_tokens", "presence_penalty", "frequency_penalty",
		"stop", "tools", "tool_choice", "response_format", "logprobs", "top_logprobs",
		"user", "logit_bias",
	),
	"cohere": unsupported(
		"n", "max_completion_tokens", "response_format",
		"logprobs", "top_logprobs", "user", "logit_bias",
	),
	"gemini": geminiProfile(),
	"replicate": unsupported(
		"n", "max_completion_tokens", "tools", "tool_choice",
		"response_format", "logprobs", "top_logprobs", "user", "logit_bias",
	),
	"ai21": unsupported(
		"n", "seed", "max_completion_tokens", "presence_penalty", "frequency_penalty",
		"tools", "tool_choice", "response_format", "logprobs", "top_logprobs",
		"user", "logit_bias",
	),
}

// geminiProfile derives Gemini's profile from geminiSupportedParams, then marks
// response_format as Translate: providers/gemini/gemini.go maps the json_object
// and json_schema response formats onto Gemini's native responseMimeType.
func geminiProfile() Profile {
	p := unsupported("max_completion_tokens", "logprobs", "top_logprobs", "user", "logit_bias")
	p["response_format"] = Translate
	return p
}

// SupportOf returns the declared Support for a provider/parameter pair. Unknown
// providers and unknown/future parameters default to Forward so the matrix never
// breaks on inputs it does not model.
func SupportOf(providerID, param string) Support {
	if p, ok := matrix[providerID]; ok {
		if s, ok := p[param]; ok {
			return s
		}
	}
	return Forward
}

// ProfileOf materialises the full Support profile for a provider over AllParams
// (defaults plus overrides), for the /v1/capabilities response.
func ProfileOf(providerID string) map[string]Support {
	out := make(map[string]Support, len(AllParams))
	for _, param := range AllParams {
		out[param] = SupportOf(providerID, param)
	}
	return out
}
