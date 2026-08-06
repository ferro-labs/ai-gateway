// Package redact_test holds the cross-package guard that keeps value redaction
// extensible. It is an external test package because providers depends on
// redact (through pkg/logger), so only a _test package may import it back.
package redact_test

import (
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/providers"
)

// credentialConfigKeys are the ProviderConfig keys whose value is secret. Keys
// absent here — base_url, region, deployment, models, project_id — are settings,
// not credentials, and enrolling them for value redaction would blank ordinary
// text such as a hostname out of every log line.
var credentialConfigKeys = map[string]bool{
	providers.CfgKeyAPIKey:          true,
	providers.CfgKeyAPIToken:        true,
	providers.CfgKeyAccessKeyID:     true,
	providers.CfgKeySecretAccessKey: true,
	providers.CfgKeySessionToken:    true,
}

// TestEveryProviderCredentialIsEnrolled is the extensibility claim, enforced.
//
// Value redaction selects environment variables by name, so a provider whose
// credential variable is named conventionally is covered the moment its
// ProviderEntry lands — no redaction change, and no list here to remember. This
// test is what makes that true rather than merely hoped for: it walks every
// registered provider and fails if a credential variable would go unrecognised.
//
// A thirtieth provider added with MISTRAL_API_KEY-style naming passes untouched.
// One named FOO_CREDS fails here, at the moment the provider is added, with the
// fix stated: rename the variable, or add its name component to
// credentialNameParts.
func TestEveryProviderCredentialIsEnrolled(t *testing.T) {
	// Vertex AI's service-account variable holds a multi-kilobyte JSON document
	// rather than a token. Whole-value matching cannot help — the document never
	// appears verbatim in an error string — so the private key inside it is
	// covered by the private_key_block policy instead.
	skip := map[string]bool{"VERTEX_AI_SERVICE_ACCOUNT_JSON": true}

	// A credential-shaped value: long enough to clear the floor, no whitespace.
	value := "Zq7" + strings.Repeat("x", 40)

	checked := 0
	for _, entry := range providers.AllProviders() {
		for _, m := range entry.EnvMappings {
			if !credentialConfigKeys[m.ConfigKey] || skip[m.EnvVar] {
				continue
			}
			checked++
			if !redact.EnvValueIsCredential(m.EnvVar, value) {
				t.Errorf("provider %s: credential env var %s is not enrolled for value redaction; "+
					"rename it to carry a _KEY / _TOKEN / _SECRET component, or add that component to credentialNameParts",
					entry.ID, m.EnvVar)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no provider credential env vars were checked — the walk is broken")
	}
	t.Logf("%d provider credential environment variables enrolled by construction", checked)
}

// TestProviderSettingsAreNotEnrolled is the other half: a base URL or a region
// must not be value-redacted, or every log line naming the upstream host would
// come out blank.
func TestProviderSettingsAreNotEnrolled(t *testing.T) {
	for _, entry := range providers.AllProviders() {
		for _, m := range entry.EnvMappings {
			if credentialConfigKeys[m.ConfigKey] {
				continue
			}
			if redact.EnvValueIsCredential(m.EnvVar, "https://api.example.com/v1") {
				t.Errorf("provider %s: setting env var %s (%s) is treated as a credential",
					entry.ID, m.EnvVar, m.ConfigKey)
			}
		}
	}
}
