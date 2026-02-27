# Changelog

All notable changes to FerroGateway will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
