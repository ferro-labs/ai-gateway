# AGENTS.md

## Project Overview

**Ferro Labs AI Gateway** is a high-performance, open-source AI gateway written in Go. It acts as a unified routing layer between applications and 100+ LLM providers (OpenAI, Anthropic, Gemini, Mistral, etc.), offering smart routing, plugin middleware, and API key management — all with an OpenAI-compatible API and transparent pass-through proxy.

- **Module**: `github.com/ferro-labs/ai-gateway`
- **Go version**: 1.25+
- **License**: Apache 2.0

### Current Development Snapshot

- **29 provider subpackages** — each provider lives in `providers/<id>/<id>.go` with its own test file. No root-level constructor shims remain.
- **Unified factory** — `providers/factory.go` holds types/constants; `providers/providers_list.go` holds all built-in `ProviderEntry` records. Auto-registration via `AllProviders()` means `main.go` never needs editing for new providers.
- **`providers/core/` split** — interfaces in `contracts.go`; shared types split into `chat.go`, `stream.go`, `embedding.go`, `image.go`, `model.go`, `constants.go`, `errors.go`.
- **Single source of truth for name constants** — `providers/names.go` re-exports `NameXxx` from each subpackage's `const Name`.
- **`internal/discovery/`** — shared OpenAI-compatible model discovery helper used by fireworks, hugging_face, perplexity, xai.
- **Provider coverage** — OpenAI, Anthropic, Gemini, Groq, Bedrock, Vertex AI, Hugging Face, Cerebras, Cloudflare, Databricks, DeepInfra, Moonshot, Novita, NVIDIA NIM, OpenRouter, Qwen, SambaNova, and more.
- **Built-in OSS plugins** — word filter, max token, response cache, request logger, rate limit, budget.
- **Admin API** — dashboard, key management, usage stats, request logs, config history/rollback (`internal/admin/handlers.go`).
- **Metrics** — Prometheus metrics exposed at `/metrics` (`internal/metrics/`).
- **Circuit breaker** — per-provider circuit breaker in `internal/circuitbreaker/`.
- **Observability (v1.1.0)** — OpenTelemetry tracing. Public `observability/` package (stable `Provider`/`Span`/`Exporter`/`Event` seam + `gen_ai.*`/`ferro.*` attribute constants); `internal/otel/` wires the OTLP exporter, W3C propagation, and a custom `IDGenerator` that unifies the OTel `trace_id` with the logging trace ID / `X-Request-ID`; `internal/redact/` redacts error messages. Defaults to a zero-allocation NoOp when no OTLP endpoint **and** no exporter are configured.

---

## Build, Test, and Run Commands

```bash
# Build
make build          # builds ./bin/ferrogw
make all            # fmt + lint + test + coverage + build

# Run
make run            # requires at least one provider key, e.g. OPENAI_API_KEY=sk-...

# Test
make test           # unit tests
make test-coverage  # with coverage report
make test-integration  # requires provider API keys

# Code quality
make fmt            # gofmt
make lint           # golangci-lint
make precommit      # fmt + test

# Docker
docker-compose up   # local dev environment
```

---

## Project Structure

```sh
ai-gateway/
├── cmd/
│   └── ferrogw/          # HTTP server + CLI entry point (Cobra subcommands)
│       ├── main.go       # Server setup, provider registration, router, Cobra root
│       ├── cors.go       # CORS middleware
│       ├── completions.go # Legacy /v1/completions handler
│       └── proxy.go      # Pass-through proxy for /v1/*
├── internal/
│   ├── admin/            # API key management + auth middleware
│   ├── cache/            # Cache interface + in-memory implementation
│   ├── cli/              # Shared CLI command implementations (doctor, status, admin, etc.)
│   ├── plugins/          # Built-in plugin implementations
│   │   ├── cache/        # Request/response caching
│   │   ├── logger/       # Request/response logging
│   │   ├── maxtoken/     # Token/message limit guardrail
│   │   └── wordfilter/   # Blocked word guardrail
│   ├── strategies/       # Routing strategy implementations
│   └── version/
├── plugin/               # Public plugin framework (interfaces + manager + registry)
├── observability/        # Public OTel seam: Provider/Span/Exporter/Event + attribute constants + NoOp + exporter registry
├── providers/
│   ├── core/             # Shared interfaces (contracts.go) and types (chat, stream, embedding, image, model)
│   ├── <id>/             # One subpackage per provider
│   ├── factory.go        # ProviderConfig, ProviderEntry, CfgKey* & Capability* consts, lookup funcs
│   ├── providers_list.go # allProviders slice — all built-in ProviderEntry registrations
│   ├── names.go          # NameXxx constants (re-exported from each subpackage)
│   ├── registry.go       # Registry type for runtime lookup by name
│   └── facade_aliases.go # Type aliases re-exporting core.* for backwards compatibility
├── internal/
│   ├── admin/            # API key management, dashboard, logs, config history
│   ├── cache/            # Cache interface + in-memory implementation
│   ├── circuitbreaker/   # Per-provider circuit breaker
│   ├── discovery/        # Shared OpenAI-compatible model discovery helper
│   ├── latency/          # Latency tracking for least-latency strategy
│   ├── metrics/          # Prometheus metrics
│   ├── otel/             # OTel-backed observability.Provider: OTLP exporter, W3C propagation, trace-ID unifying IDGenerator, privacy-aware span errors, HTTP middleware
│   ├── redact/           # Error-message redaction policies (email / JWT / AWS key)
│   ├── plugins/          # Built-in plugin implementations
│   │   ├── cache/        # Request/response caching
│   │   ├── logger/       # Request/response logging
│   │   ├── maxtoken/     # Token/message limit guardrail
│   │   ├── ratelimit/    # Rate limiting
│   │   └── wordfilter/   # Blocked word guardrail
│   ├── ratelimit/        # Rate limit internals
│   ├── strategies/       # Routing strategy implementations
│   └── version/
├── docs/
├── gateway.go            # Core Gateway struct and orchestration
├── config.go             # Config structs (Config, Strategy, Target, Plugin)
├── config_load.go        # LoadConfig(), ValidateConfig()
├── config.example.yaml
└── config.example.json
```

---

## Key Files

| File | Role |
|------|------|
| `gateway.go` | Core `Gateway` struct — routing, plugin lifecycle, strategy execution |
| `config.go` | Config schema: `Config`, `StrategyConfig`, `Target`, `PluginConfig` |
| `config_load.go` | `LoadConfig()` and `ValidateConfig()` for YAML/JSON |
| `providers/core/contracts.go` | `Provider`, `StreamProvider`, `EmbeddingProvider`, `ImageProvider`, `DiscoveryProvider`, `ProxiableProvider` interfaces |
| `providers/factory.go` | `ProviderConfig`, `ProviderEntry`, `CfgKey*` / `Capability*` constants, `AllProviders()`, `GetProviderEntry()` |
| `providers/providers_list.go` | All built-in `ProviderEntry` registrations with `Build` closures |
| `providers/names.go` | Canonical `NameXxx` constants (re-exported from subpackages) |
| `providers/registry.go` | `Registry` — runtime lookup by provider name |
| `plugin/plugin.go` | `Plugin` interface, `PluginType`, `Stage`, `Context` |
| `plugin/manager.go` | Plugin lifecycle: before/after/error stage execution (emits per-plugin child spans) |
| `observability/observability.go` | `Provider`, `Span`, `Exporter`, `Event`, `EventRecordingProvider` interfaces — the gateway↔backend seam |
| `observability/attributes.go` | `gen_ai.*` / `ferro.*` attribute-name constants (Emitted vs Planned) |
| `observability/noop.go` | Zero-allocation default `Provider` (used until `SetObservability`) |
| `observability/registry.go` | `RegisterExporter` / `LookupExporter` — exporter plugin registry |
| `internal/otel/otel.go` | `Init()` — builds an OTLP-backed `Provider` (or NoOp), resolves exporters, returns a grace-bounded `ShutdownFunc` |
| `internal/otel/idgen.go` | Custom `IDGenerator` adopting the logging trace ID so OTel `trace_id` == log trace ID == `X-Request-ID` == `ferro.gateway.trace_id` |
| `internal/otel/config.go` | OTel `Config` (endpoint, protocol, sample_ratio, privacy_level, shutdown_grace) + `Validate()` |
| `internal/redact/redact.go` | `Redactor` applied to span/event error messages |
| `internal/strategies/strategy.go` | `Strategy` interface |
| `internal/discovery/openai_compat.go` | `DiscoverOpenAICompatibleModels` — shared by fireworks, hugging_face, perplexity, xai |
| `cmd/ferrogw/main.go` | HTTP server setup and entry point |
| `internal/admin/middleware.go` | Bearer token auth middleware |

---

## Architecture & Design Patterns

- **Strategy Pattern**: Routing strategies (`Single`, `Fallback`, `LoadBalance`, `LeastLatency`, `CostOptimized`, `Conditional`) all implement `Strategy` interface in `internal/strategies/`
- **Self-Describing Factory**: Each provider has a `ProviderEntry` in `providers/providers_list.go` — no `main.go` changes needed to add a provider
- **Two-Mode Provider Init**: `ProviderConfigFromEnv` (OSS self-hosted) or direct `ProviderConfig` map (cloud/tenant credential injection)
- **Plugin Middleware**: `plugin/manager.go` runs plugins at `before_request`, `after_request`, `on_error` stages
- **OpenAI Compatibility**: All requests/responses match OpenAI spec — other provider responses are translated
- **Pass-Through Proxy**: Unhandled `/v1/*` endpoints forwarded transparently via `cmd/ferrogw/proxy.go`
- **Compile-time assertions**: Every provider subpackage has `var _ core.XxxProvider = (*Provider)(nil)` guards
- **Observability seam**: `Gateway` holds exactly one `observability.Provider` (NoOp by default; install via `SetObservability`). `Route`/`RouteStream` open a `gateway.request` root span and stamp `gen_ai.*`/`ferro.*` attributes; plugins and MCP tool calls emit child spans. Registered exporters receive `gateway.request.completed`/`failed` events. With OTel disabled the hot path stays at the NoOp allocation baseline (asserted by `TestRoute_TracingOff_AllocBaseline`). Standard `OTEL_*` env vars take precedence over config.

### Request Flow

```sh
Client → HTTP Router → before_request plugins → Strategy selection
  → Provider.Complete() / CompleteStream() → after_request plugins → Response
```

### Concurrency

- `sync.RWMutex` in `Gateway` for thread-safe reads/writes
- Streaming uses `<-chan providers.StreamChunk` channels
- Async event dispatch via goroutines

---

## Configuration

Config is loaded from YAML or JSON (auto-detected). Path defaults from env var `GATEWAY_CONFIG`.

```yaml
strategy:
  mode: fallback  # single | fallback | loadbalance | conditional

targets:
  - virtual_key: openai
    weight: 1.0
    retry:
      attempts: 3
  - virtual_key: anthropic
    weight: 1.0

plugins:
  - name: word-filter
    type: guardrail
    stage: before_request
    enabled: true
    config:
      blocked_words: ["password", "secret"]

observability:
  tracing:
    enabled: true
    endpoint: ""             # host:port; blank falls back to OTEL_EXPORTER_OTLP_ENDPOINT
    protocol: grpc           # grpc | http/protobuf  (https:// endpoint ⇒ TLS, else insecure)
    service_name: ferrogw
    sample_ratio: 1.0        # head sampler 0.0–1.0
    privacy_level: metadata  # none | metadata (redacted, default) | full (raw error text)
    shutdown_grace: 10s      # max drain time for in-flight OTel exports on shutdown
  exporters:                 # plugin exporters receiving completed/failed events; none ship in-repo
    - name: langsmith
      enabled: false
      config: {}
```

### Key Environment Variables

| Variable | Purpose |
|----------|---------|
| `MASTER_KEY` | Single admin credential for all auth (use `ferrogw init` to generate) |
| `GATEWAY_CONFIG` | Path to config YAML/JSON |
| `PORT` | Server port (default: 8080) |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `GROQ_API_KEY` | Groq API key |
| `MISTRAL_API_KEY` | Mistral API key |
| `TOGETHER_API_KEY` | Together AI API key |
| `COHERE_API_KEY` | Cohere API key |
| `DEEPSEEK_API_KEY` | DeepSeek API key |
| `AZURE_OPENAI_API_KEY` | Azure OpenAI API key |
| `AZURE_OPENAI_ENDPOINT` | Azure OpenAI endpoint URL |
| `AZURE_OPENAI_DEPLOYMENT` | Azure deployment name |
| `AZURE_OPENAI_API_VERSION` | Azure API version |
| `OLLAMA_HOST` | Ollama server URL |
| `OLLAMA_MODELS` | Comma-separated Ollama model list |
| `REPLICATE_API_TOKEN` | Replicate API token |
| `XAI_API_KEY` | xAI (Grok) API key |
| `AZURE_FOUNDRY_API_KEY` | Azure AI Foundry API key |
| `AZURE_FOUNDRY_ENDPOINT` | Azure AI Foundry endpoint URL |
| `HUGGING_FACE_API_KEY` | Hugging Face API token |
| `VERTEX_AI_PROJECT_ID` | Google Cloud project ID (Vertex AI) |
| `VERTEX_AI_REGION` | GCP region for Vertex AI |
| `VERTEX_AI_API_KEY` | Vertex AI API key (alternative to service account) |
| `AWS_REGION` | AWS region (Bedrock) |
| `AWS_ACCESS_KEY_ID` | AWS access key (optional — falls back to instance role) |
| `AWS_SECRET_ACCESS_KEY` | AWS secret key |
| `CORS_ORIGINS` | Comma-separated allowed CORS origins |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector endpoint; enables tracing when set (takes precedence over config) |
| `OTEL_TRACES_SAMPLER` / `OTEL_TRACES_SAMPLER_ARG` | Standard OTel head-sampler overrides |

---

## HTTP API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/v1/models` | GET | List all available models |
| `/v1/chat/completions` | POST | Chat completion (supports `stream: true`) |
| `/v1/completions` | POST | Legacy text completion |
| `/v1/*` | Any | Pass-through proxy to provider |
| `/admin/keys` | GET, POST | API key management (requires auth) |
| `/metrics` | GET | Prometheus metrics |
| `/admin/*` | Mixed | Admin dashboard, usage stats, request logs, config history/rollback (see `internal/admin/handlers.go`) |

---

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/go-chi/chi/v5` | HTTP router |
| `github.com/openai/openai-go` | OpenAI Go SDK |
| `gopkg.in/yaml.v3` | YAML config parsing |
| `github.com/aws/aws-sdk-go-v2` | AWS Bedrock integration |
| `github.com/prometheus/client_golang` | Prometheus metrics |
| `github.com/santhosh-tekuri/jsonschema/v5` | JSON schema validation (schemaguard plugin) |
| `golang.org/x/oauth2` | Vertex AI service-account auth |
| `github.com/spf13/cobra` | CLI subcommands (`ferrogw init`, `ferrogw doctor`, etc.) |
| `modernc.org/sqlite` | SQLite for admin/key storage |
| `github.com/lib/pq` | PostgreSQL support |
| `go.opentelemetry.io/otel` (+ `sdk`, `trace`, OTLP `otlptrace*` exporters) | OpenTelemetry tracing pipeline |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | Outbound provider-call CLIENT spans + `traceparent` propagation |

Minimal by design — no heavy logging framework, no ORM.

---

## Adding a New Provider

**No changes to `cmd/ferrogw/main.go` are needed.** The gateway auto-registers all entries in `providers/providers_list.go`.

1. Create `providers/<id>/<id>.go` (package `<id>`) — implement `core.Provider` and any optional interfaces (`core.StreamProvider`, etc.). Add compile-time assertions:
   ```go
   var (
       _ core.Provider       = (*Provider)(nil)
       _ core.StreamProvider = (*Provider)(nil)
   )
   ```
2. Add `const Name = "<id>"` in the new package and re-export it in `providers/names.go`:
   ```go
   import newpkg "github.com/ferro-labs/ai-gateway/providers/<id>"
   const NameNew = newpkg.Name
   ```
3. Add a `ProviderEntry` to the `allProviders` slice in `providers/providers_list.go` — fill in `ID`, `Capabilities`, `EnvMappings`, and `Build`.
4. Add `providers/<id>/<id>_test.go` — the stability tests in `providers/stability_test.go` automatically catch name drift and missing capabilities.
5. Add a `{ "virtual_key": "<id>" }` entry to `config.example.json` and a `- virtual_key: <id>` line to `config.example.yaml`.
6. Add the provider's env var(s) (commented out) to `docker-compose.yml`.

## Adding a New Plugin

1. Create `internal/plugins/<name>/<name>.go` (package `<name>`) implementing `plugin.Plugin`.
2. Register a factory via `plugin.RegisterFactory("my-plugin", ...)` in an `init()` function.
3. Add a blank import in `cmd/ferrogw/main.go`: `_ "github.com/ferro-labs/ai-gateway/internal/plugins/<name>"`

## Adding a New Strategy

1. Create `internal/strategies/<name>.go` implementing `strategies.Strategy`.
2. Handle the new `StrategyMode` constant in `gateway.go`'s strategy selection logic.
3. Add tests in `internal/strategies/<name>_test.go`.

## Adding an Observability Exporter

Exporters bridge gateway events to a backend (LangSmith, Langfuse, Datadog, …). They live in the
separate `ai-gateway-plugins` repo, not here — the gateway only ships the contract + wiring.

1. Implement `observability.Exporter` (`Name`, `Init(cfg map[string]any)`, `Export(ctx, Event)`, `Shutdown(ctx)`). `Export` must be safe for concurrent use and non-blocking.
2. Register a factory in `init()`: `observability.RegisterExporter("<name>", New)`.
3. Configure it under `observability.exporters` (`name`/`enabled`/`config`) — `internal/otel.Init` resolves enabled entries via `LookupExporter`; unknown/failed exporters are logged and skipped (non-fatal). Exporters work even with no OTLP endpoint.
4. Emit new span attributes only via constants in `observability/attributes.go`; mark not-yet-wired ones as Planned.

---

## Testing Conventions

The gateway has three test suites, each with its own build tag and Make target:

### 1. Unit tests (default, no build tag)

Live alongside implementation as `*_test.go`.

```bash
make test           # go test -v -short -race ./...
make test-coverage  # with coverage HTML report
```

### 2. Integration tests (build tag: `integration`)

Located in `test/integration/` and sub-packages (`http/`, `plugins/`, `strategies/`).
Spin up an in-process gateway with stub providers — no real LLM calls.
The `test/integration/` package itself uses testcontainers-go for a real Postgres 16
container to test key store, config store, and request log persistence.

```bash
make test-integration          # go test -tags=integration -race ./test/integration/...
make test-integration-postgres # alias for the above
```

Postgres requirement: testcontainers-go pulls `postgres:16-alpine` automatically.
Without Docker available locally, the Postgres-dependent tests skip cleanly.
The `test/integration/http/`, `plugins/`, and `strategies/` sub-packages do not
require Postgres and always run.

Build tag headers on every integration test file:
```go
//go:build integration
// +build integration
```

### Additional checks

- `go test ./internal/admin/...`
- `go test ./internal/plugins/logger/...`
- Prefer UTC assertions for persisted/admin timestamps.
- For dashboard rendering, avoid `innerHTML` with API data; use DOM node creation APIs.
