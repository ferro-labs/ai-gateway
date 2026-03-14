package providers

import (
	"fmt"
	"strings"

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
		// All Bedrock env mappings are optional because the provider can be
		// configured in two different ways:
		//   1. Instance-role / credential-chain auth: only AWS_REGION is set.
		//   2. Static credentials: AWS_ACCESS_KEY_ID (+ secret) are set;
		//      region may be absent and defaults to us-east-1 inside NewWithOptions.
		// The ConfiguredFn below mirrors the dual-key gate used in main.go:
		// Bedrock is considered configured when AWS_REGION OR AWS_ACCESS_KEY_ID
		// is present.
		EnvMappings: []EnvMapping{
			{CfgKeyRegion, "AWS_REGION", false},
			{CfgKeyAccessKeyID, "AWS_ACCESS_KEY_ID", false},
			{CfgKeySecretAccessKey, "AWS_SECRET_ACCESS_KEY", false},
			{CfgKeySessionToken, "AWS_SESSION_TOKEN", false},
		},
		ConfiguredFn: func(cfg ProviderConfig) bool {
			return cfg[CfgKeyRegion] != "" || cfg[CfgKeyAccessKeyID] != ""
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
		ID:           NameCerebras,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityDiscovery, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "CEREBRAS_API_KEY", true},
			{CfgKeyBaseURL, "CEREBRAS_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return cerebraspkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameCloudflare,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityEmbed, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "CLOUDFLARE_API_KEY", true},
			{CfgKeyAccountID, "CLOUDFLARE_ACCOUNT_ID", true},
			{CfgKeyBaseURL, "CLOUDFLARE_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return cloudflarepkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyAccountID], cfg[CfgKeyBaseURL])
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
		ID:           NameDatabricks,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "DATABRICKS_TOKEN", true},
			{CfgKeyBaseURL, "DATABRICKS_HOST", true},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return databrickspkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameDeepInfra,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "DEEPINFRA_API_KEY", true},
			{CfgKeyBaseURL, "DEEPINFRA_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return deepinfrapkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
		ID:           NameMoonshot,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "MOONSHOT_API_KEY", true},
			{CfgKeyBaseURL, "MOONSHOT_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return moonshotpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameNovita,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityDiscovery, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "NOVITA_API_KEY", true},
			{CfgKeyBaseURL, "NOVITA_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return novitapkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
		},
	},
	{
		ID:           NameNVIDIANIM,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "NVIDIA_NIM_API_KEY", true},
			{CfgKeyBaseURL, "NVIDIA_NIM_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return nvidianimpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
		ID:           NameOpenRouter,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityDiscovery, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "OPENROUTER_API_KEY", true},
			{CfgKeyBaseURL, "OPENROUTER_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return openrouterpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
		ID:           NameQwen,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "QWEN_API_KEY", true},
			{CfgKeyBaseURL, "QWEN_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return qwenpkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
		ID:           NameSambaNova,
		Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
		EnvMappings: []EnvMapping{
			{CfgKeyAPIKey, "SAMBANOVA_API_KEY", true},
			{CfgKeyBaseURL, "SAMBANOVA_BASE_URL", false},
		},
		Build: func(cfg ProviderConfig) (Provider, error) {
			return sambanovapkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
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
