# Changelog

All notable changes to FerroGateway will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] — v0.4.0

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
