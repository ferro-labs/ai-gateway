# Changelog

All notable changes to Ferro Labs AI Gateway will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.7.0] — 2026-03-07

This release is a **major structural refactor** of the provider layer. All 19 provider implementations are extracted into independent subpackages, a unified two-mode factory replaces ad-hoc constructors, and five new provider adapters are added. The public `providers.NewXxx()` root constructors have been removed.

### Added

- **5 new provider adapters** — xAI (`providers/xai/`), Azure Foundry (`providers/azure_foundry/`), Hugging Face (`providers/hugging_face/`), Vertex AI (`providers/vertex_ai/`), and AWS Bedrock static-credential support (`providers/bedrock/`):
  - xAI: `xai` provider, default base URL `https://api.x.ai/v1`, chat + streaming, Grok-aware model support, auto-registration via `XAI_API_KEY`
  - Azure Foundry: `azure-foundry` provider, `api-key` auth, chat + streaming, auto-registration via `AZURE_FOUNDRY_API_KEY` + `AZURE_FOUNDRY_ENDPOINT`
  - Hugging Face: `hugging-face` provider, default `https://api-inference.huggingface.co/v1`, chat + streaming + embedding + image + discovery interfaces, auto-registration via `HUGGING_FACE_API_KEY`
  - Vertex AI: `vertex-ai` provider, API-key mode (`x-goog-api-key`) and service-account JSON OAuth mode, chat + streaming, auto-registration via `VERTEX_AI_PROJECT_ID`
  - Bedrock static credentials: `BedrockOptions` with `AccessKeyID`, `SecretAccessKey`, `SessionToken` fields alongside default credential-chain support
- **`providers/core` subpackage** (`providers/core/`): canonical home for all shared interfaces (`Provider`, `StreamProvider`, `EmbeddingProvider`, `ImageProvider`, `DiscoveryProvider`, `ProxiableProvider`), request/response types, and error helpers — all exported as type aliases from the root `providers` package for backwards compatibility
- **Canonical `Name*` constants** (`providers/names.go`): `NameOpenAI`, `NameAnthropic`, `NameBedrock`, etc. — one authoritative string per provider used across gateway routing configs, credential stores, and the factory registry
- **Two-mode `ProviderConfig` factory** (`providers/factory.go`):
  - `ProviderConfig` (`map[string]string`) — single input type for all provider `Build` functions; typed `CfgKey*` constants for all fields
  - `ProviderEntry` — self-describing record per provider: `ID`, `Capabilities`, `EnvMappings`, `Build`
  - `AllProviders()` — ordered slice of all 19 entries; `GetProviderEntry(id)` for lookup
  - `ProviderConfigFromEnv(entry)` — reads env vars declared in `EnvMappings` and returns a populated `ProviderConfig`, or nil when the provider is unconfigured (no error)
  - `AllProviderNames()` — returns all canonical name constants; used by stability tests
- **Shared OpenAI-compatible discovery helper** (`providers/internal/discovery/openai_compat.go`): `DiscoverOpenAICompatibleModels` shared by xAI, Fireworks, Perplexity, and Hugging Face to enumerate models via `GET /models`
- **Per-provider subpackages** — all 19 providers extracted into `providers/<id>/<id>.go`, each with:
  - A `Name` constant matching its `Name*` registry constant
  - A standalone `New(...)` constructor
  - Compile-time interface assertions (`var _ core.Provider = (*Provider)(nil)`, etc.)
  - Tests co-located at `providers/<id>/<id>_test.go`
- **`CONTRIBUTING.md` — Adding a New Provider guide**: updated to describe the 5-step subpackage convention (`impl`, `Name*` constant, `ProviderEntry`, test, stability check)

### Changed

- **`registerProviders()` in `cmd/ferrogw/main.go`**: rewritten to iterate `providers.AllProviders()` and call `entry.Build(cfg)` uniformly — no per-provider special-casing. Bedrock remains a separate branch for its dual-key detection (`AWS_REGION` / `AWS_ACCESS_KEY_ID`)
- **All examples** (`examples/*/main.go`): updated to import provider subpackages directly (`openaipkg`, `anthropicpkg`, etc.) instead of the removed root constructors
- **`providers/factory.go`**: all `Build` functions now call subpackage `New` directly instead of the removed shim functions

### Removed

- **All 19 deprecated root shim files** (`providers/ai21.go`, `providers/anthropic.go`, `providers/azure_foundry.go`, `providers/azure_openai.go`, `providers/bedrock.go`, `providers/cohere.go`, `providers/deepseek.go`, `providers/fireworks.go`, `providers/gemini.go`, `providers/groq.go`, `providers/hugging_face.go`, `providers/mistral.go`, `providers/ollama.go`, `providers/openai.go`, `providers/perplexity.go`, `providers/replicate.go`, `providers/together.go`, `providers/vertex_ai.go`, `providers/xai.go`): the `providers.NewXxx()` root constructors and `XxxProvider` type aliases are removed. Import `providers/<id>` and call `New(...)` directly, or use `providers.GetProviderEntry(id).Build(cfg)` for factory-based construction.
- **`providers/<id>/impl.go` naming**: all implementation files renamed to `providers/<id>/<id>.go` for clarity

## [0.6.1] — 2026-03-06

### Changed

- **GitHub Actions workflow dependencies** (`.github/workflows/`): bumped core CI/release/security actions to current major versions, including `actions/checkout` (v4→v6), `actions/setup-go` (v5→v6), `github/codeql-action` (v3→v4), `goreleaser/goreleaser-action` (v6→v7), `actions/cache` (v4→v5), `actions/github-script` (v7→v8), `docker/login-action` (v3→v4), `docker/setup-buildx-action` (v3→v4), and `docker/setup-qemu-action` (v3→v4)
- **Go dependency refresh** (`go.mod`, `go.sum`): updated runtime dependencies including `github.com/lib/pq` (`v1.10.9`→`v1.11.2`), `modernc.org/sqlite` (`v1.40.1`→`v1.46.1`), and AWS SDK modules used by Bedrock integration

## [0.6.0] — 2026-03-06

### Added

- **`pii-redact` guardrail plugin** (`internal/plugins/pii/`): detects PII entities (email, phone, credit card with Luhn validation, SSN, IP, AWS access key, IBAN, passport) with configurable actions (`block|redact|warn|log`), per-entity overrides, input/output scope, and redaction modes (`mask`, `replace_type`, `hash`, `synthetic`)
- **`secret-scan` guardrail plugin** (`internal/plugins/secretscan/`): detects hardcoded credentials and DSNs across cloud/API/source-control/payment patterns with optional Shannon entropy filtering for generic high-entropy tokens
- **`prompt-shield` guardrail plugin** (`internal/plugins/promptshield/`): scores prompt-injection signals using max-confidence matching with threshold-based enforcement and optional custom tenant signals
- **`schema-guard` guardrail plugin** (`internal/plugins/schemaguard/`): validates JSON content against tenant-provided JSON Schema (draft-07 via `github.com/santhosh-tekuri/jsonschema/v5`), including optional markdown code-fence JSON extraction
- **`regex-guard` guardrail plugin** (`internal/plugins/regexguard/`): ordered, per-rule regex guardrail supporting stage targeting (`input|output|both`) and per-rule actions (`block|warn|log`)
- **Shared guardrail helpers** (`internal/plugins/guardrailutil/`): common parsing and message extraction helpers used by guardrail plugins to keep config handling consistent

### Changed

- **Plugin rejection handling** (`plugin/errors.go`, `plugin/manager.go`): introduced typed `RejectionError` for intentional guardrail rejections and extended after-request lifecycle to propagate rejections (needed for output guardrails such as `schema-guard`)
- **Gateway request mutation propagation** (`gateway.go`): `Route()` now applies before-request plugin request mutations before strategy execution (enables in-place redaction plugins)
- **HTTP error mapping for guardrail rejections** (`cmd/ferrogw/main.go`): chat-completions routes now return `400 invalid_request_error` for plugin rejection errors instead of generic `500 routing_error`
- **Plugin registration** (`cmd/ferrogw/main.go`, `cmd/ferrogw-cli/main.go`): wired all new guardrail plugins into server and CLI plugin listing
- **Config examples** (`config.example.yaml`, `config.example.json`): added sample blocks for all new guardrail plugins

## [0.5.0] — 2026-03-03

### Added

- **Streaming cost tracking** (`internal/streamwrap/wrap.go`): `Meter()` wraps any `<-chan StreamChunk` in a transparent goroutine that accumulates token usage from the final chunk and emits `gateway_requests_total`, `gateway_request_duration_seconds`, `gateway_tokens_input_total`, `gateway_tokens_output_total`, and `gateway_request_cost_usd_total` Prometheus metrics plus `request.completed` event hooks on stream close; `RouteStream()` in `gateway.go` now fully mirrors `Route()` metrics coverage
- **OpenAI streaming usage** (`providers/openai.go`): `CompleteStream()` now sets `stream_options.include_usage: true` so the final SSE chunk carries token counts; `StreamChunk` gained a `Usage` field populated from the final chunk's usage data (including `reasoning_tokens` and `cached_tokens`)
- **`providers.ParseStatusCode(err)`** (`providers/provider.go`): regex-based helper extracting the HTTP status code from provider error messages formatted as `"... (NNN): ..."` — used by retry and fallback logic across all 15 providers without requiring per-provider changes
- **Per-target retry status-code filtering** (`internal/strategies/fallback.go`): `Fallback.WithTargetRetry()` now accepts an `onStatusCodes []int` slice; if non-empty, retries are only attempted when the error's status code is in the list — e.g. retry on 429/503 but fail-fast on 400/401; `shouldRetry()` helper extracts codes via `ParseStatusCode`
- **`RetryConfig` extensions** (`config.go`): new `on_status_codes` (array of ints) and `initial_backoff_ms` (int, default 100) fields on per-target retry config
- **Least-latency routing strategy** (`internal/strategies/leastlatency.go`, `internal/latency/tracker.go`): `LeastLatency` strategy selects the compatible provider with the lowest P50 latency from a thread-safe in-process sliding window (default 100 samples per provider); falls back to random selection when a provider has no recorded samples; `Route()` records every successful call's latency into a shared `*latency.Tracker` on the `Gateway` struct
- **Cost-optimized routing strategy** (`internal/strategies/costoptimized.go`): `CostOptimized` strategy estimates prompt token count (~4 chars/token heuristic on request messages), calls `models.Calculate()` for each compatible provider, and routes to the cheapest option; falls back to the first compatible provider when no catalog pricing is available
- **`ModeLatency` / `ModeCostOptimized` strategy modes** (`config.go`): two new `StrategyMode` constants (`"least-latency"`, `"cost-optimized"`) wired into `gateway.go`'s `getStrategy()` switch
- **CLI UX overhaul** (`cmd/ferrogw-cli/`): replaced hand-rolled `switch os.Args[1]` with [Cobra](https://github.com/spf13/cobra); added persistent `--gateway-url`, `--api-key`, and `--format table|json|yaml` flags; ported `validate`, `plugins`, `version` commands to `cobra.RunE`; added full `admin` command group (`admin keys list/get/create/delete/rotate`, `admin config get/history/update/rollback`, `admin logs list/stats`, `admin providers list/health`) in `admin.go`; thin admin HTTP client in `client.go`; table/JSON/YAML output formatter in `output.go`

### Changed

- **`gateway.go` `RouteStream()`**: emits error metrics on provider failure (previously silent); wraps the raw provider channel with `streamwrap.Meter()` for full metrics/event parity with `Route()`
- **`gateway.go` `getStrategy()`**: `ModeFallback` now wires per-target `RetryConfig` (including `OnStatusCodes` and `InitialBackoffMs`) via `fb.WithTargetRetry()`; added `ModeLatency` and `ModeCostOptimized` cases
- **`gateway.go` `Route()`**: records per-provider response latency into `g.latencyTracker` on every successful call

---

## [0.4.5] — 2026-02-28

### Added

- **Model catalog** (`models/` package): `Catalog`, `Model`, `Pricing`, `Capabilities`, `Lifecycle` types; embedded 2531-entry `catalog_backup.json` (sourced from LiteLLM's pricing file); `Load()` fetches fresh copy from remote URL and falls back to the embedded file
- **Cost calculator** (`models.Calculate()`): dispatches by model mode (chat, embedding, image, audio); returns itemised `CostResult` with 9 buckets — `InputUSD`, `OutputUSD`, `CacheReadUSD`, `CacheWriteUSD`, `ReasoningUSD`, `ImageUSD`, `AudioUSD`, `EmbeddingUSD`, `TotalUSD`
- **Provider token extensions**: OpenAI response parser extracts `reasoning_tokens` (from `completion_tokens_details`) and `cached_tokens` (from `prompt_tokens_details`); Anthropic parser extracts `cache_creation_input_tokens` and `cache_read_input_tokens`
- **`/v1/models` enrichment** (`cmd/ferrogw/models_handler.go`): each model in the list response is enriched with `context_window`, `max_output_tokens`, `capabilities` (array), `status`, and `deprecated` flag sourced from the catalog
- **Catalog CI check** (`.github/workflows/catalog-check.yml`): weekly scheduled job runs `go run ./scripts/catalog-check` to HEAD-check all remote pricing source URLs; opens a deduplicated GitHub issue on failure
- **Catalog source URL checker** (`scripts/catalog-check/main.go`): concurrent HTTP checker; exits with code 1 and lists all failed URLs

### Changed

- **Gateway cost calculation** (`gateway.go`): `Route()` now calls `models.Calculate()` instead of the removed `providers.EstimateCost()`; `publishEvent` emits 9 cost fields (`cost_usd`, `cost_input_usd`, `cost_output_usd`, `cost_cache_read_usd`, `cost_cache_write_usd`, `cost_reasoning_usd`, `cost_image_usd`, `cost_audio_usd`, `cost_embedding_usd`) plus `cost_model_found`; streaming responses via `RouteStream()` do not yet emit cost metrics (no final usage data available mid-stream)
- **`Gateway.New()`**: loads model catalog at startup (non-fatal on failure; falls back to embedded JSON)

### Removed

- **`providers/pricing.go`**: hardcoded 50-model pricing table and `EstimateCost()` helper removed; `models.Calculate()` with the full catalog is the single source of truth

---

## [0.4.0] — 2026-02-28

### Added

- **Config persistence backends**: `internal/admin/config_store.go` introduces SQL-backed runtime config persistence for both SQLite (`CONFIG_STORE_BACKEND=sqlite`) and PostgreSQL (`CONFIG_STORE_BACKEND=postgres`) with `CONFIG_STORE_DSN`
- **Runtime config manager**: `GatewayConfigManager` now bridges admin config API operations (`GetConfig`, `ReloadConfig`, `ResetConfig`) with optional persistent storage, including loading persisted config at startup
- **Config store wiring**: `cmd/ferrogw/main.go` now initializes config manager via `createConfigManagerFromEnv()` and logs selected `config_store` backend at startup
- **Config CRUD completion**: Admin routes now include `POST /admin/config`, `PUT /admin/config`, and `DELETE /admin/config` for create/update/reset semantics
- **API key detail endpoint**: Added `GET /admin/keys/{id}` (masked key response) for full API-key CRUD coverage
- **Request log SQL tests**: Added SQLite + optional PostgreSQL contract tests under `internal/requestlog/store_test.go`
- **Config persistence tests**: Added env/backend wiring + SQLite persistence tests in `cmd/ferrogw/main_test.go`
- **Admin API coverage**: Added tests for key-detail endpoint and config create/delete flows in `internal/admin/handlers_test.go`

### Changed

- **Admin router** (`internal/admin/handlers.go`): read/write route set expanded to include key-detail and full config CRUD endpoints while preserving RBAC scopes
- **README**: documented `GET /admin/keys/{id}`, config CRUD endpoints, and `CONFIG_STORE_BACKEND` / `CONFIG_STORE_DSN` env vars
- **Roadmap alignment**: v0.4.0 persistent storage/management milestones updated to released status

## [0.3.0] — 2026-02-28

### Added

- **Embeddings API**: `POST /v1/embeddings` endpoint; `EmbeddingProvider` interface + `EmbeddingRequest`/`EmbeddingResponse` types; implemented by OpenAI
- **Image Generation API**: `POST /v1/images/generations` endpoint; `ImageProvider` interface + `ImageRequest`/`ImageResponse` types; implemented by OpenAI and Replicate
- **5 new providers**:
  - **Perplexity** (`PERPLEXITY_API_KEY`): sonar, sonar-pro, sonar-reasoning and sonar-reasoning-pro models with streaming
  - **Fireworks AI** (`FIREWORKS_API_KEY`): Llama, Mixtral, Qwen, DeepSeek and Firefunction models with streaming
  - **AI21 Labs** (`AI21_API_KEY`): Jamba models via OpenAI-compatible endpoint + Jurassic models via native `/complete` endpoint
  - **Replicate** (`REPLICATE_API_TOKEN`): async-polling prediction model for text and image generation; configurable via `REPLICATE_TEXT_MODELS` / `REPLICATE_IMAGE_MODELS`
  - **AWS Bedrock** (`AWS_REGION` + standard AWS credential chain): Claude, Amazon Titan, and Meta Llama model families via `bedrock-runtime`
- **Model aliasing**: `aliases` top-level config key maps friendly names (e.g. `fast`, `smart`) to real model IDs; resolved at routing time before plugins run; cycle/self-reference validation in `ValidateConfig()`
- **Provider auto-discovery** (`DiscoveryProvider` interface): Perplexity and Fireworks providers implement `DiscoverModels(ctx)`; `Gateway.StartDiscovery(ctx, interval)` polls live model lists and updates `GET /v1/models` responses
- **Cost tracking**: `providers.EstimateCost()` uses a built-in pricing table (50+ models) to estimate request cost in USD; `gateway_request_cost_usd_total` Prometheus counter emitted after every successful `Route()` call
- **Streaming tests**: Mock-SSE streaming tests added for Perplexity, Fireworks, AI21, Replicate, and all new providers; OpenAI `StreamProvider` interface compliance test added
- **Proxy handler tests**: `cmd/ferrogw/proxy_test.go` covers provider resolution (X-Provider header + model body), auth header injection, gateway header removal, `X-Gateway-Provider` response header, non-200 passthroughs, and no-provider 400 error

### Changed

- `config.go`: Added `Aliases map[string]string` field to `Config`
- `gateway.go`: `Route()` and `RouteStream()` resolve aliases before strategy execution; `AllModels()` prefers live-discovered models; added `Embed()`, `GenerateImage()`, `StartDiscovery()` methods
- `cmd/ferrogw/main.go`: Registered Perplexity, Fireworks, AI21, Replicate, and AWS Bedrock providers; added `POST /v1/embeddings` and `POST /v1/images/generations` routes

### Internal

- `providers/pricing.go`: New pricing table + `EstimateCost()` helper
- `providers/discovery.go`: Shared `discoverOpenAICompatibleModels()` helper for providers with `/v1/models` endpoints
- `internal/metrics/metrics.go`: Added `gateway_request_cost_usd_total` counter
- `go.mod`: Added `github.com/aws/aws-sdk-go-v2` modules for Bedrock

## [0.2.0] — 2026-02-27

### Added

- **Structured logging**: `internal/logging` package — slog-based JSON logs with trace ID propagation via `X-Request-ID` header; `FromContext(ctx)` returns a logger pre-annotated with the request trace ID
- **Prometheus metrics**: `internal/metrics` package + `/metrics` scrape endpoint; tracks `request_total`, `request_duration_seconds`, `tokens_input_total`, `tokens_output_total`, `provider_errors_total`, `circuit_breaker_state`, `rate_limit_rejections_total`
- **Circuit breaker**: `internal/circuitbreaker` package — per-provider three-state machine (Closed → Open → HalfOpen) with configurable failure threshold, success threshold, and open timeout; configured per-target in YAML/JSON via `circuit_breaker` key
- **Rate limiting**: `internal/ratelimit` token-bucket implementation + per-IP HTTP middleware (enabled via `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` env vars) + new `rate-limit` plugin for per-provider limiting
- **Deep health check**: `GET /health` now returns `{"status":"ok","providers":[{"name":"...","status":"available","models":N},...]}` instead of a plain string
- **Consistent error schema**: All endpoints (admin, completions, proxy) return `{"error":{"message":"...","type":"...","code":"..."}}` — matches the OpenAI error format exactly
- **`BaseProvider` struct**: Embeddable `providers.Base` struct eliminates ~400 LOC of duplication across all 10 provider implementations; `ModelsFromList()` helper replaces per-provider boilerplate loops
- **`ProviderSource` interface**: `providers.ProviderSource` read-only interface implemented by both `*providers.Registry` and `*Gateway`, enabling registry consolidation without breaking existing startup code
- **Event hooks**: `EventHookFunc` type + `Gateway.AddHook()` replace the previous `EventPublisher` interface; hooks are dispatched asynchronously via goroutines

### Changed

- **Unified logger**: Removed all `import "log"` stdlib usage from `cmd/ferrogw/main.go`; all fatal startup errors now go through `logging.Logger.Error` + `os.Exit(1)`, producing consistent JSON output
- **Plugin manager** (`plugin/manager.go`): Replaced bare `slog.*` calls with `logging.Logger.*` to make the dependency on the configured logger explicit
- **Fallback strategy** (`internal/strategies/fallback.go`): Replaced bare `slog.*` calls with `logging.Logger.*`
- **Request-logger plugin**: Fixed `Execute` to accept a named `ctx context.Context` (was `_`) and use `logging.FromContext(ctx)` — log entries from the plugin now carry the request `trace_id`
- **Gateway** (`gateway.go`): Instruments `Route()` with Prometheus counters/histograms; emits structured log on every request completion or failure
- **Admin handlers** (`internal/admin/handlers.go`): `Handlers.Providers` is now `providers.ProviderSource` instead of `*providers.Registry`
- **`config.go`**: Added `CircuitBreakerConfig` struct and `Target.CircuitBreaker` field

### Internal

- `go.mod`: Added `github.com/prometheus/client_golang v1.23.2`

## [0.1.0] — 2026-02-26

### Added

- **10 LLM Providers**: OpenAI, Anthropic, Google Gemini, Mistral, Groq, Together AI, Azure OpenAI, Cohere, DeepSeek, Ollama (local)
- **4 Routing Strategies**: single provider, fallback with retries + exponential backoff, weighted load balancing, conditional (model-based) routing
- **Transparent Pass-Through Proxy**: Seamless proxying for non-chat endpoints (audio, images, files) with automatic auth injection
- **Streaming**: Server-Sent Events (SSE) support for all providers
- **Plugin System**: Extensible lifecycle hooks (before_request, after_request, on_error) with plugin registry
- **Built-in Plugins**:
  - `response-cache` — exact-match response caching (in-memory LRU with TTL)
  - `word-filter` — configurable word/phrase blocklist guardrail
  - `max-token` — enforce max token, message count, and input length limits
  - `request-logger` — structured JSON request/response logging
- **API Key Management**: In-memory key store with scoped RBAC (admin, read_only), key rotation, expiration
- **OpenAI-Compatible API**: `/v1/chat/completions`, `/v1/models`, `/health`
- **Admin API**: Key CRUD, provider listing, health checks under `/admin/`
- **Configuration**: JSON and YAML config files with validation, CLI validator
- **CLI Tool**: `ferrogw-cli validate`, `ferrogw-cli plugins`, `ferrogw-cli version`
- **Deployment**: Dockerfile, docker-compose.yml
- **License**: Apache License 2.0
