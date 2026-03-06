# Provider Architecture Modernization — Implementation Draft

Status: All planned phases complete — Phase 4 (deprecation) merged  
Owner: ai-gateway maintainers  
Branch context: `feat/v0.6.5`  
Last updated: reflects commit `Phase 4 deprecation` (after `035aa60`)

## 1) Why this refactor

The `providers` package currently combines:

- public contracts (`Provider`, `Request`, `Response`),
- concrete implementations for all 19 providers,
- registry behavior (`providers/registry.go`),
- server-side environment wiring in `cmd/ferrogw/main.go`.

As provider count grows this creates high edit contention, poor discoverability for OSS contributors, and architectural friction for the FerroCloud premium layer which must inject per-tenant credentials from an encrypted database — not from environment variables.

There is also an existing `ProviderSource` enum (`base.go`: `SourceOpenAI`, `SourceAnthropic`, `SourceBedrock`, `SourceGoogle`, `SourceCohere`) that any new factory design must account for rather than ignore.

## 2) Goals

1. **Canonical identity contract** — one authoritative string constant per provider, tested against breakage, used as the database FK in FerroCloud's `gateway_configs` table.
2. **Two-mode factory** — OSS mode (`ProviderConfigFromEnv`) reads process env vars; cloud mode accepts an explicit `ProviderConfig` map injected from the tenant credential store.
3. **Zero-touch `main.go`** — adding a new provider requires no changes to `cmd/ferrogw/main.go`.
4. **Compile-time interface safety** — every provider file carries `var _ Interface = (*Provider)(nil)` assertions.
5. **Subpackage migration path** — move provider implementations under `providers/<id>/` to isolate edit contention, using root type aliases to preserve public API.
6. **OSS discoverability** — capability metadata (`CapabilityChat`, `CapabilityEmbed`, `CapabilityImage`, …) is machine-readable from `AllProviders()`.

## 3) Non-goals

- No behavior changes to request/response schemas in this refactor.
- No immediate removal of existing constructors (`NewOpenAI`, `NewBedrock`, etc.).
- No forced migration for downstream users in the first release wave.
- No changes to constructor signatures — `ProviderConfig` is the new input to factories only.

## 4) Breaking-change constraints

- Keep `package providers` as the stable facade.
- Keep existing constructor signatures available (wrap internally if needed).
- **Provider names are a data contract**, not just a code contract.  
  `NameOpenAI = "openai"`, `NameVertexAI = "vertex-ai"`, etc. are persisted in routing configs (YAML/JSON/PostgreSQL).  
  Changing them is equivalent to a destructive database migration.  
  The `TestProviderNameStability` test in `stability_test.go` catches drift in CI.
- Keep `providers.Registry` available while improving internals.

## 5) Current state — what is already built ✅

### `providers/names.go`
19 canonical `Name*` constants + `AllProviderNames() []string` (alphabetical).  
All 19 concrete provider structs now use `Base{name: NameXxx}` instead of raw string literals.

```go
const (
    NameOpenAI        = "openai"
    NameAnthropic     = "anthropic"
    NameAzureOpenAI   = "azure-openai"
    NameAzureFoundry  = "azure-foundry"
    NameBedrock       = "bedrock"
    NameVertexAI      = "vertex-ai"
    // ... 13 more
)
```

### `providers/factory.go`
Self-describing registry with two-mode credential injection (see §6).

Key types:
- `ProviderConfig map[string]string` — unified credential input
- `CfgKey*` constants — typed keys into `ProviderConfig`
- `Capability*` constants — machine-readable feature flags
- `EnvMapping{ConfigKey, EnvVar string; Required bool}` — maps credential key to env var; `Required=true` gates "is this provider configured?"
- `ProviderEntry{ID, Capabilities, EnvMappings, Build}` — full provider descriptor

Key functions:
- `AllProviders() []ProviderEntry` — complete registry
- `GetProviderEntry(id string) (ProviderEntry, bool)` — lookup by canonical ID
- `ProviderHasCapability(id, capability string) bool`
- `ProviderConfigFromEnv(entry ProviderEntry) ProviderConfig` — returns `nil` if any required env var is unset (OSS mode silent skip)

### `providers/stability_test.go`
5 tests, 23 sub-tests, all passing in CI:

| Test | What it catches |
|---|---|
| `TestProviderNameStability` | `Name()` drift away from constant |
| `TestAllProvidersRegistryCompleteness` | missing entry in `AllProviders()` |
| `TestProviderEntryIDMatchesNameConstant` | raw string in registry `ID` field |
| `TestProviderCapabilitiesNotEmpty` | provider with no capabilities |
| `TestProviderEnvMappingsHaveRequiredKey` | entry with no required env mapping |

### All 19 provider files
Compile-time assertions added before each constructor:

```go
var (
    _ Provider          = (*OpenAIProvider)(nil)
    _ StreamProvider    = (*OpenAIProvider)(nil)
    _ ProxiableProvider = (*OpenAIProvider)(nil)
)
```

### `cmd/ferrogw/main.go` — `registerProviders()`
Reduced from 130 to 45 lines.  
Now loops `providers.AllProviders()` + Bedrock special-case (dual-gate: `AWS_REGION` OR `AWS_ACCESS_KEY_ID`).  
**New providers require zero changes here.**

## 6) ProviderConfig — two-mode design

`ProviderConfig` is `map[string]string` with typed `CfgKey*` constants as keys.

### OSS mode (environment variables)

```go
entry, _ := providers.GetProviderEntry(providers.NameOpenAI)
cfg := providers.ProviderConfigFromEnv(entry)
if cfg == nil {
    return // OPENAI_API_KEY not set; skip silently
}
p, err := entry.Build(cfg)
```

### FerroCloud cloud mode (per-tenant credentials from DB)

```go
// Credentials retrieved from encrypted tenant secrets store
cfg := providers.ProviderConfig{
    providers.CfgKeyAPIKey: tenantSecret.OpenAIKey,
    providers.CfgKeyBaseURL: tenantOverride.OpenAIBaseURL, // optional
}
entry, _ := providers.GetProviderEntry(providers.NameOpenAI)
p, err := entry.Build(cfg)
```

No environment variable is read for cloud tenants.  
The same `Build` function handles both modes; `ProviderConfig` is the only input.

> **Note on per-tenant memory cost:**  
> Each tenant gets its own `providers.Registry` instance.  
> At scale this is multiplicative.  
> FerroCloud should use a lazy-init or LRU registry pool rather than pre-building all providers per tenant at request time.  
> This is a FerroCloud internal concern; the OSS factory API is intentionally neutral on this.

## 7) Remaining phases

### Phase 1 — `providers/core` subpackage (import root)

Create `providers/core/` as the dependency-root package for all contracts and shared types.

**Why:** Once per-provider subpackages exist (`providers/openai/`, `providers/bedrock/`, …), they must not import root `providers` (cycle). `providers/core` is imported by subpackages instead.

**Includes `ProviderSource`:**  
`base.go` currently defines `ProviderSource` constants (`SourceOpenAI`, `SourceAnthropic`, `SourceBedrock`, `SourceGoogle`, `SourceCohere`).  
These must move to `providers/core` — they are type-level metadata, not wiring logic.

```text
providers/
  core/
    contracts.go    # Provider/StreamProvider/EmbeddingProvider/ImageProvider/
                    # DiscoveryProvider/ProxiableProvider interfaces
    types.go        # Request/Response/Usage/Message/ImageContent/...
    source.go       # ProviderSource + SourceXxx constants (moved from base.go)
    errors.go       # typed provider error helpers
```

Root `providers` re-exports via type aliases:

```go
// providers/facade_aliases.go
package providers

import "github.com/ferro-labs/ai-gateway/providers/core"

type Provider         = core.Provider
type StreamProvider   = core.StreamProvider
type Request         = core.Request
type Response        = core.Response
type ProviderSource  = core.ProviderSource
```

### Phase 2 — Per-provider subpackage migration

Move concrete implementations under `providers/<id>/`.  
Root `providers` keeps thin constructor wrappers calling the subpackage.

**Migration order — complexity tiers (NOT newest-first):**

| Batch | Providers | Reason |
|---|---|---|
| **A — safest** | `xai`, `groq`, `together`, `perplexity`, `fireworks`, `deepseek`, `mistral` | Simple API-key-only, OpenAI-compatible, no special auth |
| **B — standard** | `openai`, `anthropic`, `cohere`, `ai21`, `azure_foundry`, `hugging_face` | Standard HTTP auth; some have multiple capability interfaces |
| **C — schema translation** | `gemini`, `azure_openai` | Custom request/response schemas; Gemini needs schema adapter |
| **D — SDK / auth chains** | `vertex_ai`, `bedrock`, `replicate`, `ollama` | GCP ADC / AWS credential chain; Bedrock AWS SDK; Ollama local |

> **Gemini note (Batch C):**  
> Gemini uses a different request/response schema than OpenAI format.  
> The subpackage migration must include a dedicated `schema_adapter.go` file and additional schema-mapping tests before this batch is considered complete.
>
> **Bedrock note (Batch D):**  
> Bedrock has two valid auth paths: explicit credentials (`AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`) and the AWS credential chain (`AWS_REGION` only + instance/role credentials).  
> The migration must preserve the dual-gate logic that already exists in `registerProviders()` and add explicit credential chain tests for both paths.
>
> **Vertex AI note (Batch D):**  
> Vertex AI requires `GOOGLE_APPLICATION_CREDENTIALS` or workload identity ADC.  
> The `CfgKeyServiceAccountJSON` path must be tested separately from ADC-only environments.

### Phase 3 — `discovery.go` relocation

`providers/discovery.go` contains `discoverOpenAICompatibleModels` which is:
- shared across multiple providers,
- currently an unexported function,
- not part of the `DiscoveryProvider` interface.

**Decision:** move to `providers/core/internal/discovery/` (unexported outside the module boundary). This keeps it accessible to all provider subpackages without making it part of the public API surface.

Providers that use it (`openai`, `groq`, `together`, `perplexity`, `fireworks`, `mistral`, `deepseek`, `azure_foundry`, `xai`) import `providers/core/internal/discovery` from their subpackages.

### Phase 4 — Deprecation window

- Mark old root constructors deprecated in comments only (no removal).
- After one or two releases, evaluate removal in the next major version.
- `ProviderSource` constants in root `providers` become aliases during this window.

## 8) Plugin system dependency

The plugin system (`plugin/` package) uses `providers.Provider` via the gateway core interfaces. Type aliases in `providers/facade_aliases.go` mean `providers.Provider` continues to equal `core.Provider` — no plugin API break.

Verified: `plugin.go` uses the `Provider` interface only for type assertions and `Registry` lookups; it does not instantiate providers directly. Subpackage migration is safe from the plugin layer's perspective.

## 9) Import-cycle prevention

### Problem
If root `providers` imports `providers/<id>`, and `providers/<id>` imports root `providers` for types, Go import cycles occur.

### Solution (Phase 1)
- `providers/core` becomes the dependency root for contracts and shared types.
- Provider subpackages import only `providers/core` (and standard lib / HTTP helpers).
- Root `providers` re-exports aliases and constructor wrappers.
- `names.go` and `factory.go` stay in root `providers` (no subpackage imports them; they are pure data).

Cycle-safe import graph:

```
cmd/ferrogw  →  providers  →  providers/core
                providers/<id>  →  providers/core
                providers/<id>  →  providers/core/internal/discovery
```

## 10) Phased rollout summary

| Phase | Description | Status |
|---|---|---|
| 0 | Guardrails: lint green, tests green, this doc | ✅ Complete |
| Factory | `names.go`, `factory.go`, `stability_test.go`, simplified `registerProviders()` | ✅ Complete (`af942ae`) |
| 1 | `providers/core` subpackage + type aliases | ✅ Complete (`d40851d`) |
| 2A | Subpackage: Batch A — `xai`, `groq`, `together`, `perplexity`, `fireworks`, `deepseek`, `mistral` | ✅ Complete (`9bf19e3`) |
| 2B | Subpackage: Batch B — `openai`, `anthropic`, `cohere`, `ai21`, `azure_foundry`, `hugging_face` | ✅ Complete (`1be5c28`) |
| 2C | Subpackage: Batch C — `gemini`, `azure_openai` (schema translation) | ✅ Complete (`a23eb4d`) |
| 2D | Subpackage: Batch D — `vertex_ai`, `bedrock`, `replicate`, `ollama` (SDK/auth chains) | ✅ Complete (`035aa60`) |
| 3 | `discovery.go` → `providers/internal/discovery/` | ✅ Complete (`035aa60`) |
| 4 | Deprecation window — `// Deprecated:` markers on all 19 root constructor shims | ✅ Complete |

## 11) Risk analysis

| Risk | Impact | Status | Mitigation |
|---|---|---|---|
| Import cycles during move | High | ⏳ | `providers/core` first; subpackages import only `core` |
| Constructor API break | High | ✅ Mitigated | Root wrapper constructors preserved |
| Provider name drift | High | ✅ Mitigated | `Name*` constants + `TestProviderNameStability` in CI |
| Name = database contract | High | ✅ Noted | Any rename = DB migration; treat as major version break |
| `any`-typed option loss | Medium | ✅ Resolved | `ProviderConfig map[string]string` + typed `CfgKey*` constants |
| FerroCloud credential injection | High | ✅ Resolved | Two-mode `ProviderConfig` design (§6) |
| Per-tenant Registry memory cost | Medium | ⚠️ Open | Use lazy-init / LRU pool in FerroCloud layer |
| Env parsing regressions | Medium | ✅ Mitigated | `EnvMapping.Required` gates + stability tests |
| Gemini schema adapter missing | Medium | ⏳ | Required before Batch C merge; schema adapter + tests |
| Bedrock dual-auth path | Medium | ⚠️ Open | Dual-gate preserved in `registerProviders()`; needs explicit credential-chain tests |
| Vertex AI ADC vs explicit JSON | Medium | ⚠️ Open | `CfgKeyServiceAccountJSON` path needs ADC-only env test |
| Discovery endpoint behavior drift | Medium | ⏳ | Preserve current discovery helper semantics and add contract tests |
| Plugin system break | Low | ✅ Verified | Plugin uses `providers.Provider` interface only; type aliases are transparent |

## 12) Testing strategy

For each migrated provider subpackage:

- constructor validation tests,
- auth header tests,
- request/response mapping tests,
- stream parsing tests (if stream supported),
- compile-time interface assertions (`var _ Interface = (*Provider)(nil)`),
- `Name()` and `SupportsModel()` stability tests.

Cross-cutting (already in place):

- `TestProviderNameStability` — 19 sub-tests
- `TestAllProvidersRegistryCompleteness`
- `TestProviderEntryIDMatchesNameConstant`
- `TestProviderCapabilitiesNotEmpty`
- `TestProviderEnvMappingsHaveRequiredKey`

FerroCloud integration (acceptance gate):

- Spin up registry with explicit `ProviderConfig` map (no env vars) for at least one provider.
- Confirm router selects provider by canonical name from DB config.

## 13) Release strategy

- **Factory phase release:** minor version (full compatibility preserved).
- **Subpackage migration releases:** minor versions (aliases keep external API stable).
- **Major version only if:** constructor wrappers or type aliases are removed.

Changelog requirements per release:

- State "internal architecture modernization — no API break".
- Reference `CHANGELOG.md` section "Provider Architecture".
- Provide contributor migration note: new provider convention = `providers/<id>/impl.go`.

## 14) PR slicing

| PR | Contents | Status |
|---|---|---|
| PR-0 | `names.go`, `factory.go`, `stability_test.go`, updated `registerProviders()`, all provider name constants + compile-time assertions | ✅ Merged (`af942ae`) |
| PR-1 | `providers/core/` subpackage + type aliases in root + `ProviderSource` move | ✅ Merged (`d40851d`) |
| PR-2 | Batch A subpackages: `xai`, `groq`, `together`, `perplexity`, `fireworks`, `deepseek`, `mistral` | ✅ Merged (`9bf19e3`) |
| PR-3 | Batch B subpackages: `openai`, `anthropic`, `cohere`, `ai21`, `azure_foundry`, `hugging_face` | ✅ Merged (`1be5c28`) |
| PR-4 | Batch C subpackages: `gemini` (schema adapter), `azure_openai` | ✅ Merged (`a23eb4d`) |
| PR-5 | Batch D subpackages: `vertex_ai`, `bedrock` (dual-auth tests), `replicate`, `ollama` + `discovery` relocation | ✅ Merged (`035aa60`) |
| PR-6 | Deprecation comments on all 19 root constructor shims | ✅ Merged |

## 15) Adding a new provider (post-refactor guide)

After Phase 2 is complete, adding a new provider is a 5-step process with **zero changes to `main.go`**:

1. Create `providers/<id>/impl.go` importing `providers/core`.
2. Add a `ProviderEntry` to `providers/factory.go`'s `allProviders` slice.
3. Add a `Name<ID>` constant to `providers/names.go`.
4. Add a root constructor wrapper in `providers/` (optional, for backwards compat).
5. Run `go test ./providers/...` — stability tests will catch missing entries.

## 16) Acceptance checklist

- [x] `make lint` passes — 0 issues
- [x] `go test ./...` passes — all tests green
- [x] All 19 providers: `Name()` returns canonical constant (tested in `stability_test.go`)
- [x] `AllProviders()` count matches `AllProviderNames()` count
- [x] `registerProviders()` is data-driven — no per-provider `if os.Getenv(...)` blocks
- [x] Compile-time interface assertions in all 19 provider files
- [x] `ProviderConfig` two-mode design documented and tested
- [x] `providers/core` subpackage created with type aliases in root
- [x] `ProviderSource` defined as interface in `providers/core/contracts.go`; old enum constants removed
- [x] All Batch A providers migrated to subpackages
- [x] All Batch B providers migrated to subpackages
- [x] Gemini schema translation implemented in `providers/gemini/impl.go` (gemini-specific request/response structs + mock HTTP tests)
- [x] Bedrock dual-auth credential chain tests pass (`TestNewBedrock_DefaultRegion`, `TestNewBedrockWithOptions_StaticCredentials`)
- [x] Vertex AI auth paths tested: API key mode, service account JSON validation, mock HTTP complete + stream
- [x] `discoverOpenAICompatibleModels` moved to `providers/internal/discovery/openai_compat.go`
- [x] Contributor docs updated with new provider folder pattern (`CONTRIBUTING.md`)
- [ ] FerroCloud integration test: explicit `ProviderConfig` map (no env vars) routes correctly ⏳

---

## Appendix A — Canonical provider names

Provider names are an immutable data contract. Changing them requires a database migration.

| Constant | String value | Source group |
|---|---|---|
| `NameOpenAI` | `openai` | `SourceOpenAI` |
| `NameAnthropic` | `anthropic` | `SourceAnthropic` |
| `NameAzureOpenAI` | `azure-openai` | `SourceOpenAI` |
| `NameAzureFoundry` | `azure-foundry` | `SourceOpenAI` |
| `NameBedrock` | `bedrock` | `SourceBedrock` |
| `NameVertexAI` | `vertex-ai` | `SourceGoogle` |
| `NameGemini` | `gemini` | `SourceGoogle` |
| `NameCohere` | `cohere` | `SourceCohere` |
| `NameAI21` | `ai21` | — |
| `NameGroq` | `groq` | `SourceOpenAI` |
| `NameTogether` | `together` | `SourceOpenAI` |
| `NamePerplexity` | `perplexity` | `SourceOpenAI` |
| `NameFireworks` | `fireworks` | `SourceOpenAI` |
| `NameDeepSeek` | `deepseek` | `SourceOpenAI` |
| `NameMistral` | `mistral` | `SourceOpenAI` |
| `NameXAI` | `xai` | `SourceOpenAI` |
| `NameHuggingFace` | `hugging-face` | — |
| `NameReplicate` | `replicate` | — |
| `NameOllama` | `ollama` | — |

## Appendix B — `ProviderConfig` key reference

| Constant | Key string | Used by |
|---|---|---|
| `CfgKeyAPIKey` | `api_key` | Most providers |
| `CfgKeyBaseURL` | `base_url` | OpenAI, xAI, Groq, … |
| `CfgKeyAPIVersion` | `api_version` | Azure OpenAI |
| `CfgKeyDeployment` | `deployment` | Azure OpenAI, Azure Foundry |
| `CfgKeyProjectID` | `project_id` | Vertex AI |
| `CfgKeyRegion` | `region` | Bedrock, Vertex AI |
| `CfgKeyServiceAccountJSON` | `service_account_json` | Vertex AI |
| `CfgKeyAccessKeyID` | `access_key_id` | Bedrock |
| `CfgKeySecretAccessKey` | `secret_access_key` | Bedrock |
| `CfgKeySessionToken` | `session_token` | Bedrock |
| `CfgKeyHost` | `host` | Ollama |
| `CfgKeyModels` | `models` | Ollama |
| `CfgKeyAPIToken` | `api_token` | Replicate, Hugging Face |
| `CfgKeyTextModels` | `text_models` | AI21 |
| `CfgKeyImageModels` | `image_models` | Replicate |
