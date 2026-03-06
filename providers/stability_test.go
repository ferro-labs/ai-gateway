package providers

import (
	"slices"
	"testing"
)

// TestProviderNameStability verifies that every provider's Name() method returns
// its canonical name constant. This test is a DATA CONTRACT:
//
//   - The canonical name constants in names.go define the stable public identity
//     of each provider across all environments.
//   - Gateway routing configs (YAML, JSON, PostgreSQL) persist these strings.
//     A change to any Name() return value would silently break persisted configs.
//   - Cloud credential stores index provider credentials by these strings.
//
// If this test fails, you have introduced a breaking change. Fix the Name()
// implementation, not this test.
func TestProviderNameStability(t *testing.T) {
	cases := []struct {
		wantName string
		build    func(t *testing.T) Provider
	}{
		{
			wantName: NameAI21,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewAI21(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewAI21: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameAnthropic,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewAnthropic(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewAnthropic: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameAzureFoundry,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewAzureFoundry(testAPIKey, "https://example.openai.azure.com", "")
				if err != nil {
					t.Fatalf("NewAzureFoundry: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameAzureOpenAI,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewAzureOpenAI(testAPIKey, "https://example.openai.azure.com", "gpt-4o", "")
				if err != nil {
					t.Fatalf("NewAzureOpenAI: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameBedrock,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewBedrock("us-east-1")
				if err != nil {
					t.Fatalf("NewBedrock: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameCohere,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewCohere(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewCohere: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameDeepSeek,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewDeepSeek(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewDeepSeek: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameFireworks,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewFireworks(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewFireworks: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameGemini,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewGemini(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewGemini: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameGroq,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewGroq(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewGroq: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameHuggingFace,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewHuggingFace(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewHuggingFace: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameMistral,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewMistral(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewMistral: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameOllama,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewOllama("http://localhost:11434", nil)
				if err != nil {
					t.Fatalf("NewOllama: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameOpenAI,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewOpenAI(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewOpenAI: %v", err)
				}
				return p
			},
		},
		{
			wantName: NamePerplexity,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewPerplexity(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewPerplexity: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameReplicate,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewReplicate(testAPIKey, "", nil, nil)
				if err != nil {
					t.Fatalf("NewReplicate: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameTogether,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewTogether(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewTogether: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameVertexAI,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewVertexAI(VertexAIOptions{
					ProjectID: "test-project",
					Region:    "us-central1",
					APIKey:    testAPIKey,
				})
				if err != nil {
					t.Fatalf("NewVertexAI: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameXAI,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := NewXAI(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewXAI: %v", err)
				}
				return p
			},
		},
	}

	if len(cases) != len(AllProviderNames()) {
		t.Errorf("stability test has %d cases but AllProviderNames() returns %d — add the missing provider to both", len(cases), len(AllProviderNames()))
	}

	seen := make(map[string]bool)
	for _, tc := range cases {
		t.Run(tc.wantName, func(t *testing.T) {
			p := tc.build(t)
			if got := p.Name(); got != tc.wantName {
				t.Errorf("Name() = %q, want constant %q (changing provider names breaks persisted routing configs)", got, tc.wantName)
			}
			if seen[tc.wantName] {
				t.Errorf("duplicate test case for name %q", tc.wantName)
			}
			seen[tc.wantName] = true
		})
	}
}

// TestAllProvidersRegistryCompleteness verifies that the AllProviders() factory
// registry covers every canonical provider name exactly once.
func TestAllProvidersRegistryCompleteness(t *testing.T) {
	entries := AllProviders()
	canonical := AllProviderNames()

	if len(entries) != len(canonical) {
		t.Errorf("AllProviders() has %d entries but AllProviderNames() has %d — they must stay in sync", len(entries), len(canonical))
	}

	// Every Name* constant must have a factory entry.
	entryIDs := make(map[string]bool, len(entries))
	for _, e := range entries {
		if entryIDs[e.ID] {
			t.Errorf("duplicate factory entry for provider %q", e.ID)
		}
		entryIDs[e.ID] = true
		if e.Build == nil {
			t.Errorf("provider %q has nil Build function in factory registry", e.ID)
		}
	}

	for _, name := range canonical {
		if !entryIDs[name] {
			t.Errorf("provider %q is in AllProviderNames() but missing from AllProviders() factory registry", name)
		}
	}
}

// TestProviderEntryIDMatchesNameConstant verifies that each ProviderEntry.ID
// is one of the Name* constants, not an arbitrary string.
func TestProviderEntryIDMatchesNameConstant(t *testing.T) {
	canonical := AllProviderNames()
	for _, e := range AllProviders() {
		if !slices.Contains(canonical, e.ID) {
			t.Errorf("ProviderEntry.ID = %q is not in AllProviderNames() — use a Name* constant", e.ID)
		}
	}
}

// TestProviderCapabilitiesNotEmpty verifies every provider declares at least
// the base "chat" capability.
func TestProviderCapabilitiesNotEmpty(t *testing.T) {
	for _, e := range AllProviders() {
		if len(e.Capabilities) == 0 {
			t.Errorf("provider %q has empty Capabilities slice — must include at least %q", e.ID, CapabilityChat)
		}
		hasChat := slices.Contains(e.Capabilities, CapabilityChat)
		if !hasChat {
			t.Errorf("provider %q is missing %q capability", e.ID, CapabilityChat)
		}
	}
}

// TestProviderEnvMappingsHaveRequiredKey verifies that each provider entry has
// at least one required EnvMapping (the "configured?" gate used by auto-registration).
func TestProviderEnvMappingsHaveRequiredKey(t *testing.T) {
	for _, e := range AllProviders() {
		hasRequired := false
		for _, m := range e.EnvMappings {
			if m.Required {
				hasRequired = true
				break
			}
		}
		if !hasRequired {
			t.Errorf("provider %q has no required EnvMapping — at least one must be required to act as the configured? gate", e.ID)
		}
	}
}
