package providers

// This test is the machine-checkable half of providers/README.md.
// The doc claims, per provider, which endpoints the gateway implements; this
// asserts the SAME claims against the actual optional-interface set each
// provider's *Provider satisfies. If code gains or loses an interface, or the
// doc drifts from the code, exactly one of them is now wrong and this fails —
// so the matrix cannot quietly lie.
//
// It uses typed-nil instances purely for interface assertions (no method is
// called), the runtime form of the compile-time `var _ core.XProvider =
// (*Provider)(nil)` guards each provider already carries — checked here as a
// complete SET so a missing guard is caught too.

import (
	"testing"

	ai21pkg "github.com/ferro-labs/ai-gateway/providers/ai21"
	anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"
	azurefoundrypkg "github.com/ferro-labs/ai-gateway/providers/azure_foundry"
	azureopenaipkg "github.com/ferro-labs/ai-gateway/providers/azure_openai"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
	cerebraspkg "github.com/ferro-labs/ai-gateway/providers/cerebras"
	cloudflarepkg "github.com/ferro-labs/ai-gateway/providers/cloudflare"
	coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"
	"github.com/ferro-labs/ai-gateway/providers/core"
	databrickspkg "github.com/ferro-labs/ai-gateway/providers/databricks"
	deepinfrapkg "github.com/ferro-labs/ai-gateway/providers/deepinfra"
	deepseekpkg "github.com/ferro-labs/ai-gateway/providers/deepseek"
	fireworkspkg "github.com/ferro-labs/ai-gateway/providers/fireworks"
	geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"
	groqpkg "github.com/ferro-labs/ai-gateway/providers/groq"
	huggingfacepkg "github.com/ferro-labs/ai-gateway/providers/hugging_face"
	mistralpkg "github.com/ferro-labs/ai-gateway/providers/mistral"
	moonshotpkg "github.com/ferro-labs/ai-gateway/providers/moonshot"
	novitapkg "github.com/ferro-labs/ai-gateway/providers/novita"
	nvidianimpkg "github.com/ferro-labs/ai-gateway/providers/nvidia_nim"
	ollamapkg "github.com/ferro-labs/ai-gateway/providers/ollama"
	ollamacloudpkg "github.com/ferro-labs/ai-gateway/providers/ollama_cloud"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
	openrouterpkg "github.com/ferro-labs/ai-gateway/providers/openrouter"
	perplexitypkg "github.com/ferro-labs/ai-gateway/providers/perplexity"
	qwenpkg "github.com/ferro-labs/ai-gateway/providers/qwen"
	replicatepkg "github.com/ferro-labs/ai-gateway/providers/replicate"
	sambanovapkg "github.com/ferro-labs/ai-gateway/providers/sambanova"
	togetherpkg "github.com/ferro-labs/ai-gateway/providers/together"
	vertexaipkg "github.com/ferro-labs/ai-gateway/providers/vertex_ai"
	xaipkg "github.com/ferro-labs/ai-gateway/providers/xai"
)

// endpointSupport is the optional-interface set a provider satisfies. chat
// (core.Provider) is omitted because every provider satisfies it by definition.
type endpointSupport struct {
	stream        bool // core.StreamProvider
	embed         bool // core.EmbeddingProvider
	image         bool // core.ImageProvider
	discovery     bool // core.DiscoveryProvider
	proxiable     bool // core.ProxiableProvider (BaseURL + AuthHeaders)
	nonWire       bool // core.NonOpenAIWireProvider (proxiable, but /v1/* returns 501)
	rerank        bool // core.RerankProvider
	moderation    bool // core.ModerationProvider
	transcription bool // core.TranscriptionProvider
	speech        bool // core.SpeechProvider
	batch         bool // core.BatchProvider
}

// expectedSupport is providers/README.md, in code. Effective
// pass-through proxy is proxiable && !nonWire.
var expectedSupport = map[string]endpointSupport{
	NameAI21:         {stream: true, proxiable: true},
	NameAnthropic:    {stream: true, discovery: true, proxiable: true, nonWire: true},
	NameAzureFoundry: {stream: true, embed: true, proxiable: true, nonWire: true},
	NameAzureOpenAI:  {stream: true, embed: true, image: true, proxiable: true, nonWire: true, transcription: true, speech: true, batch: true},
	NameBedrock:      {stream: true, embed: true, image: true, proxiable: true, nonWire: true, rerank: true},
	NameCerebras:     {stream: true, discovery: true, proxiable: true},
	NameCloudflare:   {stream: true, embed: true, proxiable: true},
	NameCohere:       {stream: true, embed: true, proxiable: true, nonWire: true, rerank: true},
	NameDatabricks:   {stream: true, embed: true, proxiable: true},
	NameDeepInfra:    {stream: true, embed: true, image: true, discovery: true, proxiable: true, rerank: true, transcription: true, speech: true},
	NameDeepSeek:     {stream: true, discovery: true, proxiable: true},
	NameFireworks:    {stream: true, embed: true, discovery: true, proxiable: true, transcription: true},
	NameGemini:       {stream: true, embed: true, image: true, proxiable: true, nonWire: true},
	NameGroq:         {stream: true, discovery: true, proxiable: true, transcription: true, speech: true, batch: true},
	NameHuggingFace:  {stream: true, embed: true, image: true, discovery: true, proxiable: true},
	NameMistral:      {stream: true, embed: true, discovery: true, proxiable: true, moderation: true, transcription: true, speech: true},
	NameMoonshot:     {stream: true, discovery: true, proxiable: true},
	NameNovita:       {stream: true, embed: true, discovery: true, proxiable: true, batch: true},
	NameNVIDIANIM:    {stream: true, embed: true, discovery: true, proxiable: true, rerank: true},
	NameOllama:       {stream: true, embed: true, discovery: true, proxiable: true},
	NameOllamaCloud:  {stream: true, embed: true, discovery: true}, // no BaseURL/AuthHeaders → not proxiable
	NameOpenAI:       {stream: true, embed: true, image: true, discovery: true, proxiable: true, moderation: true, transcription: true, speech: true, batch: true},
	NameOpenRouter:   {stream: true, embed: true, discovery: true, proxiable: true},
	NamePerplexity:   {stream: true, proxiable: true},
	NameQwen:         {stream: true, embed: true, discovery: true, proxiable: true, batch: true},
	NameReplicate:    {stream: true, image: true, proxiable: true, nonWire: true},
	NameSambaNova:    {stream: true, embed: true, discovery: true, proxiable: true, transcription: true},
	NameTogether:     {stream: true, embed: true, image: true, discovery: true, proxiable: true, rerank: true, transcription: true, speech: true},
	NameVertexAI:     {stream: true, embed: true, image: true, proxiable: true, nonWire: true},
	NameXAI:          {stream: true, image: true, discovery: true, proxiable: true},
}

// providerTypes is one typed-nil instance per provider, used only for interface
// assertions. The type carries the method set; the nil value is never
// dereferenced.
var providerTypes = map[string]core.Provider{
	NameAI21:         (*ai21pkg.Provider)(nil),
	NameAnthropic:    (*anthropicpkg.Provider)(nil),
	NameAzureFoundry: (*azurefoundrypkg.Provider)(nil),
	NameAzureOpenAI:  (*azureopenaipkg.Provider)(nil),
	NameBedrock:      (*bedrockpkg.Provider)(nil),
	NameCerebras:     (*cerebraspkg.Provider)(nil),
	NameCloudflare:   (*cloudflarepkg.Provider)(nil),
	NameCohere:       (*coherepkg.Provider)(nil),
	NameDatabricks:   (*databrickspkg.Provider)(nil),
	NameDeepInfra:    (*deepinfrapkg.Provider)(nil),
	NameDeepSeek:     (*deepseekpkg.Provider)(nil),
	NameFireworks:    (*fireworkspkg.Provider)(nil),
	NameGemini:       (*geminipkg.Provider)(nil),
	NameGroq:         (*groqpkg.Provider)(nil),
	NameHuggingFace:  (*huggingfacepkg.Provider)(nil),
	NameMistral:      (*mistralpkg.Provider)(nil),
	NameMoonshot:     (*moonshotpkg.Provider)(nil),
	NameNovita:       (*novitapkg.Provider)(nil),
	NameNVIDIANIM:    (*nvidianimpkg.Provider)(nil),
	NameOllama:       (*ollamapkg.Provider)(nil),
	NameOllamaCloud:  (*ollamacloudpkg.Provider)(nil),
	NameOpenAI:       (*openaipkg.Provider)(nil),
	NameOpenRouter:   (*openrouterpkg.Provider)(nil),
	NamePerplexity:   (*perplexitypkg.Provider)(nil),
	NameQwen:         (*qwenpkg.Provider)(nil),
	NameReplicate:    (*replicatepkg.Provider)(nil),
	NameSambaNova:    (*sambanovapkg.Provider)(nil),
	NameTogether:     (*togetherpkg.Provider)(nil),
	NameVertexAI:     (*vertexaipkg.Provider)(nil),
	NameXAI:          (*xaipkg.Provider)(nil),
}

func actualSupport(p core.Provider) endpointSupport {
	_, stream := p.(core.StreamProvider)
	_, embed := p.(core.EmbeddingProvider)
	_, image := p.(core.ImageProvider)
	_, discovery := p.(core.DiscoveryProvider)
	_, proxiable := p.(core.ProxiableProvider)
	_, nonWire := p.(core.NonOpenAIWireProvider)
	_, rerank := p.(core.RerankProvider)
	_, moderation := p.(core.ModerationProvider)
	_, transcription := p.(core.TranscriptionProvider)
	_, speech := p.(core.SpeechProvider)
	_, batch := p.(core.BatchProvider)
	return endpointSupport{stream, embed, image, discovery, proxiable, nonWire, rerank, moderation, transcription, speech, batch}
}

// TestEndpointSupportMatchesCode asserts the documented support matrix equals
// the actual interface set of every registered provider, and that neither table
// has drifted from the registry (a new provider with no entry, or an entry
// naming one that no longer exists).
func TestEndpointSupportMatchesCode(t *testing.T) {
	for _, entry := range AllProviders() {
		if _, ok := expectedSupport[entry.ID]; !ok {
			t.Errorf("provider %q has no expectedSupport entry; add it here and a row to providers/README.md", entry.ID)
		}
		if _, ok := providerTypes[entry.ID]; !ok {
			t.Errorf("provider %q has no providerTypes entry", entry.ID)
		}
	}

	for id, want := range expectedSupport {
		if _, ok := GetProviderEntry(id); !ok {
			t.Errorf("expectedSupport names %q, which is not a registered provider", id)
			continue
		}
		p, ok := providerTypes[id]
		if !ok {
			t.Errorf("expectedSupport has %q but providerTypes does not", id)
			continue
		}
		if got := actualSupport(p); got != want {
			t.Errorf("%s: interface set = %+v, want %+v (code and providers/README.md disagree)", id, got, want)
		}
	}
}
