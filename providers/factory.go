package providers

import "os"

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
	CfgKeyAPIToken    = "api_token"    // Replicate API token (primary required key)
	CfgKeyTextModels  = "text_models"  // comma-separated Replicate text model paths
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
