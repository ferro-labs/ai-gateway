# Ferro Labs AI Gateway Roadmap

## v1.0.0 — Stable Release

Status: **Shipped** (2026-03-24)

### What shipped

- 29 built-in providers behind a single OpenAI-compatible gateway surface
- 8 routing strategies: single, fallback, load balance, least latency, cost-optimized, content-based, A/B test, conditional
- 6 built-in OSS plugins: word-filter, max-token, response-cache, request-logger, rate-limit, budget
- Admin API with key management, usage stats, request logs, config history/rollback, and dashboard UI
- MCP tool server integration with agentic tool-call loops
- Persistence backends: memory, SQLite, PostgreSQL
- Per-provider HTTP connection pools, sync.Pool optimizations, zero-alloc stream detection
- 13,925 RPS at 1,000 concurrent users, 32 MB base memory
- Migration guides from LiteLLM, Portkey, and direct OpenAI SDK usage
- Helm chart support, Docker multi-arch images, GoReleaser packaging

## v1.0.5 — Ollama Cloud & Embeddings

Status: **Shipped** (2026-04-28)

### What shipped

- Ollama Cloud as the 30th provider with streaming and model discovery
- Expanded embedding support across 9 additional providers
- Embedding registry consistency tests

## v1.0.6 — SDKs, Helm, & Replicate Streaming

Status: **Shipped** (2026-05-04)

### What shipped

- **Official Python SDK** — [ferrolabs-python-sdk](https://github.com/ferro-labs/ferrolabs-python-sdk)
- **Official TypeScript SDK** — [ferrolabs-typescript-sdk](https://github.com/ferro-labs/ferrolabs-typescript-sdk)
- **Helm charts on ArtifactHub** — [ferro-labs on ArtifactHub](https://artifacthub.io/packages/search?org=ferro-labs)
- Replicate streaming support (SSE-based `CompleteStream`)

## v1.1.0 — OpenTelemetry Core

Status: **In progress** — branch `release/v1.1.0-observability`. Tracking issue: [#49](https://github.com/ferro-labs/ai-gateway/issues/49).

This release is intentionally **scoped to a pure OpenTelemetry core**. Vendor-specific bridges (LangSmith, Langfuse, Phoenix, Datadog, New Relic, Sentry, Helicone, Honeycomb, Grafana, …) are deliberately deferred to the v1.2.0 plugin SDK so they live once, in Go, in a dedicated repo — instead of being duplicated across the gateway core, the Python SDK, and the TypeScript SDK.

### In this release

- **Public `observability` package** — semver-stable `Provider` / `Span` / `Exporter` / `Event` contract with `gen_ai.*` (OTel GenAI semantic conventions) plus `ferro.*` extension attributes for cost, routing, cache, MCP, and stream timings.
- **OTLP tracing pipeline** — gRPC and HTTP/protobuf exporters via `internal/otel`, global W3C `TraceContext` + `Baggage` propagation, head sampling.
- **No-op short-circuit** when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset: zero allocations on the hot path (verified by `BenchmarkRoute_TracingOff`).
- **`gateway.request` root span** on every `Route()` / `RouteStream()` call with tokens, cost breakdown, routing strategy, and redacted error attributes.
- **`otelhttp` transport wrapping** on every per-provider HTTP client — outbound `CLIENT` child spans + automatic `traceparent` propagation to upstream LLM providers.
- **Trace ID unification** — OTel `trace_id`, `logging.TraceIDFromContext`, the `X-Request-ID` response header, and the `ferro.gateway.trace_id` span attribute are guaranteed equal per request.
- **Privacy levels** — `none` / `metadata` (default) / `full`, with built-in `internal/redact` policies (email / JWT / AWS access keys) applied to errors.
- **SDK observability** in [ferrolabs-python-sdk](https://github.com/ferro-labs/ferrolabs-python-sdk) and [ferrolabs-typescript-sdk](https://github.com/ferro-labs/ferrolabs-typescript-sdk) — runtime OTel detection (no hard dependency), `traceparent` injection, `trace_id` / `traceId` surfaced from gateway response headers.


### Backlog (still landing under v1.1.x patch releases)

- Plugin-stage child spans inside `plugin/Manager.Run{Before,After,OnError}`.
- Span hand-off from `RouteStream` into `streamwrap.Meter` so token / cost / stream-timing attributes land on the same span.
- MCP tool-call child spans.
- Semantic caching, Redis-backed auth cache, additional provider expansion — moved out of v1.1.0 to keep this release focused; tracked under v1.1.x / v1.2.x.

## v1.2.0 — Plugin SDK & Vendor Bridges

Status: Planning

The plugin SDK lands here so observability bridges can be developed and released independently of the gateway core, on their own cadence, without bloating the `ferrogw` binary or duplicating code across the SDKs.

### Priorities

- **`ai-gateway-plugins` companion repo** — Go modules per bridge, each implementing the stable `observability.Exporter` interface from v1.1.0. Initial bridges: LangSmith, Langfuse, Phoenix, Datadog, New Relic, Sentry, Helicone, Honeycomb, Grafana.
- **`ferrogw-builder` tool** — composes a custom `ferrogw` binary with the user-selected subset of plugins baked in, mirroring the `otelcol-builder` UX. Default `ferrogw` ships with zero bridges to stay slim.
- **Plugin SDK for guardrails / transforms** — external loading for custom request/response plugins.
- **Webhook notifications** — configurable alerts for budget limits, error spikes, circuit breaker events.
- **Enhanced A/B testing** — metrics collection and winner determination for variant experiments.

## Future

- Continue expanding provider coverage based on community demand
- Official Go client library
- Deepen production deployment guidance (Kubernetes operators, Terraform modules)
- Expand the [ai-gateway-examples](https://github.com/ferro-labs/ai-gateway-examples) repo
- Strengthen benchmark reporting and cross-gateway comparisons
