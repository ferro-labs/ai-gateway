# AGENTS.md

## Project Overview

**Ferro Labs AI Gateway** is a high-performance, open-source AI gateway written in Go. It acts as a unified routing layer between applications and 30 LLM providers (OpenAI, Anthropic, Gemini, Mistral, etc.), offering smart routing, plugin middleware, and API key management — all with an OpenAI-compatible API and transparent pass-through proxy.

- **Module**: `github.com/ferro-labs/ai-gateway`
- **Go version**: 1.25+
- **License**: Apache 2.0

### Current Development Snapshot

- **30 provider subpackages** — each provider lives in `providers/<id>/<id>.go` with its own test file, one per `ProviderEntry` in `providers/providers_list.go`. (`providers/core/` and `providers/capabilities/` are shared code, not providers.) No root-level constructor shims remain.
- **Unified factory** — `providers/factory.go` holds types/constants; `providers/providers_list.go` holds all built-in `ProviderEntry` records. Auto-registration via `AllProviders()` means `main.go` never needs editing for new providers.
- **`providers/core/` split** — interfaces in `contracts.go`; shared types split into `chat.go`, `stream.go`, `embedding.go`, `image.go`, `model.go`, `constants.go`, `errors.go`.
- **Single source of truth for name constants** — `providers/names.go` re-exports `NameXxx` from each subpackage's `const Name`.
- **Model discovery** — `providers/core` exposes `DiscoverOpenAICompatibleModels`/`DiscoverModelsWithHeaders` for live `/models` enumeration, shared by many OpenAI-compatible providers (fireworks, xai, moonshot, nvidia-nim, novita, …).
- **Provider coverage** — OpenAI, Anthropic, Gemini, Groq, Bedrock, Vertex AI, Hugging Face, Cerebras, Cloudflare, Databricks, DeepInfra, Moonshot, Novita, NVIDIA NIM, OpenRouter, Qwen, SambaNova, and more.
- **Built-in OSS plugins** — word filter, max token, response cache, request logger, rate limit, budget.
- **Admin API** — dashboard, key management, usage stats, request logs, config history/rollback (`internal/admin/handlers/`).
- **Metrics** — Prometheus metrics exposed at `/metrics` (`pkg/metrics/`).
- **Circuit breaker** — per-provider circuit breaker in `pkg/circuitbreaker/`.
- **Observability (v1.1.0)** — OpenTelemetry tracing. Public `observability/` package (stable `Provider`/`Span`/`Exporter`/`Event` seam + `gen_ai.*`/`ferro.*` attribute constants); `internal/otel/` wires the OTLP exporter, W3C propagation, and a custom `IDGenerator` that unifies the OTel `trace_id` with the logging trace ID / `X-Request-ID`; `internal/redact/` redacts error messages. Defaults to a zero-allocation NoOp when no OTLP endpoint **and** no exporter are configured.
- **Capability matrix (v1.2.0)** — `providers/capabilities/matrix.go` is the single source of truth for which OpenAI chat parameters each provider can express. Providers no longer keep private supported-parameter lists; `core.EnforceUnsupportedParams` reads the matrix and applies `compatibility.on_unsupported_param` (warn | drop | reject). Served by `GET /v1/capabilities`.
- **Conformance suite (v1.2.0)** — `test/conformance/` builds every provider through its `ProviderEntry`, points it at an httptest stub returning that provider's *native* payload, and asserts the translated `core.Response`. No build tag, no network: it runs with `make test`. `TestConformanceCoverage` fails on any provider with neither a fixture nor an allowlist reason.
- **Plugin failure policy (v1.2.0)** — a plugin that *denies* (`Context.Reject`) and a plugin that *breaks* (error/panic) are distinct: `RejectionError` keeps its 4xx/429, `FailureError` is a 500. Logging and metrics plugins fail open; guardrail, auth, ratelimit, transform, and unknown types fail closed. `Reject` is honoured for every type. The type that decides this is the one the plugin reports from `Type()`, not `plugins[].type` in config.
- **Env references (v1.2.0)** — `internal/envref` resolves `${VAR}` **at component construction**, never at config load, so the `Config` never carries a materialised secret into the config-history store, `GET /admin/config`, or a rollback. A bare `$` is data (`$100`, `pa$$w0rd` survive); an undefined variable is an error.
- **Per-target concurrency (v1.2.0)** — `targets[].concurrency` bounds in-flight requests per provider with a queue; overflow returns `ErrProviderSaturated` → 429. The limiter decorates the provider at the call site (innermost, circuit breaker outermost) and never forges capability interfaces.
- **Health split (v1.2.0)** — `/livez` (process alive) and `/readyz` (ready to serve) replace the single `/health` semantics; `/health` is retained.
- **Plugin short-circuiting (Unreleased)** — `Context.Skip` is **removed**; code that sets or reads it no longer compiles. `Context.SkipProvider` replaces it and skips the *provider call* only: every remaining `before_request` plugin and the whole `after_request` stage still run, so a response-cache hit can no longer disable a guardrail, rate limiter or budget behind it. Only a rejection or a plugin failure ends the `before_request` stage, and it ends that stage alone — `on_error` still runs, so a request denied by policy is still recorded. `SkipProvider` stays set through `after_request` as a fact (cache-served vs provider-served) that cost recording keys off.
- **Request attribution (Unreleased)** — `Context.Target` names the routing target a request used: the virtual key that served it, or on a failure the last one attempted, empty when none was attempted (denied by a plugin, no target serves the model, or served from cache). It exists because a failure has no response to read a provider from, which left `on_error` rows naming no provider. Request-log rows also carry `api_key_id` — the credential's opaque id, never the credential — so usage is attributable, including a request served from cache, which reaches no provider but is still consumed by a credential. (A cached response is only ever served back to the credential that primed it — the response cache keys on `api_key_id` — so a row's credential is the one that made the call, not the one that filled the cache.) A cache-served request is priced at a known zero rather than as though the provider had been called.
- **Unified request pipeline (Unreleased)** — chat, streaming, embeddings and image generation all route through `routeTargets` in `gateway_pipeline.go`. Retry, circuit breaker, per-target concurrency, error classification, metrics and request logging live there once, so the four surfaces cannot drift. `targets[].retry` is honoured under **every** routing mode, not only `fallback`.
- **One completion ceiling (Unreleased)** — `max_tokens` and `max_completion_tokens` are resolved to a single value at every entry point, `max_completion_tokens` winning when both are present, so a guardrail and the provider read the same number. See [Completion length](#completion-length-max_tokens-and-max_completion_tokens).
- **Declared models (Unreleased)** — `targets[].models` lets an operator name the models a target serves, joined into the routing index alongside the catalog and live discovery and advertised by `/v1/models`. It is provider-agnostic and additive: it needs no `/models` endpoint, works offline, and never narrows what a target already serves. See [Declared models](#declared-models-targetsmodels).
- **Strategy validation at load (Unreleased)** — every strategy is validated by `config.ValidateConfig`, so an invalid one is a `ferrogw validate` / startup error rather than a request-time 500. Unknown `conditions[].key` and `content_conditions[].type` values are load errors; `weight: 0` means zero traffic, a negative weight is an error, and an all-zero weight set is an error.

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
make test-integration       # in-process gateway, stub providers, Postgres via testcontainers — no API keys
make test-integration-live  # real provider calls (-tags=live); requires provider API keys

# Code quality
make fmt            # gofmt
make lint           # golangci-lint
make precommit      # fmt + test

# Docker
make up             # local dev environment (docker compose, builds from source)
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
│   ├── cli/              # Shared CLI command implementations (doctor, status, admin, etc.)
│   ├── strategies/       # Routing strategy implementations
│   └── version/
├── plugin/               # Public plugin framework (interfaces + manager + registry) + built-in plugins
│   ├── budget/           # Budget guardrail
│   ├── cache/            # Request/response caching
│   ├── logger/           # Request/response logging
│   ├── maxtoken/         # Token/message limit guardrail
│   ├── ratelimit/        # Rate limiting
│   └── wordfilter/       # Blocked word guardrail
├── pkg/                  # Public reusable libraries
│   ├── cache/            # In-memory TTL cache (used by the cache plugin)
│   ├── logger/           # Structured slog-backed logging service
│   ├── circuitbreaker/   # Per-provider circuit breaker
│   ├── metrics/          # Prometheus metrics
│   └── ratelimit/        # Rate limiter shared by the plugin and HTTP middleware
├── observability/        # Public OTel seam: Provider/Span/Exporter/Event + attribute constants + NoOp + exporter registry
├── providers/
│   ├── capabilities/     # Provider × parameter support matrix — single source of truth (v1.2.0)
│   ├── core/             # Shared interfaces (contracts.go), types (chat, stream, embedding, image, model), model discovery
│   │   ├── anthropicwire/ # Anthropic wire-format helper (shared by anthropic + bedrock)
│   │   └── openaicompat/  # OpenAI-compatible chat/stream/embedding translation
│   ├── <id>/             # One subpackage per provider
│   ├── factory.go        # ProviderConfig, ProviderEntry, CfgKey* & Capability* consts, lookup funcs
│   ├── providers_list.go # allProviders slice — all built-in ProviderEntry registrations
│   ├── names.go          # NameXxx constants (re-exported from each subpackage)
│   ├── registry.go       # Registry type for runtime lookup by name
│   └── facade_aliases.go # Type aliases re-exporting core.* for backwards compatibility
├── internal/
│   ├── admin/            # Admin control-plane, split by layer:
│   │   ├── model/        #   domain types (APIKey, Session, AuditEntry, …) + api-key/actor context kernel
│   │   ├── repository/   #   key/session/config/audit stores + migrations + config scrubbing + embedded schema
│   │   └── handlers/     #   HTTP handlers, routes, auth middleware (dashboard, keys, logs, config history)
│   ├── envref/           # Shared ${VAR} resolver — applied at component construction, not config load
│   ├── latency/          # Latency tracking for least-latency strategy
│   ├── migrations/       # Versioned schema-migration runner (schema_migrations ledger)
│   ├── otel/             # OTel-backed observability.Provider: OTLP exporter, W3C propagation, trace-ID unifying IDGenerator, privacy-aware span errors, HTTP middleware
│   ├── redact/           # Error-message redaction policies (email / JWT / AWS key)
│   ├── handler/          # HTTP handlers (chat, completions, embeddings, images, models)
│   ├── middleware/       # HTTP middleware (CORS, body-limit, rate-limit, security headers)
│   ├── proxy/            # Pass-through proxy for /v1/*
│   ├── strategies/       # Routing strategy implementations
│   └── version/
├── web/                  # React/TypeScript operations dashboard (Tailwind + shadcn).
│                         # Builds to a bundle the binary embeds via internal/webui —
│                         # no separate web image and no second origin
│   ├── src/              # SPA application and component tests
│   ├── public/           # Runtime configuration defaults
│   └── README.md         # Development and deployment contract
├── config/               # Config schema + loader
│   ├── config.go         # Config structs (Config, Strategy, Target, Plugin)
│   └── load.go           # LoadConfig(), ValidateConfig()
├── deploy/               # Container + Compose manifests
│   ├── Dockerfile                 # One image; a node stage builds the bundle the Go stage embeds
│   ├── gateway.release.Dockerfile # Packages a GoReleaser-built binary
│   └── compose{,.dev,.prod}.yaml  # Base plus one override; driven by make up / up-prod
├── docs/
├── gateway.go            # Core Gateway struct and orchestration
├── config.example.yaml
└── config.example.json
```

`.dockerignore` and `render.yaml` stay at the repository root on purpose:
Docker resolves `.dockerignore` from the build context root rather than from
beside the Dockerfile, and Render only reads `render.yaml` from the root. Both
point into `deploy/`.

---

## Key Files

| File | Role |
|------|------|
| `gateway.go` | Core `Gateway` struct — routing, plugin lifecycle, strategy execution |
| `config/config.go` | Config schema: `Config`, `StrategyConfig`, `Target`, `PluginConfig` |
| `config/load.go` | `LoadConfig()` and `ValidateConfig()` for YAML/JSON — decoding is strict, so an unknown key is rejected |
| `providers/core/contracts.go` | `Provider`, `StreamProvider`, `EmbeddingProvider`, `ImageProvider`, `DiscoveryProvider`, `ProxiableProvider` interfaces |
| `providers/factory.go` | `ProviderConfig`, `ProviderEntry`, `CfgKey*` / `Capability*` constants, `AllProviders()`, `GetProviderEntry()` |
| `providers/providers_list.go` | All built-in `ProviderEntry` registrations with `Build` closures |
| `providers/names.go` | Canonical `NameXxx` constants (re-exported from subpackages) |
| `providers/registry.go` | `Registry` — runtime lookup by provider name |
| `providers/capabilities/matrix.go` | Provider × parameter support matrix — the single source consumed by `core.EnforceUnsupportedParams` and `GET /v1/capabilities` |
| `internal/envref/envref.go` | Shared `${VAR}` resolver (`Expand`/`StringMap`/`AnyMap`) — used at plugin/exporter/MCP construction |
| `gateway_concurrency.go` | Per-target concurrency limiter + provider decoration (limiter innermost, circuit breaker outermost) |
| `gateway_circuitbreaker.go` | `cbProvider` — wraps a provider with its per-target circuit breaker on the routing paths (one breaker per `virtual_key`, shared by all four surfaces); panic-safe half-open probe release |
| `gateway_pipeline.go` | `routeTargets` — the one target walk chat, streaming, embeddings and images all take: retry, circuit breaker, concurrency, outcome recording |
| `gateway_retry.go` | `retryPolicyFor` — the single place `targets[].retry` is resolved; it does not consult the routing mode |
| `gateway_strategy.go` | `buildStrategy` — `StrategyMode` → `strategies.Strategy`; every error it returns is one `config.ValidateConfig` already rejects |
| `plugin/catalog.go` | `plugin.Builtins()` — the built-in plugin catalog served by `GET /admin/plugins/catalog`, so the dashboard keeps no copy of it |
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
| `providers/core/discovery.go` | `DiscoverOpenAICompatibleModels` / `DiscoverModelsWithHeaders` — live `/models` enumeration shared by OpenAI-compatible providers |
| `cmd/ferrogw/main.go` | HTTP server setup and entry point |
| `internal/admin/handlers/middleware.go` | Bearer token auth middleware — `AuthMiddlewareWithSessions` accepts either an API key or a dashboard session token |
| `internal/admin/repository/sessions.go` | `SessionStore` interface + `MemorySessionStore` (the `Session` type itself lives in `internal/admin/model`) — session lifetime (24h absolute / 1h idle), token hashing |
| `internal/admin/{model,repository,handlers}/` | Admin subsystem split into three packages: `model` (domain types + api-key/actor context kernel), `repository` (key/session/config/audit stores + migrations + config scrubbing), `handlers` (HTTP handlers, routes, auth middleware) |

---

## Architecture & Design Patterns

- **Strategy Pattern**: Routing strategies (`Single`, `Fallback`, `LoadBalance`, `LeastLatency`, `CostOptimized`, `Conditional`, `ContentBased`, `ABTest`) all implement `Strategy` interface in `internal/strategies/`
- **Self-Describing Factory**: Each provider has a `ProviderEntry` in `providers/providers_list.go` — no `main.go` changes needed to add a provider
- **Two-Mode Provider Init**: `ProviderConfigFromEnv` (OSS self-hosted) or direct `ProviderConfig` map (cloud/tenant credential injection)
- **Plugin Middleware**: `plugin/manager.go` runs plugins at `before_request`, `after_request`, `on_error` stages
- **OpenAI Compatibility**: All requests/responses match OpenAI spec — other provider responses are translated
- **Pass-Through Proxy**: Unhandled `/v1/*` endpoints forwarded transparently via `internal/proxy/proxy.go`
- **Compile-time assertions**: Every provider subpackage has `var _ core.XxxProvider = (*Provider)(nil)` guards
- **Observability seam**: `Gateway` holds exactly one `observability.Provider` (NoOp by default; install via `SetObservability`). `Route`/`RouteStream`/`Embed`/`GenerateImage` each open a `gateway.request` root span and stamp `gen_ai.*`/`ferro.*` attributes; plugins and MCP tool calls emit child spans. Registered exporters receive `gateway.request.completed`/`failed` events, and one `gateway.routing.attempt` event per physical provider call when they opt in through `observability.RoutingAttemptExporter`. With OTel disabled the hot path stays at the NoOp allocation baseline (asserted by `TestRoute_TracingOff_AllocBaseline`). `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` take precedence over the configured endpoint; every other tracing setting, the head sampler included, comes from config. The sampler is `ParentBased`, so an inbound sampled `traceparent` is followed whatever `sample_ratio` says.

### Request Flow

```sh
Client → HTTP Router → admitModel (404 here ends it; on_error still runs)
                       — skipped when a before_request transform is configured
  → before_request plugins → Strategy (target ORDER only)
  → routeTargets  (gateway_pipeline.go — one walk for all four surfaces)
        per target: retry → circuit breaker → concurrency limiter
          → Provider.Complete() / CompleteStream() / Embed() / GenerateImage()
  → after_request plugins  (on_error instead, when the walk failed)
  → Response
```

`routeTargets` in `gateway_pipeline.go` is the single walk chat, streaming,
embeddings and image generation all take, so retry, breaker, concurrency, error
classification, metrics and request logging cannot differ between them. The
strategy contributes target *order*; it no longer owns retry. Plugin stages sit
outside the walk, so a retry does not re-run them.

`admitModel` (`gateway_pipeline.go`) sits ahead of the plugin stages on every
routed surface, because that stage is not free: a rate limiter spends a token
and a budget spends money, so a model no target serves — which can never reach a
provider — must not be able to spend either on its way to its 404. It asks the
router's own candidacy test (`candidateLocked`, the whole of `resolveTarget`'s)
over every configured target, so it cannot drift from the walk's answer, and it
is deliberately wider than that walk: it refuses only when NO configured target
serves the model, leaving the narrower "the strategy offered none" case to the
walk, which answers identically. A refusal still runs `on_error`, so the request
is recorded exactly as a plugin denial is.

**A configured `before_request` transform turns the check off**, on every
surface, for the whole process. `Execute` may mutate the request it is handed,
and a transform is the plugin type whose job that is — so the model a request
arrives with is not yet the model that will be routed. A team-wide alias
rewritten to a real model id has no answer before the stage runs, and admitting
on the pre-plugin name answered 404 to requests the gateway had been serving.
The authority is the type the plugin **reports** from `Type()`, the same one the
fail-open policy reads, not `plugins[].type` in config.

The cost is real and one-directional: such a deployment gives up the drain
protection above, and unroutable models spend the limiter again exactly as they
did before the check existed. Correctness comes first — refusing a request a
plugin was about to make routable is a wrong answer, and quota protection does
not make a wrong answer acceptable. Running the check a **second** time after
the stage would recover nothing: it is wider than the walk that follows it, so
every model it would refuse the walk already refuses, with the same error and at
the same point — by which time the tokens are spent.

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
  - virtual_key: gemini
    models:                  # models this target serves, declared by the operator,
      - gemini-2.5-flash     # ADDED to the catalog and live discovery — see below

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
    endpoint: ""             # URL or host:port; a BASE endpoint (v1/traces is appended
                             # under http/protobuf, and NOT appended twice when the value
                             # already ends in it). Blank falls back to OTEL_EXPORTER_OTLP_*
    protocol: grpc           # grpc | http/protobuf  (https:// endpoint ⇒ TLS, else insecure)
    service_name: ferrogw
    sample_ratio: 1.0        # head sampler 0.0–1.0, wrapped in ParentBased
    privacy_level: metadata  # none | metadata (redacted, default) | full (raw error text)
    shutdown_grace: 10s      # max drain time for in-flight OTel exports on shutdown
  exporters:                 # plugin exporters receiving completed/failed events; none ship in-repo
    - name: langsmith
      enabled: false
      config: {}
```

### Declared models (`targets[].models`)

Three things can name a model for a target: the model catalog, live discovery
(`core.DiscoveryProvider` / `core.ConfiguredModelProvider`), and the operator.
`targets[].models` is the third, and it is the only one that needs no
provider-specific work — every target has the field, so a gap never waits on a
provider gaining a `/models` endpoint it may not have.

Use it when a target serves a model the automatic sources cannot see: an id
newer than the catalog, a preview or regional name, a self-hosted deployment
behind `<PROVIDER>_BASE_URL`, or a provider that enumerates nothing.

| Property | Behaviour |
|----------|-----------|
| Effect on routing | Joins the exact-match routing index alongside the catalog and discovery |
| Effect on `/v1/models` | Advertised — a declared model is listed as owned by its target |
| Additive or restrictive | **Additive only.** Declaring one model never hides the others a target serves |
| Already known to the catalog | Harmless no-op; sources are merged and de-duplicated. `/v1/models` publishes one entry per id, owned by the **first configured target** that serves it, carrying that target's catalog metadata — target order, not registration order, because the first is the operator's stated preference and the second is an implementation detail of `providers_list.go` |
| Wildcards | Rejected at load |
| Provider not registered | Declaration is inert, exactly as an unroutable target already is |

**It only adds.** There is deliberately no allowlist spelling: a target that must
serve less is a target you remove. That keeps one config line from silently
unrouting the forty models a target was already serving.

**Wildcards are a load error, not a pattern.** The routing index is an
exact-match map, so `gemini-*` would match nothing while reading as though it
matched everything. A provider whose upstream genuinely accepts ids nothing can
enumerate declares `core.AnyModelProvider` — on the provider, on purpose — and
is offered only the names no target owns.

Advertised models stay a subset of routable ones. `/v1/models` applies a
precedence between the three *inventory* sources (live discovery > catalog >
provider-configured), because those are rival descriptions of one inventory and
the best-informed should win outright. A declared list is not a rival
description — it is "this one as well" — so it survives whichever tier won
rather than competing with it. Routing takes the union of all four.

The listing also asks the strategy **and the pipeline**, so "advertised ⊆
routable" holds under every routing mode (`routingServes`, `gateway.go`). It
asks the two in the order routing asks them: `SelectTargets` names an order, and
`eligibleKeys` + `candidateLocked` — the walk's own code, not a copy of it —
decide which of those names a request may actually reach and whether one of them
serves the model.

Both halves are load-bearing, for different modes. Under `mode: cost-optimized`
with `unpriced_strategy: skip` the gateway serves only what the catalog prices,
so a declared model is not advertised — it is unpriced by construction, being
precisely the case no catalog knows — and neither is a catalog model its target
has no price for. Under `single`, `conditional` and `content-based` the walk
attempts the one target the mode names and no other, so a model owned only by a
*different* configured target is a 404 however many targets the config lists;
listing it was the same defect as listing a provider no target names, one level
in. A model's absence from `/v1/models` is the diagnostic that routing would
refuse it.

Under `content-based` the answer is representative rather than exact. Its rules
match on prompt content, which a model listing does not have and cannot be
given, so the listing reports what a request carrying no content gets: the
no-match fallback target. A model only a matched rule would reach is therefore
not advertised — the safe direction, and the only one available to an endpoint
that answers before any prompt exists. `conditional` has no such gap: its rules
match on the model, which is exactly what the listing asks about.

`ferrogw validate` rejects an empty entry, one carrying surrounding whitespace,
a wildcard, and a duplicate within one target. The same id on two different
targets is legal: that is how a model gets a fallback.

Misspelling the key itself is caught by the decoder rather than by validation,
and `PUT`/`POST /admin/config` decode as strictly as a config file does: an
unknown key is a 400 naming it, not a silent discard. That matters most for this
field — it is hand-written, no other source supplies it, and a config that
quietly lost it reports success and then routes as though the models had never
been declared.

### Target Routability and Readiness

A target is **routable** when a provider is registered under its `virtual_key`
and that provider's circuit is not open. Registration is driven by which
credentials the environment holds; routing is driven by which names the config
lists — so the two sets can be disjoint, and a config whose targets name no
registered provider fails 100% of traffic while every provider in the process is
healthy.

| Targets routable | Startup | `/readyz` |
|------------------|---------|-----------|
| all | silent | `200 ready` |
| some | `WARN`, naming the unroutable ones | `200 ready`; `targets[]` in the body marks them `routable: false` |
| none | `ERROR`, naming them | `503 not_ready`, reason `no routable targets` |

Startup **warns, it does not exit**. A provider is registered only when its
credential is present, so a target naming a provider whose key has not rolled out
yet is a legitimate config that starts serving the moment the secret lands;
refusing to boot would turn a staged credential rollout into a crash loop.
Readiness is the reversible lever instead, and it gates only on the total case:
one unroutable target among several is degraded — fallback and load-balance skip
it — and pulling an instance that still answers most requests out of rotation
makes the outage worse.

Known ceiling: routability does not ask the strategy which targets it would
actually select. Under `mode: single` only the first target is ever used, so a
config with a broken first target and a healthy second still reports ready.

`ferrogw validate` catches the typo before any of this: every `virtual_key`, and
every enabled plugin's name and stage, must resolve against what the binary knows
at build time. It deliberately does **not** check credentials — a pipeline runs
`validate` without secrets, so a target naming a real provider whose API key only
exists in production is valid and is reported as such.

### MCP Server Readiness (`mcp_servers[].required`)

`required` decides whether one MCP server's availability gates the whole
instance's readiness. It is **opt-in per server and defaults to `false`**, so an
existing config behaves exactly as it did before the field existed.

| `required` | Server unready | `/readyz` |
|------------|----------------|-----------|
| absent / `false` | reported in the body | `200 ready` |
| `true` | reported in the body | `503 not_ready`, reason `required mcp server unavailable` |

"Unready" is narrower than "unreachable" — read the two paragraphs below before
setting `required: true` on an HTTP server.

A server is unready when it has not completed the initialize handshake. That
covers a server whose transport could not be built at all — an unresolvable
`${VAR}` in its `headers` or `env` — which is reported unready rather than
omitted from the body. A server that completed the handshake and later lost its
transport also becomes unready, but only for the transports named below.

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
header, or a subprocess command line. It is logged server-side and served on
the bearer-authenticated `GET /admin/health` instead, where the caller has
already presented a credential carrying `read_only` or `admin`.

### Key Environment Variables

| Variable | Purpose |
|----------|---------|
| `MASTER_KEY` | Bootstrap and break-glass admin credential (use `ferrogw init` to generate) — see [Operator credentials](#operator-credentials) |
| `GATEWAY_CONFIG` | Path to config YAML/JSON |
| `GATEWAY_ENV` | Set to `production` to enable production-mode safety guards; unset or any other value is non-production mode. See [Production mode](#production-mode) for what it refuses and what it warns about |
| `PORT` | Server port (default: 8080) |
| `FERRO_MODEL_CATALOG_URL` | Override the model catalog source URL (used by `/v1/models` and model routing) |
| `FERRO_MODEL_CATALOG_TIMEOUT` | Go duration bounding the catalog fetch (default 10s). The fetch runs during startup, before the listener binds, so a blocked-egress deployment waits this long before falling back to the embedded catalog. Set `0` to skip the remote fetch entirely |
| `FERRO_MODEL_DISCOVERY_INTERVAL` | Opt-in interval (Go duration, e.g. 6h) to live-refresh model lists from provider /models endpoints; unset disables |
| `ALLOW_UNAUTHENTICATED_PROXY` | Set to `true` to disable proxy-route auth (dev/local only; blocked when `GATEWAY_ENV=production`) |
| `API_KEY_STORE_BACKEND` / `API_KEY_STORE_DSN` | Admin key and dashboard-session store: `memory` (default), `sqlite`, or `postgres`, plus its DSN |
| `CONFIG_STORE_BACKEND` / `CONFIG_STORE_DSN` | Config-history store; same backends, same default |
| `REQUEST_LOG_STORE_BACKEND` / `REQUEST_LOG_STORE_DSN` | Request-log store: `sqlite` or `postgres`. Unset disables persistence, so the request-logger plugin's `persist` has nothing to write to |
| `ENABLE_PPROF` | Set to `true` to mount `/debug/pprof/*` behind the same auth as `/metrics`; warned about under `GATEWAY_ENV=production` |
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
| `FERRO_OLLAMA_MODELS` | Comma-separated Ollama model list. Replaces `OLLAMA_MODELS` — see [Ollama model list](#ollama-model-list-ferro_ollama_models) |
| `OLLAMA_MODELS` | **Deprecated**, read for one more release. It is Ollama's own variable for the models *directory*, so on a host running Ollama it carries a filesystem path — which the gateway drops rather than register as a model id |
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
| `<PROVIDER>_BASE_URL` | Overrides a provider's API root (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `GROQ_BASE_URL`, … — 23 providers declare one <!-- drift-ok: subset — 23 of the 30 providers declare a base-URL override -->). Used verbatim, version segment included: write `https://api.groq.com/openai/v1`, not `.../openai`. A value with no path at all resolves to the provider's own version segment. See [Provider base URLs](#provider-base-urls) |
| `CORS_ORIGINS` | Comma-separated allowed CORS origins. Matched **literally** against the request's `Origin` header — there is no wildcard, so `CORS_ORIGINS='*'` allows nothing a browser would ever send. List each origin explicitly. A `*` entry is a startup warning, and under `GATEWAY_ENV=production` a startup error. An unlisted origin is denied by the **absence of `Access-Control-Allow-Origin`**, not by a refused preflight — see [CORS preflight](#cors-preflight) |
| `TRUSTED_PROXIES` | Comma-separated CIDRs of trusted reverse proxies; `X-Forwarded-For`/`X-Real-IP` is honored only from these (default: loopback) |
| `RATE_LIMIT_RPS` | Per-IP rate limit requests/sec; enabled by default (20 rps / burst 40). Set to `0` to disable — note the `rate-limit` **plugin**'s `requests_per_second` reads `0` the opposite way, as a rate this gateway cannot serve — it is rejected at load, and the plugin is turned off with `enabled: false` instead. Setting this alone resets burst to the default 40 too — pair with `RATE_LIMIT_BURST` for a custom rate/burst combination. Keys on the resolved client IP, so `TRUSTED_PROXIES` must list the real proxy CIDR or all traffic behind an untrusted proxy shares one bucket |
| `RATE_LIMIT_BURST` | Per-IP burst capacity override (default: 40) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector **base** endpoint. Setting it alone turns tracing on, and it takes precedence over `observability.tracing.endpoint` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Signal-specific OTLP traces endpoint, used verbatim, and it outranks the variable above. Setting either variable turns tracing on |

Those two are the only `OTEL_*` variables the gateway itself reads. Once the
pipeline is active and either is set, its value is handed to the OTel SDK
unread, so the specification's own path rules apply — the base endpoint gets
`v1/traces` appended, the signal-specific one does not. The head sampler is
built from `observability.tracing.sample_ratio` alone, so `OTEL_TRACES_SAMPLER`
has no effect. (`OTEL_EXPORTER_OTLP_HEADERS` reaches the exporter through the
SDK, not through the gateway.)

### Ollama model list (`FERRO_OLLAMA_MODELS`)

`OLLAMA_MODELS` is **Ollama's own** environment variable, and it names the
models *directory* — `$HOME/.ollama/models` by default. This gateway read it as
a comma-separated model **list**, so on the one host where both meanings are
live at once — a host running Ollama — the same variable meant two things and
the directory path was registered as a single bogus model id that `/v1/models`
then advertised.

`FERRO_OLLAMA_MODELS` is the replacement, matching the `FERRO_` prefix the
gateway's own variables already use and the non-colliding `OLLAMA_CLOUD_MODELS`
one provider along.

| Situation | Behaviour |
|-----------|-----------|
| Only `FERRO_OLLAMA_MODELS` set | Used |
| Only `OLLAMA_MODELS` set | Used, with a startup `WARN`. **Removed next release** |
| Both set | `FERRO_OLLAMA_MODELS` wins |
| The value is a filesystem path (`/…`, `~…`, `./…`, `C:\…`) with no comma | Dropped, with a `WARN` naming the replacement. The provider behaves as it does with no list configured |

The drop is the guard that matters: renaming alone would have failed *silently*,
because ollama declares `core.AnyModelProvider` — routing keeps working and only
`/v1/models` quietly narrows to the one path-shaped entry.

Neither variable is required. Ollama serves whatever the operator pulled onto
it, which is why the provider serves any model; a list only narrows what
`/v1/models` advertises. For a config-file deployment, `targets[].models`
([Declared models](#declared-models-targetsmodels)) does the same job
provider-agnostically and is the preferred spelling.

### Production mode

`GATEWAY_ENV=production` turns on a fixed set of startup checks. They split two
ways, and the split is the rule to follow when adding one.

**Refused — the gateway exits rather than serve.** Reserved for a setting that
cannot become correct on its own and cannot do what the operator asked. All
refusals are reported together, so fixing a deployment is one edit rather than a
restart per problem.

| Setting | Why it is refused |
|---------|-------------------|
| `ALLOW_UNAUTHENTICATED_PROXY=true` | Every `/v1/*` data-plane endpoint would be unauthenticated |
| `CORS_ORIGINS` containing `*` | Matched literally, so it allows *no* cross-origin request while reading as though it allows all of them. Outside production the same value is honoured with a warning |

**Warned — logged at WARN, startup continues.** Each of these is a defensible
deployment choice, and refusing to boot over one turns a deploy into a crash
loop, which is the bigger outage.

| Setting | Why it is only a warning |
|---------|--------------------------|
| `RATE_LIMIT_RPS=0` | Enforcing request rates at an ingress or upstream API gateway instead is a normal topology |
| `ENABLE_PPROF=true` | This is how a production incident gets diagnosed. The routes require the `admin` scope; the risk is leaving them mounted after the investigation |
| In-memory API key store | The documented default. It serves correctly until the process restarts, at which point operator keys, dashboard sessions and the audit trail are gone and `MASTER_KEY` is the only way back in |

Note what production mode does **not** change: `/health` stays unauthenticated
and keeps reporting provider names, model counts and circuit state. That payload
is load-bearing for orchestrators and for the dashboard (see [Reading circuit
state](#reading-circuit-state)), so it is an inventory disclosure to keep off
the public internet by deployment, not a payload to trim.

### CORS preflight

A cross-origin request is denied by **withholding
`Access-Control-Allow-Origin`**, never by refusing the preflight. Those are
different denials, and only the first is the one a browser enforces.

An `OPTIONS` request carrying an `Origin` — the shape only a preflight has — is
answered by the CORS layer, ahead of authentication and ahead of the per-route
method guard, whether or not the origin is allowed:

| Origin | Preflight response |
|--------|--------------------|
| on `CORS_ORIGINS` | `204` with the `Access-Control-*` headers |
| not on it, or no allowlist configured | `204` with none of them — the browser blocks the request that would have followed |

Answering it anywhere lower gives the wrong answer. A preflight carries no
credentials, because browsers do not send `Authorization` on one, so
authentication refuses it `401`; the route's method guard serves `POST`, so it
refuses it `405 Allow: POST`. Neither describes what was decided, and the `405`
is not true of the resource — the same route answers `OPTIONS` with `204` as
soon as the caller's origin is listed. Whether a preflight succeeds turns on the
request's `Origin`, which is not a property of the resource and so is not
something `Allow` can express.

The distinction is what an operator sees when a browser application cannot reach
the gateway. A non-2xx preflight makes the browser report a transport-shaped
failure — the status, or a missing method — and sends the reader looking at
routing or credentials. A `204` with no `Access-Control-Allow-Origin` makes it
report the missing header, which names `CORS_ORIGINS`.

`OPTIONS` **without** an `Origin` is not a preflight and is left alone: that one
really is a question about the resource's methods, so the route answers it with
its own `405` and an accurate `Allow`.

### Provider base URLs

Every provider that talks HTTP declares a `<PROVIDER>_BASE_URL` override —
`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `GROQ_BASE_URL` and 20 more, all
listed as `CfgKeyBaseURL` entries in `providers/providers_list.go`. It points
the provider at a proxy, a self-hosted compatible server, or a regional
endpoint.

**Write the API root exactly as the vendor documents it, version segment
included.** That is the whole rule, and it is the same rule on every provider:
the value is used verbatim, and each surface appends only its operation path.
So `https://proxy.example.com/v1` reaches `/v1/chat/completions` and
`/v1/embeddings`, and `https://host/openai` reaches `/openai/chat/completions`.
It is the contract openai-python, openai-node, LiteLLM and Portkey all define
for `base_url`.

Omitting the version segment is the one mistake with a net under it: a base
carrying **no path at all** is not an API root, so it resolves to the segment
the provider's own default root ends in — `/v1` for an OpenAI-wire provider,
`/v1beta` for gemini. `http://host:9901` and `http://host:9901/v1` are
therefore the same thing. The net does not extend to a base that carries any
path: `https://host/openai` is the operator's own root and is taken as written,
so a proxy mounted at `/openai/v1` and one that really does serve the API at
`/openai` are both expressible.

Userinfo travels with the host, so
`https://user:pass@proxy.example.com` reaches the proxy authenticated — the
credential survives the resolution above rather than being dropped on the way
through. A query string or fragment is refused at startup instead: an operation
path is appended to whatever the root resolves to, so `https://host/v1?a=b`
would ask for `/v1` with the operation buried in a query value. No reading of
such a base produces the request that was meant, and silently deleting the part
that cannot work is the same defect one step quieter.

`core.ResolveAPIRoot` is where this happens, and it happens **once, when the
provider is constructed**. A provider stores the one resolved root and no
request-time code re-reads it. That is what makes the rule hold across surfaces
rather than per endpoint: chat, streaming, embeddings, images, model discovery
and the `/v1/*` pass-through all build from the same string.

Three providers are deliberately outside the rule, because the value they are
configured with is not an API root. All three are listed in
`hostRootProviders()` (`providers/base_url_convention_test.go`) with the reason:

| Provider | Why | Write the base as |
|----------|-----|-------------------|
| `cohere` | The two surfaces the gateway uses sit on different versions: chat on `/v2/chat`, embeddings on `/v1/embed`. Cohere *does* serve `/v2/embed`, so one root is reachable — but moving embeddings onto it is a response-schema migration, not a URL edit | the host, `https://api.cohere.com` |
| `ollama` | `OLLAMA_HOST` is Ollama's own variable for the *server* root, and one server mounts two — the OpenAI-compatible surface at `/v1` and the native API at `/api` | the server URL, `http://localhost:11434` |
| `azure-foundry` | `AZURE_FOUNDRY_ENDPOINT` is the Azure resource host the portal shows, and the surface path under it is Azure's fixed shape, not a mount point. The GA OpenAI-compatible route is `/openai/v1`, so the version segment sits mid-path behind a vendor prefix | the resource host, `https://<resource>.services.ai.azure.com` |

`DATABRICKS_HOST`, `AZURE_OPENAI_ENDPOINT` and `HUGGING_FACE_ENDPOINT` are named
for what they are, too: a resource host, from which the provider builds the
vendor's fixed surface path.

Two guards hold the rule, and both fail on any provider that reintroduces the
defect. `TestBaseURLIsResolvedOnlyAtConstruction` fails on a base URL inspected
outside a constructor, so no provider can branch on its shape at request time.
`TestBaseURLGainsNoVersionSegmentAtRequestTime` fails on a version segment
appended to a stored base — the drift that a no-branching rule alone permits,
because appending `/v1` unconditionally is perfectly consistent within one
provider and still makes the variable mean something different than it does on
the provider next door. `TestBaseURLRootIsUsedVerbatim` then asserts the same
thing from the outside, on the paths that actually leave the process.

Both read **every segment** of the appended path, not just the first. A version
segment reached by way of a vendor prefix — `/openai/v1/chat/completions` —
doubles for an operator who wrote the `/v1` exactly as a leading one does, and a
guard that stops at the head segment reports that path clean. The allowlist is
checked in both directions: an entry whose provider stops appending a version
segment fails until the entry is removed, which is also what fails if either
scan is narrowed back to the head segment.

---

## HTTP API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | The dashboard SPA, embedded in the binary and served from every unmatched path |
| `/health` | GET | Health check — per-provider status and circuit state (see [Reading circuit state](#reading-circuit-state)) |
| `/livez` | GET | Liveness — the process is up |
| `/readyz` | GET | Readiness — the gateway can serve traffic |
| `/v1/models` | GET | List all available models |
| `/v1/capabilities` | GET | Per-provider parameter support, serialized from the capability matrix. Covers the providers a configured target names — the same set `/v1/models` answers from, so a client cannot be told a provider forwards a parameter and then get a 404 from every surface |
| `/v1/chat/completions` | POST | Chat completion (supports `stream: true`) |
| `/v1/completions` | POST | Legacy text completion — served through the gateway as a single-message chat completion (see [Legacy completions](#legacy-completions)) |
| `/v1/embeddings` | POST | Embeddings |
| `/v1/images/generations` | POST | Image generation |
| `/v1/audio/transcriptions`, `/v1/audio/translations` | POST | Speech-to-text (multipart upload, 25 MiB cap) |
| `/v1/audio/speech` | POST | Text-to-speech (JSON in, binary audio out) |
| `/v1/rerank` | POST | Rerank (Cohere-v2 contract) |
| `/v1/moderations` | POST | Moderations (OpenAI contract) |
| `/v1/files`, `/v1/files/*`, `/v1/batches`, `/v1/batches/*` | GET, POST, DELETE | Files + Batch pass-through to the configured `batch_target` (501 when unset — see [Batch and files](#batch-and-files)) |
| `/v1/responses` | POST | Responses API — model-routed, governed and **priced** (usage teed off the response/stream); see [Responses](#responses) |
| `/v1/responses/*` | GET, POST, DELETE | Responses id sub-routes (retrieve/delete/cancel/input_items) → `responses_target` (501 when unset) |
| `/v1/*` | Any | Pass-through proxy to provider |
| `/admin/keys` | GET, POST | API key management (requires auth) |
| `/admin/session` | POST | Exchange an API key or `MASTER_KEY` for a short-lived dashboard session token (unauthenticated route — the credential itself is the auth) |
| `/admin/session` | DELETE | Log out — deletes the caller's own session row server-side |
| `/admin/sessions` | GET, DELETE | List active dashboard sessions; `DELETE` signs every operator out at once |
| `/admin/sessions/{id}` | DELETE | Revoke one session, so a single lost device does not cost every operator their sign-in. Idempotent |
| `/admin/audit` | GET | Read the audit trail — filters: `action`, `actor_id`, `outcome`, `since`, plus `limit`/`offset` |
| `/admin/plugins` | GET | The plugins this instance has **configured**, read from the live config |
| `/admin/plugins/catalog` | GET | The plugins this **build ships** — name, type, summary, settings, fail-open. Fixed for the process lifetime. The dashboard reads it instead of keeping its own copy, so a card can no longer name a setting that does not exist |
| `/admin/logs` | GET, DELETE | Request log. Defaults to **one row per request** (terminal stages only); the logger still writes one row per plugin stage, so pass `stage=all` for the raw event stream or `stage=<name>` for one stage. Filters: `model`, `provider`, `since`, and `api_key_id` (see [Filtering the request log by credential](#filtering-the-request-log-by-credential)) |
| `/metrics` | GET | Prometheus metrics. Requires a bearer token — an API key or a dashboard session — carrying the `read_only` or `admin` scope |
| `/debug/vars` | GET | expvar. `/debug/pprof/*` joins it when `ENABLE_PPROF` is set. Everything under `/debug` requires the `admin` scope: a profile is a memory image that can hold request bodies and credentials, and expvar publishes the process command line |
| `/admin/*` | Mixed | Admin dashboard, usage stats, request logs, config history/rollback (see `internal/admin/handlers/`) |

### Reading circuit state

`/health` and `/readyz` report `circuit: "closed"` for three different
situations: a target whose breaker is closed, a target with **no breaker
configured**, and a registered provider **no target names at all**. That is
deliberate — the field answers "would a call be admitted", and for a provider
with no breaker the answer is the same yes a closed breaker gives — but it means
`/health` alone cannot tell you whether a breaker exists.

`gateway_circuit_breaker_state{provider="<target>"}` answers that instead: a
series exists for exactly the targets that have a breaker, from the moment it is
configured and before the target has served anything, so **absent means no
breaker is configured**. Resolve "is this target protected?" there, not from
`/health`. It is also resolved at scrape time from the live breaker rather than
written from the last request outcome, so it follows Open→HalfOpen with no
traffic.

### Circuit-breaker blast radius: one breaker per target, all four surfaces

**A breaker is scoped to a `virtual_key`, not to an endpoint.** Chat, streaming,
embeddings and image generation to one target all count into and are all refused
by the same breaker, because there is exactly one per configured target.

That is what makes a **partially** broken upstream take down the surfaces that
still work. An endpoint that fails only `/v1/embeddings` — a `301` to a moved
path, a decommissioned route, a per-surface auth failure — records
`failure_threshold` failures like any other, and the moment the circuit opens
that target stops serving **chat** too, having never failed a chat request.

What happens next depends on whether another target serves the model:

| Other targets serve the model | Behaviour once the circuit opens |
|-------------------------------|----------------------------------|
| yes | the open target is skipped during selection, under **every** routing mode that offers a sibling, and traffic silently moves to it — including under `conditional` and `content-based`, where a rule named one target on purpose. `single` offers no sibling: its strategy selects exactly one target, so an open circuit there is refused with `503`, whatever else the config lists |
| no | the walk attempts it anyway (the filter fails open rather than answering a 404 for a model that plainly exists), the breaker refuses, and the caller gets `503 upstream_unavailable` |

So the symptom an operator sees on the chat surface is either a routing shift
they did not configure or a 503, with nothing in the chat traffic to explain it.
The chain to follow is `gateway_circuit_breaker_state{provider="<target>"}` →
`gateway_provider_errors_total{provider="<target>",error_type="provider_error"}`
→ `GET /admin/logs?provider=<target>` to see **which** surface's requests were
failing. Both `/health` and `/readyz` report the target as `circuit: "open"`, and
`/readyz` marks it `routable: false`.

Not everything counts toward opening it. A `429`, a client disconnect, a
caller-supplied deadline, an unsupported-parameter rejection and a shed under
`targets[].concurrency` are all excluded — none is evidence the upstream is
unhealthy. A redirect, a `5xx`, a connection failure, and the gateway's own
`request_timeout` or stream idle bound elapsing all do count.

There are no per-surface breakers. If one surface of a target is expected to be
unavailable, give it its own target rather than relying on the breaker to keep
the distinction.

### Failover: which modes carry a request past a dead target

A breaker is not what produces failover, and a deployment without one still
fails over. The two do different jobs, and conflating them cost a default
config its failover entirely: the breaker makes failover **cheap**, the routing
mode makes it **happen**.

Modes split by what their leading candidate *means*
(`advancesPastFailure`, `gateway_pipeline.go`):

| | Modes | On a failure |
|---|---|---|
| **Pool** | `fallback`, `loadbalance`, `least-latency`, `cost-optimized`, `ab-test` | the walk advances only after a failover-safe failure |
| **Named** | `single`, `conditional`, `content-based` | the walk stops and reports the failure |

A pool mode picks its head for a reason that is about the pool rather than
about that target — spread the load, take the cheapest, take the fastest, split
the traffic — from targets the operator declared interchangeable. So the request
belongs to the pool, and carrying it to a sibling is what was asked for.

Failover-safe failures are transport or no-status failures, an attempt that
timed out waiting on the target, 408, 429, 5xx, open circuits, and target
saturation. The request's own cancellation or deadline and every other 4xx stop
at the current target.

A named mode picks its head because something named that target specifically:
`single` names it, and a `conditional` or `content-based` rule matched it.
Serving from somebody else would demote the rule to a suggestion, so these
report the failure instead. They still move off a target whose **circuit is
open**, because that is a target the gateway has already decided not to call —
see the table above; the two rules are independent.

Without a breaker the walk pays the dead target's connection timeout on every
request before advancing. That is the cost the breaker removes, and the reason
to configure one on a target that can fail — not to obtain failover, which the
pool modes provide on every failover-safe failure.

### Filtering the request log by credential

`GET /admin/logs?api_key_id=<id>` narrows the log to the rows a single
credential was served under. The value is the opaque id the row carries, matched
exactly and never checked against the key store — a deleted or revoked key keeps
its rows, and that is the case the question is most often asked about.

`api_key_id=none` selects the rows that name **no** credential. Two different
facts land there: an unauthenticated request records an empty value, and a row
written before the column existed holds NULL. Storage keeps them apart and the
filter deliberately folds them, because both answer "this row cannot be
attributed" — and a filter matching only one of them would leave rows visible
that no filter value selects.

An empty `api_key_id=` is read as absent, as it is for `model`, `provider` and
`stage`, so a client that always sets the parameter does not silently narrow the
listing. `DELETE /admin/logs` takes no `api_key_id`; a purge is scoped by
`stage`, `model` and `provider` alone.

The dashboard's Request Logs page resolves the recorded id to the key's **name**
through `GET /admin/keys`, marks a key revoked or expired since it served the
traffic, and falls back to the recorded id when the store can no longer name one
— a deleted key, or the synthetic id the master credential carries. The
credential itself is never displayed.

### What `GET /admin/config` serves from a free-form map

A named field of the config schema — `virtual_key`, `mode`, `command`, `url` —
is served with its shape intact and only its secret parts removed. A **free-form
map**, the operator-supplied surface the gateway hands to something else, is
withheld key by key and shown only where the receiver declares that key a
setting. **An unrecognised key is withheld.**

| Map | Shown |
|-----|-------|
| `plugins[].config` | the keys that plugin's catalog entry declares (`GET /admin/plugins/catalog`), and no others |
| `plugins[].config` of a plugin registered out of tree | nothing — the gateway does not know its schema |
| `aliases` | everything: the gateway resolves it itself and both halves of an entry are model names |
| `mcp_servers[].env`, `mcp_servers[].headers`, `observability.exporters[].config`, `observability.tracing.headers` | nothing — these carry transport credentials and declare no schema |
| any map field added later | nothing, until something declares it |

A shown key is still run through the value rules, so a credential inlined under
a declared setting is caught on its shape.

**Withholding removes the key, not just the value.** A credential can be inlined
in either position, and an env or header *name* is deployment topology in its own
right — so a withheld entry comes back as `[REDACTED_KEY_<n>]`, indexed over the
sorted original names so the response is stable across calls. The count of
withheld settings survives; none of their names does.

`${VAR}` references are still served as written in a withheld entry's **value**.
They name a value rather than carrying one, and the stored-literal warning
decides whether an operator inlined a credential by asking what scrubbing
changed — replacing references would make it fire on every config that took the
advice it gives. The key they sat under is withheld regardless, so a reference is
no longer attributable to the setting it configures.

The cost lands on the config editor: a **withheld map no longer round-trips**.
Its keys are gone, so a `GET` body cannot be edited and `PUT` back — `PUT` refuses
it, because the placeholder key carries the redaction marker the write guard
already looks for. Edit those maps from the config file. A map whose keys are
*shown* — `aliases`, and a plugin's declared settings — round-trips exactly as
before.

The direction is the point. The rule this replaced withheld a value when its key
matched a vocabulary of credential-ish fragments and served it otherwise, which
fails expensively — `pat`, `sid`, `jwt`, `hmac`, `otp`, `dsn`, `account_sid` and
every non-English spelling name a credential, match no fragment, and were served
in plaintext to any caller holding a `read_only` key. Withholding by default
costs at most the legibility of a setting nobody declared, and it needs no
vocabulary to keep up to date. `plugin.Builtins()` is the authority for the one
allowlist that exists, and `plugin/catalog_test.go` holds each entry's
`Settings` equal to the keys that plugin actually reads — so the allowlist
cannot drift from the code it describes.

The cost lands on the config editor: a withheld value comes back as
`[REDACTED]`, and `PUT /admin/config` refuses a body carrying that marker
anywhere rather than overwrite a live credential with the placeholder text.
Replace it with the real value or a `${VAR}` reference before saving.

### Attribution headers

Every routed surface — `/v1/chat/completions` (streamed or not),
`/v1/completions`, `/v1/embeddings`, `/v1/images/generations`, `/v1/rerank`,
`/v1/moderations`, `/v1/audio/transcriptions`, `/v1/audio/translations` and
`/v1/audio/speech` — answers with four headers naming the target that served
it, or on failure the last one attempted:

| Header | Value |
|--------|-------|
| `X-Gateway-Provider` | the serving target's canonical provider (`openai`) |
| `X-Gateway-Target` | the target key as configured: `targets[].virtual_key` |
| `X-Gateway-Model` | the upstream model sent to the provider, after `model_map` |
| `X-Gateway-Attempts` | routing-layer attempts for the request: provider calls plus local breaker/concurrency refusals, retries and failovers included |

On a stream they are written before the first chunk, because the pipeline has
finished choosing by then. A request refused before any target was attempted —
a plugin denial, a model nothing serves — carries none. The value is never a
credential: the target key is the config string, not the key it names. An
embedder reads the same data by passing a `*aigateway.RoutingAttribution`
through `aigateway.WithRoutingAttribution` on the request context. The
pass-through proxy (`/v1/*`) keeps emitting `X-Gateway-Provider` only.

### Legacy completions

`/v1/completions` wraps the prompt as a single user message and routes it through
the same `Gateway` entry point `/v1/chat/completions` uses, so targets, aliases,
the routing strategy, plugins, circuit breakers, per-target concurrency limits,
metrics, and request logging all apply. The chat response is re-wrapped in the
legacy envelope.

There is no pass-through to a provider's own completions route: an opaque body
forwarded upstream carries none of the above — a guardrail cannot read it, a
budget cannot bill it, and a provider excluded from `targets` is reachable
through it. What the chat surface cannot express is refused with a 400 rather
than forwarded: `stream: true`, batch prompts, and token-id prompts.

`echo` is **honoured**: the prompt precedes the completion, per choice, so with
`n > 1` each candidate is echoed because each is its own completion
(`internal/handler/completions.go`). `best_of`, `logprobs`, and `suffix` are
accepted and ignored — `suffix` because it is a fill-in-the-middle generation
constraint rather than an append, and the chat surface cannot express it.

### Batch and files

`/v1/files*` and `/v1/batches*` are a **transparent pass-through to a single
configured backend**, `batch_target` (a `targets[].virtual_key`). Unlike every
routed surface, these carry no `model`: a batch references an uploaded
`input_file_id` and the model lives per-line inside the JSONL, and a bare
`GET /v1/files/{id}` or `GET /v1/batches/{id}` is an opaque, provider-scoped id
with no routing hint. So the gateway's model→target routing does not apply, and
a single backend serves the whole surface — which is exactly what a batch flow
needs, because the file, the batch that references it, and the output file it
produces all live on the same upstream. The ids stay **native** (not rewritten),
so a follow-up call resolves against that backend with **zero gateway state**;
nothing has to survive a restart, and the in-memory store default is untouched.

The forwarder (`internal/proxy/batch.go`, `proxy.BatchHandler`) reuses the
`/v1/*` proxy's security machinery — path-traversal refusal before any credential
is installed, response-credential redaction, and the streaming idle bound — and
strips the gateway's `/v1` before appending the operation beneath the provider's
batch root. A file upload streams straight through, so these routes sit **outside
the shared request-body limit** (a batch input file is far larger than a chat
body). The gateway credential replaces the client's on the way upstream.

The backend is named by a provider's `core.BatchProvider` seam — `BatchBaseURL`
(the root `/files` and `/batches` hang beneath) and `BatchAuthHeaders`. For most
providers that is their OpenAI-compatible base with bearer auth; **azure-openai**
serves batch on the resource's `/openai/v1` root (a different root than its
deployment-path chat surface) with the `api-key` header, resolved once in `New`.
`openai`, `azure-openai`, `groq`, `novita` and `qwen` declare `CapabilityBatch`.

When no `batch_target` is configured — or it names a target whose provider is not
batch-capable — every route answers **501**. `ferrogw validate` rejects a
`batch_target` that names no configured target. The six providers with a *native*
(non-OpenAI) batch API — mistral (`/v1/batch/jobs`), together, anthropic, gemini,
vertex-ai, bedrock — are deliberately out of this surface; each needs a bespoke
job adapter.

### Responses

`POST /v1/responses` is a **governed, priced pass-through**, not a re-modeled
schema. It carries a model, so it routes exactly like chat — through the full
`RoutePassthrough` pipeline (plugins, guardrails, circuit breaker, per-target
concurrency, request log). It differs from the generic `/v1/*` proxy in one way:
it is **priced**. The Responses API returns a `usage` object — on the JSON body,
or on the terminal `response.completed` / `response.incomplete` SSE event (there
is no `[DONE]` sentinel) — and the forward tees it out of the response as it
streams through, without altering a byte, feeding `Gateway.RouteResponses` →
`priceSurface`. So catalog pricing, the request-log cost column, the span cost and
the completed event all light up, where every other pass-through records
*unpriced*. The tee (`internal/proxy/responses_usage.go`) scans the SSE stream
line by line and never buffers more than one frame.

The body is forwarded **verbatim** — the gateway parses only three schema-stable
fields from it: `model` (routing), `max_output_tokens` (surfaced as the
completion ceiling so a max-token guardrail governs Responses — chat's field is
`max_tokens`, renamed here), and the string projection every content guardrail
already reads (`input`/`instructions` land in it). Re-serializing the ~40-event
Responses schema would be maintenance debt both LiteLLM and Portkey avoid.

The stateful **id sub-routes** — `GET`/`DELETE /v1/responses/{id}`,
`POST /v1/responses/{id}/cancel`, `GET /v1/responses/{id}/input_items` — carry no
model and reference an opaque, provider-scoped response id, so they pin to a
single `responses_target` (the Files/Batches pattern; native ids, zero state).
They answer **501** when `responses_target` is unset; create is unaffected. `openai`
and `xai` both serve the OpenAI Responses contract byte-compatibly; a
`NonOpenAIWire` provider is refused **501** on this surface.

### Completion length: `max_tokens` and `max_completion_tokens`

A request can express its completion ceiling in either field. Every gateway
entry point resolves the pair to **one value carried by both**, before any
plugin runs, using `core.Request.EffectiveMaxTokens`:
**`max_completion_tokens` supersedes `max_tokens` when both are present**, the
same precedence the OpenAI API applies (`max_tokens` is deprecated in its
favour, and o-series models accept only the newer field).

Resolving matters because which field travels is a *provider* decision — OpenAI,
Azure OpenAI, Azure AI Foundry, Groq and Cerebras send **only**
`max_completion_tokens` (`PreferCompletionTokens`), while others forward
`max_tokens`. While the two
could disagree, the ceiling a request actually imposed depended on which provider
it landed on, and a guardrail reading one field could be handed the other: a
request carrying `max_tokens: 5` with `max_completion_tokens: 500000` passed a
`max-token` cap of 10 and then had 500000 forwarded upstream.

A guardrail must therefore read `EffectiveMaxTokens`, never `Request.MaxTokens`
— that is what `plugin/maxtoken` does, so the value it approves is the value
that travels.

On the five OpenAI-surface providers, a request that sets **only** `max_tokens`
is **promoted** to `max_completion_tokens` (same ceiling, `max_tokens` dropped),
because their o-series / GPT-5 models reject `max_tokens` outright and
`max_completion_tokens` is accepted by every chat model there — so the field most
OpenAI SDKs still default to does not 400 the moment it routes to a reasoning
model. Nothing is invented when the caller set no limit at all. The *global*
request the guardrails and cache key read is unchanged — it is reconciled by
`NormalizeCompletionTokenLimits`, which still leaves a `max_tokens`-only request
as it found it; only the provider's outbound body is rewritten.

---

## Operator credentials

**Give each operator their own admin-scoped key. Keep `MASTER_KEY` for
bootstrap and break-glass.**

The gateway has no user accounts — a key *is* the password. That makes the key
the unit of identity, so sharing one between operators costs both things a
credential is for:

- **Offboarding.** A shared key has to be rotated and redistributed to everyone
  when one person leaves. A per-operator key is revoked on its own, disturbing
  nobody else.
- **Attribution.** Config versions and credential changes record the acting
  credential. If everyone presents the same key, every record names it and
  answers nothing.

Create one per operator via `POST /admin/keys` or the dashboard, named for the
person or the device. `MASTER_KEY` is then the credential that creates the
first one and the way back in when every stored key is lost — not a daily
login. It has no database row, so unlike a stored key it cannot be revoked or
expired without restarting the process.

Two things worth knowing before relying on this:

- Credential changes, sign-ins (accepted and denied), and log purges are written
  to the durable `audit_log` table *and* logged. The store follows the key
  store's backend (`internal/admin/repository/audit_log.go`): the in-memory default keeps
  only the most recent actions and does not survive a restart, so a deployment
  that needs the full history configures a SQL backend. The write is best-effort
  and never fails the audited action — a down audit database must not break key
  management — so on a persistently failing store the log line is the record.
  `GET /admin/audit` reads the trail back, and the dashboard's Audit page is
  built on it. Both are bounded by whatever the configured store retains, so a
  deployment that keeps the in-memory default can still only answer questions
  about the recent past and only until the next restart.
- `GET /admin/config/history` serves the **durable** trail whenever a config
  store is configured: `configVersions` (`internal/admin/handlers/config.go`)
  prefers `LoadHistory` and falls back to the in-memory list only when the
  manager keeps no persistent store. The fallback is what resets on restart, and
  only under it is version 1 something other than the first recorded change. The
  actor is recorded durably in `config_history` either way.

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

1. Create `providers/<id>/<id>.go` (package `<id>`) — implement `core.Provider` and any optional interfaces (`core.StreamProvider`, etc.). **A provider does not report a model list**: `core.Provider` is `Name`/`Complete`/`SupportsModel`, and which models it serves comes from the catalog plus `core.DiscoveryProvider`. Implement `core.ConfiguredModelProvider` only when the model set comes from this instance's own config and no catalog can know it — an Azure deployment name, a `FERRO_OLLAMA_MODELS` entry. Add compile-time assertions:
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
8. Add the provider's env var(s) (commented out) to `deploy/compose.yaml`.

## Adding a New Plugin

**Deny with `pctx.Reject` + `pctx.Reason`, and return `nil`.** Return an error only when the plugin itself failed — an error means "I broke", and for every type except logging and metrics it aborts the request as a 500. See the `plugin` package docs.

1. Create `plugin/<name>/<name>.go` (package `<name>`) implementing `plugin.Plugin`.
2. Register a factory via `plugin.RegisterFactory("my-plugin", ...)` in an `init()` function.
3. Add a blank import in `cmd/ferrogw/main.go`: `_ "github.com/ferro-labs/ai-gateway/plugin/<name>"`

**A plugin whose `Execute` branches on the stage — checking before the request and
recording after it, as `response-cache`, `budget` and `request-logger` do — needs one
config entry per stage.** One entry registers one stage, and the entries **must** carry
identical config so they resolve to the same instance and therefore the same state.
That is enforced, not advised: `validateMultiStagePlugins` (`gateway.go`) rejects
disagreeing entries at gateway construction and **startup fails**, because two
instances that never see each other's state both log "plugin registered" and neither
does its job. Instance identity is `name` plus the JSON encoding of the whole
`config` block — the same `store_id` with any other key differing is still two
configs. `ferrogw validate` and `ferrogw doctor` both catch this.

Add such a plugin's name to `multiStagePlugins` in `config/example_plugin_stages_test.go`
and give it both entries in `config.example.yaml` and `config.example.json`; the test
fails otherwise.

## Adding a New Strategy

1. Create `internal/strategies/<name>.go` implementing `strategies.Strategy`.
2. Handle the new `StrategyMode` constant in `buildStrategy` (`gateway_strategy.go`).
3. **Handle it in `validateStrategy` (`config/load.go`) too.** Strategies are
   built lazily on the first request that needs one, so anything
   `buildStrategy` can reject and `validateStrategy` does not is a 500 on a
   live request instead of a startup error. `TestValidateConfigCoversStrategyConstruction`
   holds the two in step and fails if you add an arm to one and not the other.
   Every mode not in `validateStrategy`'s switch falls to its `unknown strategy
   mode` default, so a new mode is unusable until it is listed.
4. If the strategy matches on a **closed set of values** — the way `conditional`
   matches `conditions[].key` and `content-based` matches
   `content_conditions[].type` — add each new value to the accepted list in
   `config` (`ConditionKeys()` / `ContentConditionTypes()`), to the matcher
   switch in `internal/strategies`, **and** to the corresponding constant list
   in `internal/strategies/matchers_config_test.go`. The two halves are one
   rule: a value config accepts that no matcher arm handles silently routes
   that rule's whole traffic to the fallback target, and a value a matcher
   handles that config rejects is a config nobody can write.
5. Add tests in `internal/strategies/<name>_test.go`.

## Adding an Observability Exporter

Exporters bridge gateway events to a backend (LangSmith, Langfuse, Datadog, …). They live in the
separate `ai-gateway-plugins` repo, not here — the gateway only ships the contract + wiring.

1. Implement `observability.Exporter` (`Name`, `Init(cfg map[string]any)`, `Export(ctx, Event)`, `Shutdown(ctx)`). `Export` must be safe for concurrent use and non-blocking. Implement `observability.RoutingAttemptExporter` (`ExportsRoutingAttempts() bool`) as well to receive one `gateway.routing.attempt` event per physical provider call; without it the exporter is handed exactly one event per request.
2. Register a factory in `init()`: `observability.RegisterExporter("<name>", New)`.
3. Configure it under `observability.exporters` (`name`/`enabled`/`config`) — `internal/otel.Init` resolves enabled entries via `LookupExporter`; unknown/failed exporters are logged and skipped (non-fatal). Exporters work even with no OTLP endpoint.
4. Emit new span attributes only via constants in `observability/attributes.go`; mark not-yet-wired ones as Planned.

---

## Testing Conventions

The repository has an independently built frontend suite plus three Go test
suites, each with its own Make target or build tag. `go build` and `go test`
depend on no Node and no generated frontend asset; only `make build`, the
image, and the release pipeline embed a bundle.

### Web application

```bash
npm ci --prefix web
npm run check --prefix web     # lint + unit tests + TypeScript + production build
npm run test:e2e --prefix web  # rendered Chromium workflows
```

Keep the web application compatible with the CSP the gateway serves it under
(`internal/middleware/securityheaders.go`): no inline scripts and no runtime CDN
dependencies. Inline styles are blocked with one exception — `style-src` allows a
single stylesheet by SHA-256 digest, which the component library injects to hide
a scrollbar inside an open dropdown. Adding a second such exception is a
deliberate decision, not a formality: `'unsafe-inline'` would disable every
digest in the same directive and permit any inline style on a page that manages
credentials. `src/lib/csp.test.ts` recomputes the digest from the installed
library and compares it against the Go constant, so a dependency upgrade that
changes the stylesheet fails there rather than silently reintroducing a console
violation nobody reads.

Configure separate-origin deployments through the runtime `config.json` and the
gateway's `CORS_ORIGINS`.

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
make test-integration-postgres # the same suite with a shorter timeout
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

### 3. Strategy end-to-end (no keys)

`scripts/strategy_e2e.sh` runs every routing mode over the real `ferrogw` binary
and its HTTP surfaces — unary, SSE, embeddings, `/v1/models`, `/metrics` —
against three scriptable mock upstreams (`scripts/mockllm`) that stand in for
groq, together and openai. Each scenario tells one mock to fail, rate-limit,
slow down, fail mid-stream, or recover before the request is sent, and the
response's `provider` field plus the mocks' call counters show what the
strategy did. Failure classes, retries and `Retry-After`, breaker
open/half-open/closed, weight and variant distributions, and `model_map` on
unary and stream are all covered; nothing needs a credential.

```bash
make test-e2e-strategies              # about a minute; runs in CI
E2E_SLOW=1 make test-e2e-strategies   # adds the hung-target cell (~15s more)
```

`scripts/strategy_smoke.sh` is the live counterpart against real providers and
needs `GROQ_API_KEY` and `TOGETHER_API_KEY`.

### Additional checks

- `go test ./internal/admin/...`
- `go test ./plugin/logger/...`
- Prefer UTC assertions for persisted/admin timestamps.
- For dashboard rendering, avoid `innerHTML` with API data; use React text nodes and typed components.

---

## Working Agreements (AI sessions)

- Never write provider wire-format code from memory — verify against the
  conformance fixture, vendor docs, or a live probe first.
- A failing guard test (stability, conformance coverage, base-URL convention,
  example-config sync) means: complete the checklist step it guards. Never edit
  the guard.
- "Done" = the gate's exit code plus the failing lines if any — not a prose
  summary of a run.
- Fix defects root-cause-first: the regression test lands beside the fix;
  hand-write the `[Unreleased]` entry (symptom → cause → behaviour change).
