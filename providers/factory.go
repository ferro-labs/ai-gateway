package providers

import (
	"fmt"
	"os"
	"strings"
)

// ProviderConfigFromEnv reads environment variables for the given ProviderEntry
// and returns a populated ProviderConfig, or nil if the provider is not configured.
//
// "Not configured" means any EnvMapping with Required=true has an empty env var.
// In that case, nil is returned and the provider should be silently skipped.
//
// If all required env vars are present, a populated ProviderConfig is returned.
// The caller should then call entry.Build(cfg) which validates secondary
// constraints (e.g. azure-openai needing both endpoint AND deployment).
func ProviderConfigFromEnv(entry ProviderEntry) ProviderConfig {
	cfg := make(ProviderConfig, len(entry.EnvMappings))
	for _, m := range entry.EnvMappings {
		val := os.Getenv(m.EnvVar)
		if val == "" && m.Required {
			// Primary required key is not set — provider is not configured.
			return nil
		}
		if val != "" {
			cfg[m.ConfigKey] = val
		}
	}
	return cfg
}

// ProviderConfig is a string key-value map for constructing a provider.
//
// This is the single input type for all provider factories, enabling two
// init modes that cover every deployment scenario:
//
//   - OSS self-hosted: populate from environment variables via ProviderConfigFromEnv.
//   - Cloud / tenant injection: populate from an encrypted credential store
//     (e.g. FerroCloud's credentials domain) without touching env vars.
//
// Standard key names are defined as CfgKey* constants below.
type ProviderConfig map[string]string

// Standard ProviderConfig key names.
// Use these constants — never raw strings — when building or reading a ProviderConfig.
const (
	// Universal keys
	CfgKeyAPIKey     = "api_key"     // primary API key / token
	CfgKeyBaseURL    = "base_url"    // optional base URL override
	CfgKeyAPIVersion = "api_version" // optional API version string

	// Azure OpenAI
	CfgKeyDeployment = "deployment" // deployment / model name for Azure OpenAI

	// Vertex AI
	CfgKeyProjectID          = "project_id"           // GCP project ID
	CfgKeyRegion             = "region"               // GCP region (vertex-ai) or AWS region (bedrock)
	CfgKeyServiceAccountJSON = "service_account_json" // Vertex AI service-account JSON

	// AWS Bedrock
	CfgKeyAccessKeyID     = "access_key_id"     // AWS access key ID
	CfgKeySecretAccessKey = "secret_access_key" // AWS secret access key
	CfgKeySessionToken    = "session_token"     // AWS session token (optional)

	// Ollama
	CfgKeyHost   = "host"   // Ollama server host (primary required key)
	CfgKeyModels = "models" // comma-separated model list

	// Replicate
	CfgKeyAPIToken   = "api_token"   // Replicate API token (primary required key)
	CfgKeyTextModels = "text_models" // comma-separated Replicate text model paths
	CfgKeyImageModels = "image_models" // comma-separated Replicate image model paths
)

// Capability names for capability-based registry filtering.
const (
	CapabilityChat      = "chat"      // Provider.Complete  — always present
	CapabilityStream    = "stream"    // StreamProvider
	CapabilityEmbed     = "embed"     // EmbeddingProvider
	CapabilityImage     = "image"     // ImageProvider
	CapabilityDiscovery = "discovery" // DiscoveryProvider
	CapabilityProxy     = "proxy"     // ProxiableProvider
)

// EnvMapping maps a single ProviderConfig key to its environment variable.
// Required=true means: if the env var is unset, the provider is considered
// "not configured" and is silently skipped during auto-registration.
type EnvMapping struct {
	ConfigKey string
	EnvVar    string
	Required  bool
}

// ProviderEntry is the complete self-describing registration record for a
// provider. Each provider has exactly one entry in allProviders.
//
// Callers should use AllProviders() rather than referencing allProviders directly.
type ProviderEntry struct {
	// ID is the canonical provider name (one of the Name* constants).
	// This value MUST match the string returned by the constructed provider's Name().
	ID string

	// Capabilities lists optional interfaces the provider implements beyond
	// the base Provider interface. Use CapabilityXxx constants.
	Capabilities []string

	// EnvMappings documents the environment variables this provider reads.
	// ProviderConfigFromEnv uses these to build a ProviderConfig automatically.
	// EnvMappings with Required=true act as the "configured?" gate:
	// if any required env var is unset, ProviderConfigFromEnv returns nil
	// (provider is skipped, not an error).
	EnvMappings []EnvMapping

	// Build constructs the provider from an explicit ProviderConfig.
	// Returns an error if required config keys are absent or invalid.
	// Never reads environment variables directly — callers supply all inputs.
	Build func(cfg ProviderConfig) (Provider, error)
}

// allProviders is the canonical ordered registry of all built-in providers.
// Order is alphabetical by ID. Add new providers here and nowhere else.
var allProviders = []ProviderEntry{
	{
		ID:           NameAI21,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "AI21_API_KEY", true},
			{CfgKeyBaseURL, "AI21_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			p, err := NewAI21(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
			return p, err
		},
	},
	{
		ID:           NameAnthropic,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "ANTHROPIC_API_KEY", true},
			{CfgKeyBaseURL, "ANTHROPIC_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewAnthropic(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameAzureFoundry,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "AZURE_FOUNDRY_API_KEY", true},
			{CfgKeyBaseURL, "AZURE_FOUNDRY_ENDPOINT", true},
			{CfgKeyAPIVersion, "AZURE_FOUNDRY_API_VERSION", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			if cfg[CfgKeyBaseURL] == "" {
				return nil, fmt.Errorf("%s: base_url (AZURE_FOUNDRY_ENDPOINT) is required", NameAzureFoundry)
			}
			return NewAzureFoundry(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL], cfg[CfgKeyAPIVersion])
		},
	},
	{
		ID:           NameAzureOpenAI,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "AZURE_OPENAI_API_KEY", true},
			{CfgKeyBaseURL, "AZURE_OPENAI_ENDPOINT", true},
			{CfgKeyDeployment, "AZURE_OPENAI_DEPLOYMENT", true},
			{CfgKeyAPIVersion, "AZURE_OPENAI_API_VERSION", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			if cfg[CfgKeyBaseURL] == "" {
				return nil, fmt.Errorf("%s: base_url (AZURE_OPENAI_ENDPOINT) is required", NameAzureOpenAI)
			}
			if cfg[CfgKeyDeployment] == "" {
				return nil, fmt.Errorf("%s: deployment (AZURE_OPENAI_DEPLOYMENT) is required", NameAzureOpenAI)
			}
			return NewAzureOpenAI(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL], cfg[CfgKeyDeployment], cfg[CfgKeyAPIVersion])
		},
	},
	{
		ID:           NameBedrock,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		// AWS_REGION is required; static credentials are optional (falls back to
		// the default AWS credential chain: env → ~/.aws/credentials → instance role).
		EnvMappings: []EnvMapping{
			{CfgKeyRegion, "AWS_REGION", true},
			{CfgKeyAccessKeyID, "AWS_ACCESS_KEY_ID", false},
			{CfgKeySecretAccessKey, "AWS_SECRET_ACCESS_KEY", false},
			{CfgKeySessionToken, "AWS_SESSION_TOKEN", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewBedrockWithOptions(BedrockOptions{
				Region:          cfg[CfgKeyRegion],
				AccessKeyID:     cfg[CfgKeyAccessKeyID],
				SecretAccessKey: cfg[CfgKeySecretAccessKey],
				SessionToken:    cfg[CfgKeySessionToken],
			})
		},
	},
	{
		ID:           NameCohere,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "COHERE_API_KEY", true},
			{CfgKeyBaseURL, "COHERE_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewCohere(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameDeepSeek,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "DEEPSEEK_API_KEY", true},
			{CfgKeyBaseURL, "DEEPSEEK_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewDeepSeek(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameFireworks,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityDiscovery, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "FIREWORKS_API_KEY", true},
			{CfgKeyBaseURL, "FIREWORKS_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewFireworks(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameGemini,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "GEMINI_API_KEY", true},
			{CfgKeyBaseURL, "GEMINI_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewGemini(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameGroq,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "GROQ_API_KEY", true},
			{CfgKeyBaseURL, "GROQ_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewGroq(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameHuggingFace,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityEmbed, CapabilityImage, CapabilityDiscovery, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "HUGGING_FACE_API_KEY", true},
			{CfgKeyBaseURL, "HUGGING_FACE_ENDPOINT", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewHuggingFace(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameMistral,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "MISTRAL_API_KEY", true},
			{CfgKeyBaseURL, "MISTRAL_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewMistral(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameOllama,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		// Ollama has no API key; CfgKeyHost acts as the "configured?" gate.
		EnvMappings: []EnvMapping{
			{CfgKeyHost, "OLLAMA_HOST", true},
			{CfgKeyModels, "OLLAMA_MODELS", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			var models []string
			if m := cfg[CfgKeyModels]; m != "" {
				models = strings.Split(m, ",")
			}
			return NewOllama(cfg[CfgKeyHost], models)
		},
	},
	{
		ID:           NameOpenAI,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityEmbed, CapabilityImage, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "OPENAI_API_KEY", true},
			{CfgKeyBaseURL, "OPENAI_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewOpenAI(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NamePerplexity,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityDiscovery, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "PERPLEXITY_API_KEY", true},
			{CfgKeyBaseURL, "PERPLEXITY_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewPerplexity(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameReplicate,
		Capabilities: []string{CapabilityChat, CapabilityImage, CapabilityProxy},
		// Replicate uses api_token (not api_key) as its primary key.
		EnvMappings: []EnvMapping{
			{CfgKeyAPIToken, "REPLICATE_API_TOKEN", true},
			{CfgKeyBaseURL, "REPLICATE_BASE_URL", false},
			{CfgKeyTextModels, "REPLICATE_TEXT_MODELS", false},
			{CfgKeyImageModels, "REPLICATE_IMAGE_MODELS", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			var textModels, imageModels []string
			if m := cfg[CfgKeyTextModels]; m != "" {
				textModels = strings.Split(m, ",")
			}
			if m := cfg[CfgKeyImageModels]; m != "" {
				imageModels = strings.Split(m, ",")
			}
			return NewReplicate(cfg[CfgKeyAPIToken], cfg[CfgKeyBaseURL], textModels, imageModels)
		},
	},
	{
		ID:           NameTogether,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "TOGETHER_API_KEY", true},
			{CfgKeyBaseURL, "TOGETHER_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewTogether(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameVertexAI,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		// project_id is the gate: if unset, skip silently.
		// region plus one of api_key / service_account_json are required once
		// project_id is present.
		EnvMappings: []EnvMapping{
			{CfgKeyProjectID, "VERTEX_AI_PROJECT_ID", true},
			{CfgKeyRegion, "VERTEX_AI_REGION", false},
			{CfgKeyAPIKey, "VERTEX_AI_API_KEY", false},
			{CfgKeyServiceAccountJSON, "VERTEX_AI_SERVICE_ACCOUNT_JSON", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			if cfg[CfgKeyRegion] == "" {
				return nil, fmt.Errorf("%s: region (VERTEX_AI_REGION) is required when project_id is set", NameVertexAI)
			}
			if cfg[CfgKeyAPIKey] == "" && cfg[CfgKeyServiceAccountJSON] == "" {
				return nil, fmt.Errorf("%s: either api_key (VERTEX_AI_API_KEY) or service_account_json (VERTEX_AI_SERVICE_ACCOUNT_JSON) is required", NameVertexAI)
			}
			return NewVertexAI(VertexAIOptions{
				ProjectID:          cfg[CfgKeyProjectID],
				Region:             cfg[CfgKeyRegion],
				APIKey:             cfg[CfgKeyAPIKey],
				ServiceAccountJSON: cfg[CfgKeyServiceAccountJSON],
			})
		},
	},
	{
		ID:           NameXAI,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityDiscovery, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "XAI_API_KEY", true},
			{CfgKeyBaseURL, "XAI_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return NewXAI(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
}

// AllProviders returns the complete ordered list of built-in ProviderEntry records.
// The slice is a copy — mutations do not affect the internal registry.
func AllProviders() []ProviderEntry {
	out := make([]ProviderEntry, len(allProviders))
	copy(out, allProviders)
	return out
}

// GetProviderEntry returns the ProviderEntry for the given canonical provider ID.
// ok is false if the ID is not registered.
func GetProviderEntry(id string) (ProviderEntry, bool) {
	for _, e := range allProviders {
		if e.ID == id {
			return e, true
		}
	}
	return ProviderEntry{}, false
}

// ProviderHasCapability reports whether the named provider declares the
// given capability (one of the CapabilityXxx constants).
func ProviderHasCapability(id, capability string) bool {
	e, ok := GetProviderEntry(id)
	if !ok {
		return false
	}
	for _, c := range e.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}
