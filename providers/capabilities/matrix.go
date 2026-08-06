// Package capabilities declares, from a single source of truth, which OpenAI
// chat-completion parameters each provider can express.
//
// The matrix is the authoritative record of provider parameter support: native
// providers consult it via core.EnforceUnsupportedParams (which applies the
// configured warn/drop/reject mode), and GET /v1/capabilities serializes it.
// It records exceptions only: any parameter not listed for a provider defaults
// to Forward, and any provider without an entry forwards everything. The values
// were originally derived from each provider's own supported-parameter list, so
// they mirror real provider behaviour rather than inventing support.
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

// AllParams is the canonical set of OpenAI chat-completion parameters this
// matrix describes, taken from the JSON tags on core.Request. ProfileOf
// materialises a full profile over this set for the /v1/capabilities response.
//
// stream and stream_options are deliberately absent. They are transport
// controls the gateway resolves above the provider layer, not provider
// parameters: every routed stream is drained through internal/streamwrap, which
// applies the caller's stream_options.include_usage itself, and usage is
// requested upstream unconditionally so a client opting out cannot zero cost
// accounting. Publishing them here would answer a question about the provider
// with a fact about the gateway, whichever value it carried.
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
// Each entry lists the parameters the provider cannot express — the complement
// of its supported set — and cites the vendor documentation it was read from.
// "Cannot express" covers both a parameter the provider rejects and one it
// accepts and then ignores: in each case the caller's intent does not reach the
// model, which is what the compatibility mode exists to report. A parameter the
// vendor documents neither way is left Forward rather than guessed at.
//
// Bedrock support is model-dependent and stays computed per-model in
// providers/bedrock (see the bedrock entry below); the other entries are
// provider-level. These sets are the single source consumed by
// core.EnforceUnsupportedParams; the providers no longer keep their own lists.
//
// Only the parameters governed by the compatibility mechanism are recorded
// here.
var matrix = map[string]Profile{
	"anthropic": anthropicProfile(),
	// Bedrock parameter support is model-dependent (Anthropic, Titan, Nova, and
	// Llama families each expose a different set). The matrix is provider-level
	// for now: it encodes the common/base intersection across families
	// (temperature, top_p, max_tokens), so anything outside that is Unsupported
	// at the provider level.
	"bedrock": bedrockProfile(),
	"cohere": unsupported(
		"n", "max_completion_tokens", "response_format",
		"logprobs", "top_logprobs", "user", "logit_bias",
		// Cohere v2 /chat has tool_choice (REQUIRED|NONE) and no parallel-call
		// control of any kind, so the caller's intent cannot travel.
		"parallel_tool_calls",
	),
	// Groq answers logprobs, top_logprobs and logit_bias with a 400 outright
	// ("The following fields are currently not supported and will result in a
	// 400 error if they are supplied" —
	// https://console.groq.com/docs/openai). presence_penalty and
	// frequency_penalty are in the request schema but Groq's own description of
	// both is "This is not yet supported by any of our models"
	// (https://console.groq.com/docs/api-reference), so sending them loses the
	// caller's intent silently — which is what this matrix exists to surface.
	//
	// n is left Forward: Groq accepts it when it is 1 and 400s anything else,
	// and this matrix has no way to say "one value only". Marking it Unsupported
	// would reject the n=1 requests Groq serves.
	"groq": unsupported(
		"presence_penalty", "frequency_penalty",
		"logprobs", "top_logprobs", "logit_bias",
	),
	"gemini":  geminiProfile(),
	"mistral": mistralProfile(),
	// Ollama's OpenAI-compatibility document lists the request fields it
	// supports and leaves the rest unchecked: n, tool_choice, user and
	// logit_bias are explicitly not supported, and logprobs is unsupported as a
	// feature (which takes top_logprobs with it).
	// https://github.com/ollama/ollama/blob/main/docs/api/openai-compatibility.mdx
	//
	// Both providers speak the same /v1/chat/completions surface, so they carry
	// the same profile.
	"ollama":       ollamaProfile(),
	"ollama-cloud": ollamaProfile(),
	// DeepSeek still accepts both penalties and returns no error, but documents
	// each as "This parameter is no longer supported. It will not take effect if
	// you pass it to the API"
	// (https://api-docs.deepseek.com/api/create-chat-completion). Accepted and
	// inert is the case this matrix is for: nothing else tells the caller their
	// parameter did nothing.
	"deepseek": unsupported("presence_penalty", "frequency_penalty"),
	"replicate": unsupported(
		"n", "max_completion_tokens", "tools", "tool_choice",
		"response_format", "logprobs", "top_logprobs", "user", "logit_bias",
		// Replicate builds a bespoke prediction input with no tool calling at
		// all, so there is nothing for a parallel-call control to modify.
		"parallel_tool_calls",
	),
}

// anthropicProfile marks Anthropic's unsupported parameters, then
// parallel_tool_calls as Translate: providers/core/anthropicwire maps
// parallel_tool_calls:false onto Anthropic's inverse tool_choice flag
// disable_parallel_tool_use, synthesizing the default {"type":"auto"} choice
// when the caller sent tools but no explicit tool_choice.
func anthropicProfile() Profile {
	p := unsupported(
		"n", "seed", "max_completion_tokens", "presence_penalty",
		"frequency_penalty", "response_format", "logprobs", "top_logprobs", "logit_bias",
	)
	p["parallel_tool_calls"] = Translate
	return p
}

// bedrockProfile is the provider-level view; enforcement on Bedrock is
// per-model (bedrockSupportedParams in providers/bedrock), because Anthropic,
// Titan, Nova and Llama each expose a different set.
//
// parallel_tool_calls is Translate because the Anthropic families share
// anthropicwire's translation. It is the only Bedrock family with tool calling
// at all — the rest already declare tools Unsupported here — so there is no
// family for which this profile claims a translation that does not happen.
func bedrockProfile() Profile {
	p := unsupported(
		"n", "seed", "max_completion_tokens", "presence_penalty", "frequency_penalty",
		"stop", "tools", "tool_choice", "response_format", "logprobs", "top_logprobs",
		"user", "logit_bias",
	)
	p["parallel_tool_calls"] = Translate
	return p
}

// mistralProfile marks the parameters Mistral's published request schema does
// not carry. That schema sets additionalProperties:false
// (https://raw.githubusercontent.com/mistralai/platform-docs-public/main/openapi.yaml),
// so an unlisted field is a rejection rather than an omission.
//
// seed is Translate rather than Unsupported: Mistral's equivalent field is
// random_seed, and providers/mistral renames it on the way out.
func mistralProfile() Profile {
	p := unsupported("max_completion_tokens", "logprobs", "top_logprobs", "user", "logit_bias")
	p["seed"] = Translate
	return p
}

// ollamaProfile returns a fresh Profile for one Ollama-compatible provider, so
// the two entries that use it cannot share (and mutate) one map.
func ollamaProfile() Profile {
	return unsupported("n", "tool_choice", "logprobs", "top_logprobs", "user", "logit_bias")
}

// geminiProfile marks Gemini's unsupported parameters, then response_format as
// Translate: providers/gemini/gemini.go maps both JSON response formats onto
// Gemini's native responseMimeType, and forwards a json_schema's schema as
// generationConfig.responseSchema so the structure is enforced rather than
// degrading to free-form JSON. responseSchema takes an OpenAPI-3.0 subset, so
// $ref/$defs nodes are forwarded unconstrained — the ceiling documented on
// gemini.geminiResponseSchema.
//
// parallel_tool_calls is Unsupported: Gemini's toolConfig carries a function
// calling mode (AUTO|ANY|NONE) and no parallel-call control, so a caller asking
// for serial tool calls cannot be answered.
func geminiProfile() Profile {
	p := unsupported(
		"max_completion_tokens", "logprobs", "top_logprobs", "user", "logit_bias",
		"parallel_tool_calls",
	)
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

// HasProfile reports whether providerID has any exceptions recorded in the
// matrix at all. It is a single map lookup, cheap enough for callers on the
// request hot path (e.g. the shared openaicompat builder) to skip a per-param
// AllParams scan for the common case of a provider with no entry: such a
// provider forwards everything by definition, so there is nothing to find.
func HasProfile(providerID string) bool {
	_, ok := matrix[providerID]
	return ok
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
