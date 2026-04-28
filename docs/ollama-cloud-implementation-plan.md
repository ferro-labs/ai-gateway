# Ollama Cloud Provider Implementation Plan

This document is the single source of truth for implementing GitHub issue #94:
add Ollama Cloud as a first-class provider while preserving the gateway's
OpenAI-compatible public API.

## Goal

Add a clean `ollama-cloud` provider that lets users call the existing gateway
OpenAI-compatible endpoint (`/v1/chat/completions`) with normal chat completion
payloads while the provider internally talks to Ollama Cloud's documented API.

The existing local `ollama` provider must continue to represent local Ollama
instances and local cloud-offload models. Ollama Cloud direct API access should
be a separate provider because it has a different base URL, authentication
requirements, model IDs, and documented endpoint shape.

## Design Decisions

### Provider identity

- Add canonical provider ID: `ollama-cloud`
- Add package path: `providers/ollama_cloud`
- Add public root constant: `providers.NameOllamaCloud`
- Do not change the existing `providers.NameOllama` value or local Ollama
  behavior.

### End-user compatibility

Users should not need to learn Ollama's native API to use this provider through
the gateway. They continue using:

```http
POST /v1/chat/completions
```

with OpenAI-compatible request fields such as `model`, `messages`,
`temperature`, `max_tokens`, and `stream`.

The provider adapts gateway `core.Request` values to Ollama Cloud's native
`/api/chat` request and adapts Ollama Cloud responses back to `core.Response`
and `core.StreamChunk`.

### Upstream API target

Use Ollama Cloud's documented direct API:

- Default host: `https://ollama.com`
- Chat: `POST /api/chat`
- Models: `GET /api/tags`
- Authentication: `Authorization: Bearer <OLLAMA_API_KEY>`

Do not depend on undocumented `https://ollama.com/v1/*` routes for the initial
implementation. If those routes are later verified and documented, proxy support
can be added separately.

### Proxy support

Do not implement `core.ProxiableProvider` for `ollama-cloud` initially.

Reason: the gateway proxy forwards unhandled `/v1/*` paths to provider
`BaseURL()`. Ollama Cloud's documented direct API is `/api/*`, so advertising
OpenAI pass-through could create confusing 404s or silently depend on
undocumented behavior.

### Model routing

The provider should support explicit configured models and live discovery:

- `OLLAMA_CLOUD_MODELS` can provide a comma-separated static list for routing.
- `DiscoverModels(ctx)` should fetch `GET /api/tags` and map the returned model
  names to `core.ModelInfo`.
- `SupportsModel(model)` should return true for configured/discovered models
  when a list is available, and may use a conservative fallback only if needed
  for compatibility with provider routing.

Avoid making `ollama-cloud` support every arbitrary model name if doing so would
steal traffic from other providers during fallback routing.

## Catalog Plan

Current catalog state:

- `models/catalog.json` and `models/catalog_backup.json` include 29 `ollama/*`
  entries.
- Four of those entries are local cloud-offload style models:
  - `ollama/gpt-oss:120b-cloud`
  - `ollama/gpt-oss:20b-cloud`
  - `ollama/qwen3-coder:480b-cloud`
  - `ollama/deepseek-v3.1:671b-cloud`
- There are no `ollama-cloud/*` entries today.

Add separate `ollama-cloud/*` entries for direct Ollama Cloud model IDs. Initial
entries should include the model IDs confirmed by Ollama Cloud's public
`/api/tags` endpoint and docs, for example:

- `ollama-cloud/gpt-oss:120b`
- `ollama-cloud/gpt-oss:20b`
- `ollama-cloud/qwen3-coder:480b`
- `ollama-cloud/deepseek-v3.1:671b`

Keep existing `ollama/*-cloud` entries because they represent local Ollama
cloud-offload mode, where the local Ollama host handles auth/session behavior.

Pricing caution:

- Ollama Cloud public pricing is currently plan/GPU-usage oriented, not fixed
  token pricing.
- The implementation uses `null` token pricing for `ollama-cloud/*` entries so
  cost reporting does not imply fixed per-token billing where Ollama has not
  published it.
- If Ollama publishes fixed per-token pricing later, update the catalog entries
  then.

Both `models/catalog.json` and `models/catalog_backup.json` must be updated
together to keep remote and embedded fallback behavior consistent.

## Implementation Steps

1. Create `providers/ollama_cloud/ollama_cloud.go`.
2. Implement constructor:
   - `New(apiKey, baseURL string, models []string) (*Provider, error)`
   - require a non-empty API key
   - default empty base URL to `https://ollama.com`
   - validate `http`/`https` base URLs with a host
   - trim trailing slashes
3. Implement provider methods:
   - `Name() string`
   - `SupportedModels() []string`
   - `SupportsModel(model string) bool`
   - `Models() []core.ModelInfo`
   - `Complete(ctx, req core.Request) (*core.Response, error)`
   - `CompleteStream(ctx, req core.Request) (<-chan core.StreamChunk, error)`
   - `DiscoverModels(ctx) ([]core.ModelInfo, error)`
4. Add compile-time assertions:
   - `core.Provider`
   - `core.StreamProvider`
   - `core.DiscoveryProvider`
5. Add native request mapping for `/api/chat`:
   - `model`
   - `messages`
   - `stream`
   - `temperature`
   - `top_p`
   - `max_tokens`, mapped to Ollama `options.num_predict` if needed by the
     native API
   - `tools` if supported by Ollama's request schema
6. Add native response mapping:
   - Ollama `message.role/content/tool_calls` to `core.Message`
   - `done_reason` to `finish_reason`
   - `prompt_eval_count` to `Usage.PromptTokens`
   - `eval_count` to `Usage.CompletionTokens`
   - sum to `Usage.TotalTokens`
   - set `Provider` to `ollama-cloud`
7. Add stream parsing:
   - parse Ollama native newline-delimited JSON chunks from `/api/chat`
   - emit gateway `core.StreamChunk` values
   - include final usage when Ollama sends final counts
   - surface scanner/read errors through `StreamChunk{Error: err}`
8. Add model discovery:
   - call `GET {baseURL}/api/tags`
   - include Bearer auth
   - parse `models[].name` or `models[].model`
   - convert `modified_at` to Unix `Created` when possible
   - set `OwnedBy` to `ollama-cloud`
9. Register provider:
   - import package in `providers/names.go`
   - add `NameOllamaCloud`
   - add it to `AllProviderNames()` in alphabetical order
   - add `ProviderEntry` in `providers/providers_list.go`
10. Add env mappings:
    - `OLLAMA_API_KEY` required
    - `OLLAMA_CLOUD_BASE_URL` optional
    - `OLLAMA_CLOUD_MODELS` optional
11. Update configuration examples:
    - add `virtual_key: ollama-cloud` in `config.example.yaml`
    - add `{ "virtual_key": "ollama-cloud" }` in `config.example.json`
    - add commented env vars in `docker-compose.yml`
12. Update model catalogs:
    - add `ollama-cloud/*` entries to `models/catalog.json`
    - add matching entries to `models/catalog_backup.json`
13. Update tests:
    - new `providers/ollama_cloud/ollama_cloud_test.go`
    - update `providers/stability_test.go`
    - update any provider-count expectations or documentation strings if they
      are asserted in tests
14. Run validation:
    - `go test ./providers/...`
    - `go test ./internal/transport/...` if adding a transport preset
    - `go test ./...` before finalizing

## Test Coverage Details

Provider tests should cover:

- constructor rejects empty API key
- constructor defaults base URL to `https://ollama.com`
- constructor rejects invalid base URLs
- auth header is sent on chat and discovery requests
- non-streaming `/api/chat` response maps message, finish reason, provider, and
  token usage correctly
- non-200 responses surface useful error messages
- streaming parses newline-delimited JSON chunks and final usage
- discovery parses `/api/tags` model names and timestamps
- `SupportsModel` does not unintentionally claim unrelated provider model IDs

Registry/stability tests should cover:

- `NameOllamaCloud` is stable
- `AllProviderNames()` includes `ollama-cloud`
- `AllProviders()` includes exactly one `ollama-cloud` entry
- provider capabilities include `chat`, `stream`, and `discovery`
- env mappings have a required configured gate

## Open Questions

- Whether direct Ollama Cloud supports `/v1/chat/completions` with Bearer auth.
  If verified and documented, a later change can add `core.ProxiableProvider`
  or simplify the provider implementation.
- Whether Ollama will publish fixed per-token pricing. Until then, cost
  reporting for `ollama-cloud` should avoid pretending usage-based plan limits
  are token prices.
- How aggressive `SupportsModel` should be before first discovery has run. The
  safest default is to use configured/static models and rely on discovery for
  the broader model list.

## Definition of Done

- Users can configure Ollama Cloud with `OLLAMA_API_KEY`.
- Users can call the gateway's normal `/v1/chat/completions` endpoint for
  `ollama-cloud` models without using Ollama-specific request shapes.
- Streaming works through the gateway's normal SSE response path.
- `/v1/models` includes `ollama-cloud` models from static config and/or
  discovery, enriched from catalog where entries exist.
- Local `ollama` behavior remains unchanged.
- Catalog fallback remains consistent by updating both catalog files.
- Provider registry stability tests pass.
- Full Go test suite passes.
