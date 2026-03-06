package providers

import (
	"fmt"
	"strings"

	ai21pkg "github.com/ferro-labs/ai-gateway/providers/ai21"
	anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"
	azurefoundrypkg "github.com/ferro-labs/ai-gateway/providers/azure_foundry"
	azureopenaipkg "github.com/ferro-labs/ai-gateway/providers/azure_openai"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
	coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"
	deepseekpkg "github.com/ferro-labs/ai-gateway/providers/deepseek"
	fireworkspkg "github.com/ferro-labs/ai-gateway/providers/fireworks"
	geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"
	groqpkg "github.com/ferro-labs/ai-gateway/providers/groq"
	huggingfacepkg "github.com/ferro-labs/ai-gateway/providers/hugging_face"
	mistralpkg "github.com/ferro-labs/ai-gateway/providers/mistral"
	ollamapkg "github.com/ferro-labs/ai-gateway/providers/ollama"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
	perplexitypkg "github.com/ferro-labs/ai-gateway/providers/perplexity"
	replicatepkg "github.com/ferro-labs/ai-gateway/providers/replicate"
	togetherpkg "github.com/ferro-labs/ai-gateway/providers/together"
	vertexaipkg "github.com/ferro-labs/ai-gateway/providers/vertex_ai"
	xaipkg "github.com/ferro-labs/ai-gateway/providers/xai"
)

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
			p, err := ai21pkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return anthropicpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return azurefoundrypkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL], cfg[CfgKeyAPIVersion])
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
			return azureopenaipkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL], cfg[CfgKeyDeployment], cfg[CfgKeyAPIVersion])
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
			return bedrockpkg.NewWithOptions(bedrockpkg.Options{
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
			return coherepkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return deepseekpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return fireworkspkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return geminipkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return groqpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return huggingfacepkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return mistralpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return ollamapkg.New(cfg[CfgKeyHost], models)
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
			return openaipkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return perplexitypkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return replicatepkg.New(cfg[CfgKeyAPIToken], cfg[CfgKeyBaseURL], textModels, imageModels)
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
			return togetherpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
			return vertexaipkg.New(vertexaipkg.Options{
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
			return xaipkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
}
