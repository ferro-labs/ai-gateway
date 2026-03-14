package providers

import (
	ai21pkg "github.com/ferro-labs/ai-gateway/providers/ai21"
	anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"
	azurefoundrypkg "github.com/ferro-labs/ai-gateway/providers/azure_foundry"
	azureopenaipkg "github.com/ferro-labs/ai-gateway/providers/azure_openai"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
	cerebraspkg "github.com/ferro-labs/ai-gateway/providers/cerebras"
	cloudflarepkg "github.com/ferro-labs/ai-gateway/providers/cloudflare"
	coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"
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

// Canonical provider name constants.
//
// Each constant is sourced directly from its provider subpackage — the
// subpackage is the single source of truth for its own identity string.
// These re-exports exist so that code importing the root providers package
// can use providers.NameOpenAI etc. without knowing the subpackage paths.
//
// IMPORTANT: These values are a stable public contract.
// Changing any constant value is a BREAKING CHANGE that invalidates:
//   - Persisted routing configs in SQLite / PostgreSQL
//   - YAML / JSON gateway config files
//   - Client code that matches on provider name strings
//
// To add a new provider: add a constant here re-exporting from the new
// subpackage, and add its ProviderEntry to providers_list.go.
const (
	// NameOpenAI is the canonical name for the OpenAI provider.
	NameOpenAI = openaipkg.Name

	// NameAnthropic is the canonical name for the Anthropic provider.
	NameAnthropic = anthropicpkg.Name

	// NameGemini is the canonical name for the Google Gemini provider.
	NameGemini = geminipkg.Name

	// NameGroq is the canonical name for the Groq provider.
	NameGroq = groqpkg.Name

	// NameTogether is the canonical name for the Together AI provider.
	NameTogether = togetherpkg.Name

	// NameMistral is the canonical name for the Mistral AI provider.
	NameMistral = mistralpkg.Name

	// NameCohere is the canonical name for the Cohere provider.
	NameCohere = coherepkg.Name

	// NameDeepSeek is the canonical name for the DeepSeek provider.
	NameDeepSeek = deepseekpkg.Name

	// NamePerplexity is the canonical name for the Perplexity provider.
	NamePerplexity = perplexitypkg.Name

	// NameFireworks is the canonical name for the Fireworks AI provider.
	NameFireworks = fireworkspkg.Name

	// NameAI21 is the canonical name for the AI21 Labs provider.
	NameAI21 = ai21pkg.Name

	// NameXAI is the canonical name for the xAI (Grok) provider.
	NameXAI = xaipkg.Name

	// NameAzureOpenAI is the canonical name for the Azure OpenAI provider.
	NameAzureOpenAI = azureopenaipkg.Name

	// NameAzureFoundry is the canonical name for the Azure AI Foundry provider.
	NameAzureFoundry = azurefoundrypkg.Name

	// NameVertexAI is the canonical name for the Google Vertex AI provider.
	NameVertexAI = vertexaipkg.Name

	// NameHuggingFace is the canonical name for the Hugging Face provider.
	NameHuggingFace = huggingfacepkg.Name

	// NameBedrock is the canonical name for the AWS Bedrock provider.
	NameBedrock = bedrockpkg.Name

	// NameCerebras is the canonical name for the Cerebras provider.
	NameCerebras = cerebraspkg.Name

	// NameCloudflare is the canonical name for the Cloudflare Workers AI provider.
	NameCloudflare = cloudflarepkg.Name

	// NameOllama is the canonical name for the Ollama (local) provider.
	NameOllama = ollamapkg.Name

	// NameDatabricks is the canonical name for the Databricks provider.
	NameDatabricks = databrickspkg.Name

	// NameDeepInfra is the canonical name for the DeepInfra provider.
	NameDeepInfra = deepinfrapkg.Name

	// NameNovita is the canonical name for the Novita provider.
	NameNovita = novitapkg.Name

	// NameMoonshot is the canonical name for the Moonshot AI provider.
	NameMoonshot = moonshotpkg.Name

	// NameNVIDIANIM is the canonical name for the NVIDIA NIM provider.
	NameNVIDIANIM = nvidianimpkg.Name

	// NameQwen is the canonical name for the Qwen provider.
	NameQwen = qwenpkg.Name

	// NameReplicate is the canonical name for the Replicate provider.
	NameReplicate = replicatepkg.Name

	// NameSambaNova is the canonical name for the SambaNova provider.
	NameSambaNova = sambanovapkg.Name

	// NameOpenRouter is the canonical name for the OpenRouter provider.
	NameOpenRouter = openrouterpkg.Name
)

// AllProviderNames returns every registered canonical provider name in a
// deterministic, alphabetically sorted order.
// Use this for validation, documentation generation, and test fixtures.
func AllProviderNames() []string {
	return []string{
		NameAI21,
		NameAnthropic,
		NameAzureFoundry,
		NameAzureOpenAI,
		NameBedrock,
		NameCerebras,
		NameCloudflare,
		NameCohere,
		NameDatabricks,
		NameDeepInfra,
		NameDeepSeek,
		NameFireworks,
		NameGemini,
		NameGroq,
		NameHuggingFace,
		NameMistral,
		NameMoonshot,
		NameNovita,
		NameNVIDIANIM,
		NameOllama,
		NameOpenAI,
		NameOpenRouter,
		NamePerplexity,
		NameQwen,
		NameReplicate,
		NameSambaNova,
		NameTogether,
		NameVertexAI,
		NameXAI,
	}
}
