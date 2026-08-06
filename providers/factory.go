package providers

import (
	"fmt"
	"os"
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
			return nil
		}
		if val != "" {
			cfg[m.ConfigKey] = val
		}
	}
	// If a custom configured? gate is provided, apply it after reading env vars.
	// ConfiguredFn is used for providers whose activation depends on an OR
	// condition across multiple env vars (e.g. Bedrock: AWS_REGION OR
	// AWS_ACCESS_KEY_ID). When ConfiguredFn is set, no EnvMapping needs
	// Required=true for that provider.
	if entry.ConfiguredFn != nil && !entry.ConfiguredFn(cfg) {
		return nil
	}
	return cfg
}

// ProviderConfig is a string key-value map for constructing a provider.
//
// This is the single input type for all provider factories, enabling two
// init modes that cover every deployment scenario:
//
//   - OSS self-hosted: populate from environment variables via ProviderConfigFromEnv.
//   - Programmatic injection: populate from an encrypted credential store or a
//     per-tenant secret source without touching env vars.
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
	CfgKeyAccountID  = "account_id"  // provider account / tenant identifier when required

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
	CapabilityChat          = "chat"          // Provider.Complete  — always present
	CapabilityStream        = "stream"        // StreamProvider
	CapabilityEmbed         = "embed"         // EmbeddingProvider
	CapabilityImage         = "image"         // ImageProvider
	CapabilityDiscovery     = "discovery"     // DiscoveryProvider
	CapabilityProxy         = "proxy"         // ProxiableProvider
	CapabilityRerank        = "rerank"        // RerankProvider
	CapabilityModeration    = "moderation"    // ModerationProvider
	CapabilityTranscription = "transcription" // TranscriptionProvider
	CapabilitySpeech        = "speech"        // SpeechProvider
	CapabilityBatch         = "batch"         // BatchProvider (Files + Batch pass-through)
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
	//
	// The "required config keys are absent" half is enforced by AllProviders()
	// and GetProviderEntry(), which return entries whose Build applies
	// requireConfigured first. The closure written here therefore only has to
	// validate what the declared mappings cannot express — a value's shape, or
	// a constraint between two keys.
	Build func(cfg ProviderConfig) (Provider, error)

	// ConfiguredFn is an optional custom "configured?" gate used by
	// ProviderConfigFromEnv after all env vars have been read. When non-nil it
	// takes the place of the default Required=true EnvMapping gate for this
	// provider. Return false to signal "not configured" (silent skip).
	//
	// Use this for providers whose activation depends on an OR condition across
	// multiple env vars — for example, Bedrock is considered configured when
	// AWS_BEARER_TOKEN_BEDROCK, AWS_REGION, or AWS_ACCESS_KEY_ID is set
	// (allowing bearer auth, instance-role auth with an explicit region, or
	// explicit static credentials without a region env var). When ConfiguredFn
	// is set, no EnvMapping needs Required=true.
	ConfiguredFn func(cfg ProviderConfig) bool
}

// requireConfigured reports whether cfg satisfies the entry's own declared
// "configured?" gate — the Required EnvMappings, plus ConfiguredFn when the
// entry sets one.
//
// It is deliberately the same gate, in the same order, that
// ProviderConfigFromEnv applies to the environment. The environment path runs
// it before building and skips the provider silently; the programmatic path had
// no equivalent, so a ProviderConfig assembled from a credential store could
// construct a provider with no credential at all and fail later as an empty
// Authorization header. Required metadata that only one of the two callers
// honours is not a contract.
//
// ConfiguredFn already expresses the OR across alternative credential sets that
// bedrock (bearer token OR region OR access key) and vertex-ai (explicit key OR
// service account OR ADC) need, so nothing new has to describe them.
func (e ProviderEntry) requireConfigured(cfg ProviderConfig) error {
	for _, m := range e.EnvMappings {
		if m.Required && cfg[m.ConfigKey] == "" {
			return fmt.Errorf("%s: missing required config key %q (%s)", e.ID, m.ConfigKey, m.EnvVar)
		}
	}
	if e.ConfiguredFn != nil && !e.ConfiguredFn(cfg) {
		return fmt.Errorf("%s: config satisfies none of the accepted credential sets", e.ID)
	}
	return nil
}

// gateRequiredKeys returns e with Build wrapped in requireConfigured.
//
// Wrapping here rather than at each call site is what makes the gate hold: Build
// is an exported field, so every caller reaches it directly and a gate that had
// to be invoked separately would be enforced only where someone remembered. The
// two accessors below already hand out copies, so the wrap costs no per-entry
// edit and cannot be bypassed by anything outside this package.
func gateRequiredKeys(e ProviderEntry) ProviderEntry {
	gate, inner := e, e.Build
	e.Build = func(cfg ProviderConfig) (Provider, error) {
		if err := gate.requireConfigured(cfg); err != nil {
			return nil, err
		}
		return inner(cfg)
	}
	return e
}

// AllProviders returns the complete ordered list of built-in ProviderEntry records.
// The slice is a copy — mutations do not affect the internal registry.
func AllProviders() []ProviderEntry {
	out := make([]ProviderEntry, len(allProviders))
	for i, e := range allProviders {
		out[i] = gateRequiredKeys(e)
	}
	return out
}

// GetProviderEntry returns the ProviderEntry for the given canonical provider ID.
// ok is false if the ID is not registered.
func GetProviderEntry(id string) (ProviderEntry, bool) {
	for _, e := range allProviders {
		if e.ID == id {
			return gateRequiredKeys(e), true
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
