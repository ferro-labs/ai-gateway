package providers

// Canonical provider name constants.
//
// These constants define the permanent, immutable identity of each provider
// in the gateway. They are used as:
//   - Registry keys (providers.Registry.Get / Register)
//   - Routing config target names (gateway_configs, config.yaml "provider:" field)
//   - Provider name returned by Provider.Name()
//   - Log / metric labels
//
// IMPORTANT: These values are a stable public contract.
// Changing any constant value is a BREAKING CHANGE that invalidates:
//   - Persisted routing configs in SQLite / PostgreSQL
//   - YAML / JSON gateway config files
//   - Client code that matches on provider name strings
//
// To add a new provider: add a new constant here, use it in the provider
// struct initialisation (Base{name: NameXxx, ...}), and add it to the
// AllProviders registry in factory.go.
const (
	// NameOpenAI is the canonical name for the OpenAI provider.
	NameOpenAI = "openai"

	// NameAnthropic is the canonical name for the Anthropic provider.
	NameAnthropic = "anthropic"

	// NameGemini is the canonical name for the Google Gemini provider.
	NameGemini = "gemini"

	// NameGroq is the canonical name for the Groq provider.
	NameGroq = "groq"

	// NameTogether is the canonical name for the Together AI provider.
	NameTogether = "together"

	// NameMistral is the canonical name for the Mistral AI provider.
	NameMistral = "mistral"

	// NameCohere is the canonical name for the Cohere provider.
	NameCohere = "cohere"

	// NameDeepSeek is the canonical name for the DeepSeek provider.
	NameDeepSeek = "deepseek"

	// NamePerplexity is the canonical name for the Perplexity provider.
	NamePerplexity = "perplexity"

	// NameFireworks is the canonical name for the Fireworks AI provider.
	NameFireworks = "fireworks"

	// NameAI21 is the canonical name for the AI21 Labs provider.
	NameAI21 = "ai21"

	// NameXAI is the canonical name for the xAI (Grok) provider.
	NameXAI = "xai"

	// NameAzureOpenAI is the canonical name for the Azure OpenAI provider.
	// Note: uses a hyphen, not an underscore.
	NameAzureOpenAI = "azure-openai"

	// NameAzureFoundry is the canonical name for the Azure AI Foundry provider.
	// Note: uses a hyphen, not an underscore.
	NameAzureFoundry = "azure-foundry"

	// NameVertexAI is the canonical name for the Google Vertex AI provider.
	// Note: uses a hyphen, not an underscore.
	NameVertexAI = "vertex-ai"

	// NameHuggingFace is the canonical name for the Hugging Face provider.
	// Note: uses a hyphen, not an underscore.
	NameHuggingFace = "hugging-face"

	// NameBedrock is the canonical name for the AWS Bedrock provider.
	NameBedrock = "bedrock"

	// NameOllama is the canonical name for the Ollama (local) provider.
	NameOllama = "ollama"

	// NameReplicate is the canonical name for the Replicate provider.
	NameReplicate = "replicate"
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
		NameCohere,
		NameDeepSeek,
		NameFireworks,
		NameGemini,
		NameGroq,
		NameHuggingFace,
		NameMistral,
		NameOllama,
		NameOpenAI,
		NamePerplexity,
		NameReplicate,
		NameTogether,
		NameVertexAI,
		NameXAI,
	}
}
