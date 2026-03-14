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
type providerNameStabilityCase struct {
	wantName string
	build    func(t *testing.T) Provider
}

func providerNameStabilityCases() []providerNameStabilityCase {
	return []providerNameStabilityCase{
		{
			wantName: NameAI21,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := ai21pkg.New(testAPIKey, "")
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
				p, err := anthropicpkg.New(testAPIKey, "")
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
				p, err := azurefoundrypkg.New(testAPIKey, "https://example.openai.azure.com", "")
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
				p, err := azureopenaipkg.New(testAPIKey, "https://example.openai.azure.com", "gpt-4o", "")
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
				p, err := bedrockpkg.New("us-east-1")
				if err != nil {
					t.Fatalf("NewBedrock: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameCerebras,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := cerebraspkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewCerebras: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameCloudflare,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := cloudflarepkg.New(testAPIKey, "acct-123", "")
				if err != nil {
					t.Fatalf("NewCloudflare: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameCohere,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := coherepkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewCohere: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameDatabricks,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := databrickspkg.New(testAPIKey, "https://dbc.example.com")
				if err != nil {
					t.Fatalf("NewDatabricks: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameDeepInfra,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := deepinfrapkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewDeepInfra: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameDeepSeek,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := deepseekpkg.New(testAPIKey, "")
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
				p, err := fireworkspkg.New(testAPIKey, "")
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
				p, err := geminipkg.New(testAPIKey, "")
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
				p, err := groqpkg.New(testAPIKey, "")
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
				p, err := huggingfacepkg.New(testAPIKey, "")
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
				p, err := mistralpkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewMistral: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameMoonshot,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := moonshotpkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewMoonshot: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameNovita,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := novitapkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewNovita: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameNVIDIANIM,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := nvidianimpkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewNVIDIANIM: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameOllama,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := ollamapkg.New("http://localhost:11434", nil)
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
				p, err := openaipkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewOpenAI: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameOpenRouter,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := openrouterpkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewOpenRouter: %v", err)
				}
				return p
			},
		},
		{
			wantName: NamePerplexity,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := perplexitypkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewPerplexity: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameQwen,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := qwenpkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewQwen: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameReplicate,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := replicatepkg.New(testAPIKey, "", nil, nil)
				if err != nil {
					t.Fatalf("NewReplicate: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameSambaNova,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := sambanovapkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewSambaNova: %v", err)
				}
				return p
			},
		},
		{
			wantName: NameTogether,
			build: func(t *testing.T) Provider {
				t.Helper()
				p, err := togetherpkg.New(testAPIKey, "")
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
				p, err := vertexaipkg.New(vertexaipkg.Options{
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
				p, err := xaipkg.New(testAPIKey, "")
				if err != nil {
					t.Fatalf("NewXAI: %v", err)
				}
				return p
			},
		},
	}
}

func TestProviderNameStability(t *testing.T) {
	cases := providerNameStabilityCases()

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
// a configured? gate: either at least one EnvMapping with Required=true, or a
// ConfiguredFn (used for providers whose gate is an OR across multiple env vars,
// e.g. Bedrock: AWS_REGION OR AWS_ACCESS_KEY_ID).
func TestProviderEnvMappingsHaveRequiredKey(t *testing.T) {
	for _, e := range AllProviders() {
		if e.ConfiguredFn != nil {
			// Custom gate provided — no Required mapping needed.
			continue
		}
		hasRequired := false
		for _, m := range e.EnvMappings {
			if m.Required {
				hasRequired = true
				break
			}
		}
		if !hasRequired {
			t.Errorf("provider %q has no configured? gate: add a Required EnvMapping or a ConfiguredFn", e.ID)
		}
	}
}
