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

Status: **Shipped** (2026-05-24). Tracking issue: [#49](https://github.com/ferro-labs/ai-gateway/issues/49).

This release is intentionally **scoped to a pure OpenTelemetry core**. Vendor-specific bridges (LangSmith, Langfuse, Phoenix, Datadog, New Relic, Sentry, Helicone, Honeycomb, Grafana, …) are deliberately deferred to the v1.5.0 plugin SDK so they live once, in Go, in a dedicated repo — instead of being duplicated across the gateway core, the Python SDK, and the TypeScript SDK.

### What shipped

- **Public `observability` package** — semver-stable `Provider` / `Span` / `Exporter` / `Event` contract with `gen_ai.*` (OTel GenAI semantic conventions) plus `ferro.*` extension attributes for cost, routing, cache, MCP, and stream timings.
- **OTLP tracing pipeline** — gRPC and HTTP/protobuf exporters via `internal/otel`, global W3C `TraceContext` + `Baggage` propagation, head sampling.
- **No-op short-circuit** when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset: zero allocations on the hot path (verified by `BenchmarkRoute_TracingOff`).
- **`gateway.request` root span** on every `Route()` / `RouteStream()` call with tokens, cost breakdown, routing strategy, and redacted error attributes.
- **`otelhttp` transport wrapping** on every per-provider HTTP client — outbound `CLIENT` child spans + automatic `traceparent` propagation to upstream LLM providers.
- **Trace ID unification** — OTel `trace_id`, `logging.TraceIDFromContext`, the `X-Request-ID` response header, and the `ferro.gateway.trace_id` span attribute are guaranteed equal per request.
- **Privacy levels** — `none` / `metadata` (default) / `full`, with built-in `internal/redact` policies (email / JWT / AWS access keys) applied to errors.
- **SDK observability** in [ferrolabs-python-sdk](https://github.com/ferro-labs/ferrolabs-python-sdk) and [ferrolabs-typescript-sdk](https://github.com/ferro-labs/ferrolabs-typescript-sdk) — runtime OTel detection (no hard dependency), `traceparent` injection, `trace_id` / `traceId` surfaced from gateway response headers.


## v1.1.x — Stability & Correctness Patches

Weekly patch line. Each release is backward-compatible bug-fixing on a single theme — no public API breaks.

### Shipped

- **v1.1.1** — Concurrency & crash-safety hardening: data races, nil-pointer panics in routing, send-on-closed-channel on shutdown, oversized-SSE aborts, null-priced-model mispricing.
- **v1.1.2** — Cost & catalog accuracy: external model-catalog cutover, Azure/Vertex `$0` pricing fix, O(n) catalog-scan removal, OpenAI response-body lifecycle.
- **v1.1.3** — Streaming & async correctness: `RouteStream` brought to parity with `Route` (post-request plugins, circuit breaker, least-latency / cost ordering), tighter fallback retry semantics, `context.WithoutCancel` for detached goroutines.
- **v1.1.4** — Provider-translation correctness: tool/function calling, sampling params, `max_completion_tokens`, `finish_reason` normalization, Anthropic multimodal/tool roles, and Gemini `systemInstruction` across native and OpenAI-compatible providers, plus a dependency sweep.
- **v1.1.5** — Capability & model-support accuracy: capability-miss status codes (400/404), closed capability gaps (Azure embeddings/images, image generation, discovery), catalog-derived `/v1/models`.
- **v1.1.6** — Runtime robustness: cache & circuit-breaker correctness (logprobs in cache key, true LRU, half-open probe cap), OTel shutdown span-loss fix, plugin-pipeline panic recovery / RunOnError-on-reject / Close-on-reload, Bedrock bearer auth.
- **v1.1.7** — Small enhancements: end-to-end `context.Context` propagation, plugin-registry concurrency, hot-path allocation reductions, git-hook gating.
- **v1.1.8** — Security & trust hardening: baseline HTTP security headers, request body-size limit, trusted-proxy client-IP resolution, expanded secret redaction, config-validation and admin-key safety.
- **v1.1.9** — Quality & maintainability hardening: per-key rate-limit / budget scoping, atomic budget soft cap, internal package restructuring (no API or behaviour changes), and CI supply-chain hardening (SHA-pinned actions).

## v1.2.0 — Provider Parameter Capability Matrix

Status: Planning (target 2026-07-10). Tracking issue: [#207](https://github.com/ferro-labs/ai-gateway/issues/207).

Builds on the v1.1.4 forwarding fix to make per-provider parameter support **explicit and machine-readable**, so a changed model behaviour can be traced to either provider capability or gateway forwarding.

### Priorities

- **Per-provider capability matrix** — provider profiles declaring per-param support (`forward` / `translate` / `unsupported`), built on a shared OpenAI-compatible request builder so providers can't drift.
- **`GET /v1/capabilities`** — the matrix exposed so users can compare providers programmatically.
- **Opt-in strict mode** — `compatibility.on_unsupported_param: warn | drop | reject`; default `warn` stays backward-compatible, `reject` returns a clear unsupported-parameter error.
- **Sanitized debug echo** — forwarded parameter *names* surfaced via observability (never prompts or keys).
- **Docs** — "OpenAI-compatible request shape" documented separately from "OpenAI-identical feature support."

## v1.3.0 — MCP stdio Transport

Status: Planning (target 2026-07-17). Tracking issue: [#121](https://github.com/ferro-labs/ai-gateway/issues/121).

- **stdio transport** for the Model Context Protocol so the gateway can speak MCP over stdio alongside the existing transport, enabling local / embedded MCP clients.

## v1.4.0 — Embeddable Gateway

Status: Planning (target 2026-07-24). Tracking issue: [#206](https://github.com/ferro-labs/ai-gateway/issues/206).

- **Public importable server entrypoint** — embed the gateway directly in Go programs and plugin builders instead of only running the standalone `ferrogw` binary. Carries the highest API-stability commitment of the near-term minors, so it ships with deliberate public-surface design.

## v1.5.0 — Plugin SDK & Vendor Bridges

Status: Planning (target 2026-07-31). _Renumbered from the original v1.2.0 roadmap slot._

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

### Observability & caching backlog (unscheduled)

- Plugin-stage child spans inside `plugin/Manager.Run{Before,After,OnError}`.
- Span hand-off from `RouteStream` into `streamwrap.Meter` so token / cost / stream-timing attributes land on the same span.
- MCP tool-call child spans.
- Semantic caching, Redis-backed auth cache, additional provider expansion.
