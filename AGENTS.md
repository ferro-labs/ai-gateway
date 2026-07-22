# AGENTS.md

## Project Overview

**Ferro Labs AI Gateway** is a high-performance, open-source AI gateway written in Go. It acts as a unified routing layer between applications and 30 LLM providers (OpenAI, Anthropic, Gemini, Mistral, etc.), offering smart routing, plugin middleware, and API key management — all with an OpenAI-compatible API and transparent pass-through proxy.

- **Module**: `github.com/ferro-labs/ai-gateway`
- **Go version**: 1.25+
- **License**: Apache 2.0

### Current Development Snapshot

- **29 provider subpackages** — each provider lives in `providers/<id>/<id>.go` with its own test file. No root-level constructor shims remain. <!-- drift-ok: pre-existing subpackage count, not the canonical provider count -->
- **Unified factory** — `providers/factory.go` holds types/constants; `providers/providers_list.go` holds all built-in `ProviderEntry` records. Auto-registration via `AllProviders()` means `main.go` never needs editing for new providers.
- **`providers/core/` split** — interfaces in `contracts.go`; shared types split into `chat.go`, `stream.go`, `embedding.go`, `image.go`, `model.go`, `constants.go`, `errors.go`.
- **Single source of truth for name constants** — `providers/names.go` re-exports `NameXxx` from each subpackage's `const Name`.
- **`internal/discovery/`** — shared OpenAI-compatible model discovery helper used by many OpenAI-compatible providers (fireworks, xai, moonshot, nvidia-nim, novita, …).
- **Provider coverage** — OpenAI, Anthropic, Gemini, Groq, Bedrock, Vertex AI, Hugging Face, Cerebras, Cloudflare, Databricks, DeepInfra, Moonshot, Novita, NVIDIA NIM, OpenRouter, Qwen, SambaNova, and more.
- **Built-in OSS plugins** — word filter, max token, response cache, request logger, rate limit, budget.
- **Admin API** — dashboard, key management, usage stats, request logs, config history/rollback (`internal/admin/handlers.go`).
- **Metrics** — Prometheus metrics exposed at `/metrics` (`internal/metrics/`).
- **Circuit breaker** — per-provider circuit breaker in `internal/circuitbreaker/`.
- **Observability (v1.1.0)** — OpenTelemetry tracing. Public `observability/` package (stable `Provider`/`Span`/`Exporter`/`Event` seam + `gen_ai.*`/`ferro.*` attribute constants); `internal/otel/` wires the OTLP exporter, W3C propagation, and a custom `IDGenerator` that unifies the OTel `trace_id` with the logging trace ID / `X-Request-ID`; `internal/redact/` redacts error messages. Defaults to a zero-allocation NoOp when no OTLP endpoint **and** no exporter are configured.
- **Capability matrix (v1.2.0)** — `providers/capabilities/matrix.go` is the single source of truth for which OpenAI chat parameters each provider can express. Providers no longer keep private supported-parameter lists; `core.EnforceUnsupportedParams` reads the matrix and applies `compatibility.on_unsupported_param` (warn | drop | reject). Served by `GET /v1/capabilities`.
- **Conformance suite (v1.2.0)** — `test/conformance/` builds every provider through its `ProviderEntry`, points it at an httptest stub returning that provider's *native* payload, and asserts the translated `core.Response`. No build tag, no network: it runs with `make test`. `TestConformanceCoverage` fails on any provider with neither a fixture nor an allowlist reason.
- **Plugin failure policy (v1.2.0)** — a plugin that *denies* (`Context.Reject`) and a plugin that *breaks* (error/panic) are distinct: `RejectionError` keeps its 4xx/429, `FailureError` is a 500. Logging and metrics plugins fail open; guardrail, auth, ratelimit, transform, and unknown types fail closed. `Reject` is honoured for every type.
- **Env references (v1.2.0)** — `internal/envref` resolves `${VAR}` **at component construction**, never at config load, so the `Config` never carries a materialised secret into the config-history store, `GET /admin/config`, or a rollback. A bare `$` is data (`$100`, `pa$$w0rd` survive); an undefined variable is an error.
- **Per-target concurrency (v1.2.0)** — `targets[].concurrency` bounds in-flight requests per provider with a queue; overflow returns `ErrProviderSaturated` → 429. The limiter decorates the provider at the call site (innermost, circuit breaker outermost) and never forges capability interfaces.
- **Health split (v1.2.0)** — `/livez` (process alive) and `/readyz` (ready to serve) replace the single `/health` semantics; `/health` is retained.

---

## Public-Facing Wording

Keep all public-facing text — commit messages, godocs, `CHANGELOG.md`, `ROADMAP.md`, and GitHub issues/PRs — **neutral and outcome-focused**. Do **not** reference internal tooling, code-review services, AI assistants, private decisions, or how the change was produced; describe *what* changed and *why* it matters to users. Commit messages stay short and imperative; godocs stay brief with no meta-commentary or disclaimers.

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
│       └── main.go       # Server setup, provider registration, router, Cobra root
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
│   ├── capabilities/     # Provider × parameter support matrix — single source of truth (v1.2.0)
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
│   ├── envref/           # Shared ${VAR} resolver — applied at component construction, not config load
│   ├── latency/          # Latency tracking for least-latency strategy
│   ├── metrics/          # Prometheus metrics
│   ├── migrations/       # Versioned schema-migration runner (schema_migrations ledger)
│   ├── otel/             # OTel-backed observability.Provider: OTLP exporter, W3C propagation, trace-ID unifying IDGenerator, privacy-aware span errors, HTTP middleware
│   ├── redact/           # Error-message redaction policies (email / JWT / AWS key)
│   ├── plugins/          # Built-in plugin implementations
│   │   ├── cache/        # Request/response caching
│   │   ├── logger/       # Request/response logging
│   │   ├── maxtoken/     # Token/message limit guardrail
│   │   ├── ratelimit/    # Rate limiting
│   │   └── wordfilter/   # Blocked word guardrail
│   ├── handler/          # HTTP handlers (chat, completions, embeddings, images, models)
│   ├── middleware/       # HTTP middleware (CORS, body-limit, rate-limit, security headers)
│   ├── proxy/            # Pass-through proxy for /v1/*
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
| `providers/capabilities/matrix.go` | Provider × parameter support matrix — the single source consumed by `core.EnforceUnsupportedParams` and `GET /v1/capabilities` |
| `internal/envref/envref.go` | Shared `${VAR}` resolver (`Expand`/`StringMap`/`AnyMap`) — used at plugin/exporter/MCP construction |
| `gateway_concurrency.go` | Per-target concurrency limiter + provider decoration (limiter innermost, circuit breaker outermost) |
| `test/conformance/conformance_test.go` | Cross-provider conformance suite + coverage drift guard |
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
| `internal/discovery/openai_compat.go` | `DiscoverOpenAICompatibleModels` — shared by many OpenAI-compatible providers (fireworks, xai, moonshot, nvidia-nim, …) |
| `cmd/ferrogw/main.go` | HTTP server setup and entry point |
| `internal/admin/middleware.go` | Bearer token auth middleware |

---

## Architecture & Design Patterns

- **Strategy Pattern**: Routing strategies (`Single`, `Fallback`, `LoadBalance`, `LeastLatency`, `CostOptimized`, `Conditional`, `ContentBased`, `ABTest`) all implement `Strategy` interface in `internal/strategies/`
- **Self-Describing Factory**: Each provider has a `ProviderEntry` in `providers/providers_list.go` — no `main.go` changes needed to add a provider
- **Two-Mode Provider Init**: `ProviderConfigFromEnv` (OSS self-hosted) or direct `ProviderConfig` map (cloud/tenant credential injection)
- **Plugin Middleware**: `plugin/manager.go` runs plugins at `before_request`, `after_request`, `on_error` stages
- **OpenAI Compatibility**: All requests/responses match OpenAI spec — other provider responses are translated
- **Pass-Through Proxy**: Unhandled `/v1/*` endpoints forwarded transparently via `internal/proxy/proxy.go`
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

mcp_servers:                 # external MCP tool servers for agentic tool calling
  - name: filesystem
    command: npx             # stdio transport: launched as a subprocess
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
    env:                     # the subprocess inherits NO gateway environment
      SOME_TOKEN: ${SOME_TOKEN}
    timeout_seconds: 30      # per tool call; default 30
    required: false          # see below; default false
  - name: search
    url: https://mcp.example.com/mcp   # Streamable HTTP transport
    headers:
      Authorization: Bearer ${SEARCH_TOKEN}
    allowed_tools: ["web_search"]      # empty means all discovered tools

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

### MCP Server Readiness (`mcp_servers[].required`)

`required` decides whether one MCP server's availability gates the whole
instance's readiness. It is **opt-in per server and defaults to `false`**, so an
existing config behaves exactly as it did before the field existed.

| `required` | Server unready | `/readyz` |
|------------|----------------|-----------|
| absent / `false` | reported in the body | `200 ready` |
| `true` | reported in the body | `503 not_ready`, reason `required mcp server unavailable` |

A server is unready when it never completed the initialize handshake. That
covers a server whose transport could not be built at all — an unresolvable
`${VAR}` in its `headers` or `env` — which is reported unready rather than
omitted from the body.

Death **after** a successful handshake is detected for **stdio servers only**. A
stdio server whose subprocess exits is noticed two ways — the process closing
its error stream, confirmed with a ping before anything is withdrawn, and a tool
call failing with a closed transport or a broken pipe — and its tools are then
withdrawn from the model, so it stops being advertised and stops resolving.

An HTTP server that becomes unreachable after a successful handshake is **not
currently detected**: it continues to report ready and its tools stay
advertised, and calls to it fail per request. Do not rely on `required: true`
to take an instance out of rotation when an HTTP MCP server goes down.

Set `required: true` only for a server the deployment genuinely cannot serve
without. A required server that is down takes the instance out of rotation and
stops **all** traffic through it, including requests that use no tools at all.
Every server's state appears under `mcp_servers` in the `/readyz` body whether
or not it is required, so MCP health can be monitored without gating on it.

The failure *reason* is deliberately not in the `/readyz` body: the endpoint is
unauthenticated and an MCP failure can quote a server URL, an authorization
header, or a subprocess command line. It is logged server-side instead.

### Key Environment Variables

| Variable | Purpose |
|----------|---------|
| `MASTER_KEY` | Single admin credential for all auth (use `ferrogw init` to generate) |
| `GATEWAY_CONFIG` | Path to config YAML/JSON |
| `GATEWAY_ENV` | Set to `production` to enable production-mode safety guards (e.g. refuses to start if `ALLOW_UNAUTHENTICATED_PROXY=true`); unset or any other value is non-production mode |
| `PORT` | Server port (default: 8080) |
| `FERRO_MODEL_CATALOG_URL` | Override the model catalog source URL (used by `/v1/models` and model routing) |
| `FERRO_MODEL_CATALOG_TIMEOUT` | Go duration bounding the catalog fetch (default 10s). The fetch runs during startup, before the listener binds, so a blocked-egress deployment waits this long before falling back to the embedded catalog. Set `0` to skip the remote fetch entirely |
| `FERRO_MODEL_DISCOVERY_INTERVAL` | Opt-in interval (Go duration, e.g. 6h) to live-refresh model lists from provider /models endpoints; unset disables |
| `ALLOW_UNAUTHENTICATED_PROXY` | Set to `true` to disable proxy-route auth (dev/local only; blocked when `GATEWAY_ENV=production`) |
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
| `TRUSTED_PROXIES` | Comma-separated CIDRs of trusted reverse proxies; `X-Forwarded-For`/`X-Real-IP` is honored only from these (default: loopback) |
| `RATE_LIMIT_RPS` | Per-IP rate limit requests/sec; enabled by default (20 rps / burst 40). Set to `0` to disable. Setting this alone resets burst to the default 40 too — pair with `RATE_LIMIT_BURST` for a custom rate/burst combination. Keys on the resolved client IP, so `TRUSTED_PROXIES` must list the real proxy CIDR or all traffic behind an untrusted proxy shares one bucket |
| `RATE_LIMIT_BURST` | Per-IP burst capacity override (default: 40) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector endpoint; enables tracing when set (takes precedence over config) |
| `OTEL_TRACES_SAMPLER` / `OTEL_TRACES_SAMPLER_ARG` | Standard OTel head-sampler overrides |

---

## HTTP API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/livez` | GET | Liveness — the process is up |
| `/readyz` | GET | Readiness — the gateway can serve traffic |
| `/v1/models` | GET | List all available models |
| `/v1/capabilities` | GET | Per-provider parameter support, serialized from the capability matrix |
| `/v1/chat/completions` | POST | Chat completion (supports `stream: true`) |
| `/v1/route/trace` | POST | Dry-run route explanation — selects a target/strategy WITHOUT calling a provider; returns candidates, health/circuit state, catalog match (issue #238) |
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
4. If the provider **cannot express** some OpenAI chat parameters, add an entry to the matrix in `providers/capabilities/matrix.go` listing them (the complement of what it supports). Anything absent defaults to `Forward`. Do **not** keep a supported-parameter list inside the provider — the matrix is the only source, and `core.EnforceUnsupportedParams` reads it. A provider whose params are all forwarded needs no entry.
5. Add a conformance fixture to `test/conformance/conformance_test.go` — the provider's *native* success payload — so the suite proves your translation produces a correct `core.Response`. If the provider genuinely cannot be pointed at an httptest stub (e.g. an SDK-signed transport), add it to `uncoveredProviders()` with the reason instead. `TestConformanceCoverage` fails if you do neither.
6. Add `providers/<id>/<id>_test.go` — the stability tests in `providers/stability_test.go` automatically catch name drift and missing capabilities.
7. Add a `{ "virtual_key": "<id>" }` entry to `config.example.json` and a `- virtual_key: <id>` line to `config.example.yaml`.
8. Add the provider's env var(s) (commented out) to `docker-compose.yml`.

## Adding a New Plugin

**Deny with `pctx.Reject` + `pctx.Reason`, and return `nil`.** Return an error only when the plugin itself failed — an error means "I broke", and for every type except logging and metrics it aborts the request as a 500. See the `plugin` package docs.

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
