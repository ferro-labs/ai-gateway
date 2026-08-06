package providers

import (
	"os"
	"strings"
	"testing"
)

// syntheticValue returns a plausible value for a ProviderConfig key, so a
// factory can be handed an otherwise-complete config and the ONE removed key is
// the only thing it can be reacting to.
func syntheticValue(key, baseURL string) string {
	switch key {
	case CfgKeyAPIKey:
		return "synthetic-api-key"
	case CfgKeyAPIToken:
		return "synthetic-api-token"
	case CfgKeyBaseURL, CfgKeyHost:
		return baseURL
	case CfgKeyAPIVersion:
		return "2024-01-01"
	case CfgKeyAccountID:
		return "synthetic-account"
	case CfgKeyDeployment:
		return "synthetic-deployment"
	case CfgKeyProjectID:
		return "synthetic-project"
	case CfgKeyRegion:
		return "us-east-1"
	case CfgKeyAccessKeyID:
		return "synthetic-access-key"
	case CfgKeySecretAccessKey:
		return "synthetic-secret-key"
	case CfgKeySessionToken:
		return "synthetic-session-token"
	case CfgKeyModels:
		return "synthetic-model"
	case CfgKeyTextModels:
		return "synthetic/text-model"
	case CfgKeyImageModels:
		return "synthetic/image-model"
	default:
		return ""
	}
}

func completeConfig(e ProviderEntry, baseURL string) ProviderConfig {
	cfg := make(ProviderConfig, len(e.EnvMappings))
	for _, m := range e.EnvMappings {
		if v := syntheticValue(m.ConfigKey, baseURL); v != "" {
			cfg[m.ConfigKey] = v
		}
	}
	return cfg
}

// TestBuildRefusesMissingRequiredKey is the contract ProviderEntry.Build states
// and only the environment path used to keep: a config missing a key the entry
// DECLARES required is refused, whoever assembled it.
//
// The gap it closes is the documented programmatic-injection mode — a
// ProviderConfig built from a per-tenant credential store rather than from env
// vars. That path skipped the Required metadata entirely, so a provider could be
// constructed with no credential at all and the mistake surfaced later as an
// empty Authorization header against a real upstream.
//
// It is driven from AllProviders() rather than a hand-written table, so a
// thirty-first provider inherits it without anyone writing a line.
func TestBuildRefusesMissingRequiredKey(t *testing.T) {
	const baseURL = "http://127.0.0.1:1"

	checked := 0
	for _, entry := range AllProviders() {
		complete := completeConfig(entry, baseURL)
		for _, m := range entry.EnvMappings {
			if !m.Required {
				continue
			}
			checked++
			cfg := make(ProviderConfig, len(complete))
			for k, v := range complete {
				if k != m.ConfigKey {
					cfg[k] = v
				}
			}
			if _, err := entry.Build(cfg); err == nil {
				t.Errorf("%s: Build() accepted a config missing required key %q (%s); "+
					"a declared required key must be enforced for an explicit ProviderConfig too",
					entry.ID, m.ConfigKey, m.EnvVar)
			} else if !strings.Contains(err.Error(), m.ConfigKey) {
				t.Errorf("%s: Build() rejected a config missing %q with %v; the error must name the missing key",
					entry.ID, m.ConfigKey, err)
			}
		}
	}

	// A guard that silently checked nothing reports green for a contract it
	// never read.
	if checked < 25 {
		t.Fatalf("only %d required mappings checked; the scan is not reaching the provider entries", checked)
	}
}

// TestBuildAcceptsCompleteConfig is the other half: the gate must refuse only
// what is genuinely absent. A config carrying every declared key reaches the
// entry's own Build closure, whatever that closure then decides about the
// values.
func TestBuildAcceptsCompleteConfig(t *testing.T) {
	const baseURL = "http://127.0.0.1:1"

	for _, entry := range AllProviders() {
		if err := entry.requireConfigured(completeConfig(entry, baseURL)); err != nil {
			t.Errorf("%s: a config carrying every declared key was refused by the gate: %v", entry.ID, err)
		}
	}
}

// TestEnvPathIsUnchangedByTheGate holds the OSS registration path exactly where
// it was. ProviderConfigFromEnv already applies this gate and returns nil for an
// unconfigured provider, which bootstrap skips silently; the gate below it must
// therefore never fire on a config that path produced, or a provider would be
// refused twice and the second refusal would be logged as an init failure.
func TestEnvPathIsUnchangedByTheGate(t *testing.T) {
	for _, entry := range AllProviders() {
		for _, m := range entry.EnvMappings {
			t.Setenv(m.EnvVar, syntheticValue(m.ConfigKey, "http://127.0.0.1:1"))
		}
		cfg := ProviderConfigFromEnv(entry)
		if cfg == nil {
			t.Errorf("%s: ProviderConfigFromEnv returned nil with every declared env var set", entry.ID)
			continue
		}
		if err := entry.requireConfigured(cfg); err != nil {
			t.Errorf("%s: the gate refused a config ProviderConfigFromEnv accepted: %v", entry.ID, err)
		}
	}
}

// TestEnvPathStillSkipsSilently pins the other direction of the same boundary: a
// missing required env var stays a silent skip (nil config) rather than becoming
// an error, because a target whose credential has not rolled out yet must not
// crash startup.
func TestEnvPathStillSkipsSilently(t *testing.T) {
	entry, ok := GetProviderEntry(NameOpenAI)
	if !ok {
		t.Fatal("openai provider entry not found")
	}
	for _, m := range entry.EnvMappings {
		t.Setenv(m.EnvVar, "")
	}
	if cfg := ProviderConfigFromEnv(entry); cfg != nil {
		t.Errorf("ProviderConfigFromEnv() = %v with OPENAI_API_KEY unset, want nil (silent skip)", cfg)
	}
}

// TestConfiguredFnGatesAlternativeCredentialSets covers the two providers whose
// credential requirement is an OR rather than a list: bedrock accepts a bearer
// token, a region, or an access key, and vertex-ai must still reach its
// Application Default Credentials path with no explicit key.
//
// ConfiguredFn already expressed that OR for the environment. The programmatic
// path now reads the same declaration, so nothing new describes these two.
func TestConfiguredFnGatesAlternativeCredentialSets(t *testing.T) {
	bedrock, ok := GetProviderEntry(NameBedrock)
	if !ok {
		t.Fatal("bedrock provider entry not found")
	}

	if err := bedrock.requireConfigured(ProviderConfig{}); err == nil {
		t.Error("bedrock: an empty config satisfied the gate; none of bearer token, region or access key is present")
	}
	for _, key := range []string{CfgKeyAPIKey, CfgKeyRegion, CfgKeyAccessKeyID} {
		if err := bedrock.requireConfigured(ProviderConfig{key: "set"}); err != nil {
			t.Errorf("bedrock: %q alone was refused: %v", key, err)
		}
	}

	vertex, ok := GetProviderEntry(NameVertexAI)
	if !ok {
		t.Fatal("vertex-ai provider entry not found")
	}
	// project_id is the declared gate; the ADC path deliberately carries no key.
	if err := vertex.requireConfigured(ProviderConfig{CfgKeyProjectID: "demo", CfgKeyRegion: "us-central1"}); err != nil {
		t.Errorf("vertex-ai: the no-explicit-key (ADC) config was refused by the gate: %v", err)
	}
	if err := vertex.requireConfigured(ProviderConfig{CfgKeyRegion: "us-central1"}); err == nil {
		t.Error("vertex-ai: a config with no project_id satisfied the gate")
	}
}

// TestGateReadsTheProcessEnvironmentNever guards the one thing the wrapper could
// quietly reintroduce: Build must decide from the ProviderConfig it is handed,
// so a programmatic caller injecting a tenant's credential cannot have the
// gateway's own environment answer for it.
func TestGateReadsTheProcessEnvironmentNever(t *testing.T) {
	entry, ok := GetProviderEntry(NameOpenAI)
	if !ok {
		t.Fatal("openai provider entry not found")
	}
	t.Setenv("OPENAI_API_KEY", "sk-ambient-process-key")
	if _, err := entry.Build(ProviderConfig{CfgKeyBaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("Build() accepted a config with no api_key while one sat in the process environment")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Fatal("test setup failed: the ambient env var was not set")
	}
}
