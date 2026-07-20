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
- **v1.1.10–v1.1.17** — Provider readiness remediation: proxy governance, native provider fidelity, enterprise endpoints, OpenAI-compatible provider coverage, local/prediction API providers, and provider-readiness closeout.
- **v1.1.18** — Critical fixes and security hardening: concurrent cache-hit race fix, SQLite file permissions restricted to `0600`, safe default per-IP rate limiting, and Go 1.25.11 toolchain pin.
- **v1.1.19** — Provider read bounds and typed errors: upstream provider response reads capped at 50 MiB, typed provider HTTP status errors for retry/circuit-breaker classification, DeepSeek streaming token accounting, and cross-provider status conformance.
- **v1.1.20** — Streaming deadlines and serving robustness: long-lived proxy and legacy-completions streams with an upstream idle bound, recoverable provider initialization failures, degraded health status codes, CSP/Permissions-Policy headers, panic recovery order, and metrics-cardinality hardening.
- **v1.1.21** — Key hashing and storage hardening: API keys stored as `sha256(key)` at rest with a table rebuild that purges the plaintext from the file, fail-closed bootstrap credentials, pre-write SQLite file permissions, versioned schema migrations, request-logger persistence through the shared store, and exact in-database log stats.
- **v1.1.22** — v1.1.x hardening closeout: streaming goroutine-leak and single circuit-breaker-probe fixes, streaming/non-streaming target parity, strict unknown-key config decode with an advisory `apiVersion`, a unified store schema-migration path, atomic config-history persistence, Gemini/Bedrock provider fixes, and Qwen live model discovery.

The v1.1.x hardening patch line is complete; further work continues in the v1.2.0+ minors below.

## v1.2.0 — Provider Parameter Capability Matrix

Status: **Shipped** (2026-07-14). Tracking issue: [#207](https://github.com/ferro-labs/ai-gateway/issues/207).

Builds on the v1.1.4 forwarding fix to make per-provider parameter support **explicit and machine-readable**, so a changed model behaviour can be traced to either provider capability or gateway forwarding.

### What shipped

- **Per-provider capability matrix** — one declarative source of per-param support (`forward` / `translate` / `unsupported`). Providers no longer carry private supported-parameter lists; the matrix is the only source enforcement reads, so a provider and its declared capabilities cannot drift apart.
- **`GET /v1/capabilities`** — the matrix exposed so users can compare providers programmatically.
- **Opt-in strict mode** — `compatibility.on_unsupported_param: warn | drop | reject`; default `warn` stays backward-compatible, `reject` returns a clear unsupported-parameter error.
- **Cross-provider conformance suite** — every provider is constructed through its registration seam and asserted against its real native payload, closing the gap between what a provider advertises and what it delivers ([#276](https://github.com/ferro-labs/ai-gateway/issues/276)).
- **Per-request deadlines and retry hygiene** — `request_timeout` bounds a non-streaming request end to end; retries are limited to retryable statuses with jittered backoff ([#277](https://github.com/ferro-labs/ai-gateway/issues/277), [#278](https://github.com/ferro-labs/ai-gateway/issues/278)).
- **Per-target concurrency limits** — `targets[].concurrency` bounds in-flight requests per provider and sheds with 429 when saturated ([#248](https://github.com/ferro-labs/ai-gateway/issues/248)).
- **Split liveness and readiness probes** — `/livez` and `/readyz` for orchestrator rollout gating ([#279](https://github.com/ferro-labs/ai-gateway/issues/279)).
- **Plugin failure policy** — a plugin that denies a request and a plugin that breaks are now distinct, so a broken rate-limit plugin no longer answers 429 and invites SDKs to retry into the outage ([#288](https://github.com/ferro-labs/ai-gateway/issues/288)).

### Deferred

- **Sanitized debug echo** — the `ferro.forwarded_params` attribute is defined but not emitted: the shared request builder has no span in scope, and threading one through for a debug-only attribute costs more than it returns. It stays marked Planned in `observability/attributes.go` until the builder carries a span for another reason.

## v1.3.0 — MCP stdio Transport

Status: Release candidate — dated 2026-07-20, awaiting tag. Tracking issue: [#121](https://github.com/ferro-labs/ai-gateway/issues/121).

### What ships

- **stdio transport** for the Model Context Protocol: an `mcp_servers` entry may set `command` instead of `url`, and the gateway launches and supervises that process for its lifetime. Any `npx`, `uvx`, or binary MCP server works without a separate HTTP endpoint. Contributed by [@gr3enarr0w](https://github.com/gr3enarr0w).
- **Subprocess environment isolation** — MCP servers do not inherit the gateway environment, so no gateway credential reaches a server implicitly. Anything a server needs, including a credential, must be listed in its `env`; `${VAR}` there is resolved at client construction and redacted from `GET /admin/config`.
- **Process-group teardown and stderr capture** — `npx`-style servers no longer leak their real worker on shutdown or reload, and a server's diagnostics reach the log instead of a pipe nobody reads.
- **Two fixes to the existing MCP path**: a misconfigured server no longer collapses streaming gateway-wide, and caller-supplied tool calls are no longer intercepted by the agentic loop. Both affected operators regardless of whether they used stdio.

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
