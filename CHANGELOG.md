# Changelog

All notable changes to Ferro Labs AI Gateway are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.3] — 2026-06-15

Streaming-correctness release. Brings the streaming routing path (`RouteStream`) to parity with non-streaming `Route`: the post-request plugin pipeline, the per-provider circuit breaker, and least-latency / cost-optimized ordering now all apply to `stream: true` traffic. Also tightens fallback retry semantics and async-context propagation. No public API breaks. Fixes every issue labelled [`release-1.1.3`](https://github.com/ferro-labs/ai-gateway/issues?q=label%3Arelease-1.1.3): [#135](https://github.com/ferro-labs/ai-gateway/issues/135), [#136](https://github.com/ferro-labs/ai-gateway/issues/136), [#137](https://github.com/ferro-labs/ai-gateway/issues/137), and [#138](https://github.com/ferro-labs/ai-gateway/issues/138), plus enhancement [#181](https://github.com/ferro-labs/ai-gateway/issues/181).

### Fixed

- **Streaming requests skipped the post-request plugin pipeline** (issue [#135](https://github.com/ferro-labs/ai-gateway/issues/135), PR [#201](https://github.com/ferro-labs/ai-gateway/pull/201)): `RouteStream` ran only `before_request` plugins, so `after_request` (response-cache store, request-logger) and `on_error` plugins never fired for streaming traffic and a cache `Skip` hit was discarded with the provider called anyway. The full plugin lifecycle now runs for streams: `RunAfter` fires once the stream drains — with the response reconstructed from streamed chunks so the response-cache can store it — `RunOnError` fires on resolution / provider / stream / after-plugin failures, and a cache hit short-circuits the provider into a single streamed chunk.
- **Circuit breaker was a no-op for streaming-first traffic** (issue [#136](https://github.com/ferro-labs/ai-gateway/issues/136), PR [#199](https://github.com/ferro-labs/ai-gateway/pull/199)): the streaming path never recorded breaker outcomes, so mid-stream provider failures did not count and the breaker never opened. Stream success is now recorded at stream completion and failures (both at startup and mid-stream) count toward the breaker; open-circuit streaming targets are skipped during resolution so fallback advances instead of returning circuit-open.
- **Fallback strategy retried cancellations and open circuits** (issue [#137](https://github.com/ferro-labs/ai-gateway/issues/137), PR [#198](https://github.com/ferro-labs/ai-gateway/pull/198)): `shouldRetry` returned true for any error without a parseable HTTP status code, so `context.Canceled` / `context.DeadlineExceeded` and `circuitbreaker.ErrCircuitOpen` re-attempted already-cancelled requests and burned the whole retry budget against an open circuit. These sentinels are now non-retryable, and the retry loop checks `ctx.Err()` before each attempt.
- **Streaming ignored least-latency / cost-optimized ordering** (issue [#138](https://github.com/ferro-labs/ai-gateway/issues/138), PR [#198](https://github.com/ferro-labs/ai-gateway/pull/198)): `streamingTargetOrderLocked` had no case for these modes and silently fell through to declaration order. Streaming now orders targets by observed p50 latency (exploring unsampled targets first) and by catalog model cost, mirroring the non-streaming path, and records successful stream latency samples so least-latency converges for streaming-only traffic.

### Changed

- **Circuit-breaker failure accounting** (issue [#136](https://github.com/ferro-labs/ai-gateway/issues/136), PR [#199](https://github.com/ferro-labs/ai-gateway/pull/199)): caller-side cancellation and client deadlines no longer trip the breaker, while provider-side timeouts that surface as `context.DeadlineExceeded` while the request context is still live are counted as failures. Applies to both `Route` and `RouteStream`.
- **Detached background goroutines preserve trace context** (issue [#181](https://github.com/ferro-labs/ai-gateway/issues/181), PR [#202](https://github.com/ferro-labs/ai-gateway/pull/202)): async event hooks and the streaming observability completion event now run under `context.WithoutCancel(ctx)` instead of the already-cancelled request context / `context.Background()`, so fire-and-forget work (DB writes, outbound calls) is no longer dead-on-arrival and recorded events stay linked to the originating trace. MCP background initialization parents its 60s timeout on the gateway shutdown context so `Close()` cancels in-flight handshakes instead of letting them linger.

### Notes

- Under `cost-optimized` routing with `unpriced_strategy: skip`, the streaming path keeps unpriced providers as a last-resort fallback (and errors only when *no* candidate is priced), whereas the non-streaming path never selects an unpriced provider. This is intentional for the streaming fallback chain.
- All public `release-1.1.3` issues — [#135](https://github.com/ferro-labs/ai-gateway/issues/135), [#136](https://github.com/ferro-labs/ai-gateway/issues/136), [#137](https://github.com/ferro-labs/ai-gateway/issues/137), [#138](https://github.com/ferro-labs/ai-gateway/issues/138) — and enhancement [#181](https://github.com/ferro-labs/ai-gateway/issues/181) are closed by this release.

---

## [1.1.2] — 2026-06-06

Release hardening patch for the external model catalog cutover, catalog pricing correctness, and response-body lifecycle fixes. No public API breaks. Fixes every issue labelled [`release-1.1.2`](https://github.com/ferro-labs/ai-gateway/issues?q=label%3Arelease-1.1.2): [#132](https://github.com/ferro-labs/ai-gateway/issues/132), [#133](https://github.com/ferro-labs/ai-gateway/issues/133), [#134](https://github.com/ferro-labs/ai-gateway/issues/134), and [#185](https://github.com/ferro-labs/ai-gateway/issues/185).

### Fixed

- **Azure OpenAI, Azure Foundry, and Vertex AI catalog pricing aliases** (issue [#132](https://github.com/ferro-labs/ai-gateway/issues/132)): gateway provider IDs such as `azure-openai`, `azure-foundry`, and `vertex-ai` now resolve against the catalog's canonical provider prefixes (`azure_openai`, `azure_foundry`, `azure`, and `vertex_ai`). Cost calculation walks the provider-specific fallback chain, skips unpriced preferred chat entries when a priced fallback exists, and includes explicit regression coverage for Azure and Vertex pricing.
- **OpenAI response-body lifecycle** (issue [#185](https://github.com/ferro-labs/ai-gateway/issues/185)): non-streaming OpenAI-compatible responses now drain the HTTP body after decoding so transports can reuse connections, and streaming responses close the OpenAI stream when the gateway stream goroutine exits.
- **Provider and circuit-breaker lookup race** (PR [#170](https://github.com/ferro-labs/ai-gateway/pull/170)): routing strategy provider lookup now snapshots the provider and circuit-breaker maps under the gateway lock before dispatch, avoiding concurrent map access during runtime discovery or config reload.
- **Admin API key mutation leak** (PR [#190](https://github.com/ferro-labs/ai-gateway/pull/190)): key-management APIs now return copied in-memory key records so callers cannot mutate the store's internal state through returned pointers.
- **Shutdown hook regression coverage** (PR [#171](https://github.com/ferro-labs/ai-gateway/pull/171)): added unit-level tests around hook shutdown behavior so future edits keep close/drain semantics intact.

### Changed

- **Model catalog loading now consumes the external release artifact** (issue [#133](https://github.com/ferro-labs/ai-gateway/issues/133)): the default catalog source is the latest `ferro-labs/model-catalog` release artifact. Gateway startup and refresh use remote-first loading with the embedded `catalog_backup.json` as fallback, preserving offline startup while allowing catalog updates without an ai-gateway release.
- **Catalog lookup uses a reverse model-ID index** (issue [#134](https://github.com/ferro-labs/ai-gateway/issues/134)): bare model-ID lookups avoid scanning the full catalog, while preserving the previous behavior for arbitrary caller-constructed `Catalog` values through validation and fallback scanning.
- **Streaming content matching precompiles regular expressions** (PR [#189](https://github.com/ferro-labs/ai-gateway/pull/189)): repeated streaming content checks no longer compile regexes on the hot path, and config validation fails fast on invalid patterns.
- **CI and release workflows use Go `1.25.11`**: vulnerability scanning now runs against the patched Go 1.25 toolchain so standard-library `govulncheck` findings match the release environment rather than a stale host toolchain.

### Added

- **Catalog coverage guardrail**: added a provider/catalog coverage test that verifies registered providers either have priced catalog entries for representative models or are explicitly documented as dynamic/no-prefix exclusions.
- **Catalog backup refresh guardrails**: added the `scripts/refresh_catalog_backup.sh` helper and release workflow checks to keep the embedded fallback catalog aligned with the external catalog artifact.
- **Catalog load observability**: added `gateway_catalog_loads_total{source,result}` metrics and structured remote/fallback logging with catalog URLs sanitized for credentials and query strings.

### Notes

- `models/catalog.json` was removed from the repository. Runtime catalog loading now uses the remote release artifact plus the embedded `models/catalog_backup.json` fallback.
- The release notes generator reads this `1.1.2` section directly when publishing the `v1.1.2` GitHub release.

---

## [1.1.1] — 2026-05-31

Stability hotfix. No new features, no API breaks. Fixes every issue labelled [`release-1.1.1`](https://github.com/ferro-labs/ai-gateway/issues?q=label%3Arelease-1.1.1) plus three additional CRITICAL bugs found in the post-v1.1.0 engine audit (shutdown panic, runtime-discovery data race, streaming goroutine/body leak). Adds a `goleak` + `-race` concurrency stress harness so these regressions can't recur silently.

### Fixed

- **Send on closed channel panic during shutdown** (issue [#127](https://github.com/ferro-labs/ai-gateway/issues/127)): `Gateway.Close()` previously called `close(g.hookDispatchQ)` while `publishEvent` could still be enqueuing dispatches; the producer's `select`/`default` arm guards a *full* channel but not a *closed* one, so production crashed under shutdown-under-load. `Close()` now cancels a shutdown context instead; producers select on it before sending; workers drain any queued events before exiting; `Close()` waits up to 5s for workers via a `WaitGroup` (never blocks indefinitely so a panicking hook can't wedge shutdown). Stress-tested with 50 concurrent `Route()` callers racing `Close()` under `-race`.
- **Data race in provider lookup vs runtime discovery / config reload** (issue [#128](https://github.com/ferro-labs/ai-gateway/issues/128)): the lookup closure built in `getStrategy` read `g.providers` and `g.circuitBreakers` without holding `g.mu`, racing `RegisterProvider` and `ReloadConfig` (which reassigns `circuitBreakers` wholesale). Closure now takes `g.mu.RLock` for its body; verified `Route` (`gateway.go:330`) and `RouteStream` (`gateway.go:1108`) release the gateway lock before strategy execution so the lock-in-closure cannot recursively deadlock against a writer. Stress-tested with 20 concurrent `Route()` callers racing a mutator goroutine that reassigns both maps under `-race`.
- **Streaming goroutine and HTTP body leak on client disconnect**: `streamwrap.Meter` previously blocked forever on `out <- chunk` when the consumer (typically the HTTP handler) stopped reading because the client disconnected. The `Meter` goroutine, the upstream provider goroutine (blocked on its next send to `src`), and the provider's HTTP response body all leaked. `Meter` now selects on `ctx.Done()` for every send and every read from `src`; on cancel it drains `src` so the upstream goroutine can finish its in-flight write and exit. Emits a single `gateway.request.failed` event with a new `client_canceled` `provider_errors` metric label so budgets and observability still see the request and dashboards can separate client disconnects from real provider errors.
- **MCP registry/executor and plugin-manager races** (issue [#131](https://github.com/ferro-labs/ai-gateway/issues/131), PR [#172](https://github.com/ferro-labs/ai-gateway/pull/172)): `Route`/`RouteStream` now snapshot `g.mcpRegistry` / `g.mcpExecutor` under `g.mu.RLock` instead of reading the fields after the lock is released, eliminating a race against `ReloadConfig`. `plugin.Manager` gets its own `sync.RWMutex` around the `before`/`after`/`onErr` slices so registrations during reload are safe vs concurrent execution.
- **Streaming aborts on SSE lines larger than 64 KB** (issue [#129](https://github.com/ferro-labs/ai-gateway/issues/129), PR [#153](https://github.com/ferro-labs/ai-gateway/pull/153)): added shared `providers/core/sse_scanner.go` with a `Buffer(_, 1 MiB)` helper and applied it to the 9 stream-capable providers that were missing it. Tools, long reasoning blocks, and large embedded payloads no longer truncate the stream.
- **Nil-pointer panic in `least-latency` / `cost-optimized` / `loadbalance` when a target is unresolvable at dispatch** (issue [#130](https://github.com/ferro-labs/ai-gateway/issues/130), PR [#156](https://github.com/ferro-labs/ai-gateway/pull/156)): all three strategies now return a routing error when the selected target can no longer be resolved between candidate-building and dispatch, instead of dereferencing a nil provider.
- **`cost-optimized` routing treated `null`-priced catalog entries as $0** (issue [#126](https://github.com/ferro-labs/ai-gateway/issues/126), PR [#155](https://github.com/ferro-labs/ai-gateway/pull/155); originally scoped for v1.1.2, pulled forward because the fix was ready). Unpriced candidates no longer silently win cheapest-provider selection in a mixed pool. Behavior is governed by the new `strategy.unpriced_strategy` knob (see *Added*); the default preserves the historical fallback ranking for pools where every candidate is priced.

### Added

- **`strategy.unpriced_strategy` config knob** for `cost-optimized` routing: `fallback` (default — prefer priced candidates, then first compatible unpriced target), `skip` (reject unpriced candidates), or `allow` (legacy behavior — treat missing prices as zero cost). Validated at config load.
- **`providers/core/sse_scanner.go`**: shared `NewSSEScanner(r)` helper returning a `*bufio.Scanner` pre-configured with a 1 MiB line buffer. New stream providers should call it instead of repeating the buffer setup.
- **`provider_errors{err="client_canceled"}` metric label**: distinguishes streaming requests cancelled by the client from real provider errors. Existing `provider_error` and `circuit_open` labels are unchanged.
- **Stress / leak test harness**: `internal/streamwrap/wrap_leak_test.go` (goroutine-leak check + client-disconnect + natural-end-of-stream cases) and `gateway_stress_test.go` (`TestStress_ShutdownUnderLoad_NoPanic`, `TestStress_ReloadUnderLoad_NoRace`). Both use `go.uber.org/goleak` and run under `-race`.

### Changed

- **`Gateway.Close()`** now drains the hook-dispatch queue and waits up to 5 seconds for hook workers to finish in-flight dispatches before returning. The hook channel is no longer closed by `Close()` — workers exit via the new shutdown context. Calling `Close()` more than once remains safe (idempotent).
- **`go.uber.org/goleak`** promoted from an indirect to a direct dependency, used by the new stress and leak tests.

### Documentation

- `README.md` and the YAML/JSON example configs document the new `strategy.unpriced_strategy` knob under the *Routing strategies* section.

### Notes

- All public `release-1.1.1` issues — [#126](https://github.com/ferro-labs/ai-gateway/issues/126), [#127](https://github.com/ferro-labs/ai-gateway/issues/127), [#128](https://github.com/ferro-labs/ai-gateway/issues/128), [#129](https://github.com/ferro-labs/ai-gateway/issues/129), [#130](https://github.com/ferro-labs/ai-gateway/issues/130), [#131](https://github.com/ferro-labs/ai-gateway/issues/131) — are closed by this release.
- A separate GitHub Security Advisory accompanies this tag for the streaming-disconnect fix; check the Security tab for the GHSA ID and CVSS scoring.

---

## [1.1.0] — 2026-05-24

Adds opt-in OpenTelemetry tracing. Off by default — a zero-allocation no-op until an OTLP endpoint or exporter is configured.

### Added

- **OpenTelemetry tracing** (issue [#49](https://github.com/ferro-labs/ai-gateway/issues/49)): new public `observability` package (stable `Provider`/`Span`/`Exporter`/`Event` seam + `gen_ai.*`/`ferro.*` attribute constants) backed by an `internal/otel` OTLP pipeline (gRPC + HTTP/protobuf, W3C propagation). Each `Route()`/`RouteStream()` emits a `gateway.request` span carrying model, token-usage, cost, and routing attributes; plugins and MCP tool calls emit child spans; outbound provider calls are `otelhttp`-instrumented.
- **Unified trace ID**: the OTel `trace_id`, log trace ID, `X-Request-ID` header, and `ferro.gateway.trace_id` attribute are identical per request (custom `IDGenerator` adopting the logging trace ID). Holds for self-originated requests too; embedders bypassing `logging.Middleware` get a consistent independent ID.
- **Privacy levels**: `observability.tracing.privacy_level` — `none` (records only `"redacted"`), `metadata` (default; redacts email/JWT/AWS keys via `internal/redact`), or `full` (raw error text). Validated at config load.
- **Exporter event pathway**: registered exporters receive `gateway.request.completed`/`failed` events, configured via the new `observability.exporters` block (non-fatal on unknown/failing exporters). Contract + wiring only — no built-in exporters ship here; vendor bridges live in the forthcoming `ai-gateway-plugins` repo.
- **Config**: `ObservabilityConfig`/`TracingConfig`/`ExporterConfig` (endpoint, protocol, sample_ratio, privacy_level, shutdown_grace) in `gateway.Config`; `OTEL_*` env vars take precedence. New `Gateway.SetObservability`/`Observability` accessors.
- **OTLP exporter headers**: `observability.tracing.headers` (values support `${ENV}` interpolation) enables authenticated OTLP export to managed backends (Datadog, New Relic, Honeycomb, …). The standard `OTEL_EXPORTER_OTLP_HEADERS` env var also applies. The endpoint scheme selects transport security — `https://` uses TLS, while `http://` or a bare `host:port` connects in plaintext.

### Changed

- `internal/logging.Middleware` trace-ID precedence: existing context ID → inbound `X-Request-ID` → freshly generated.
- `internal/transport.Manager.DefaultTransport` and `ProviderPool.Transport` now return the raw `*http.Transport` (the client `RoundTripper` is the `otelhttp` wrapper) — inspect via these accessors.

### Documentation

- New **Observability** section in both [README.md](README.md) and [README.zh-CN.md](README.zh-CN.md) (Jaeger quickstart, config, OTLP headers, endpoint TLS, privacy levels, plugin exporters); `config.example.{yaml,json}` and ROADMAP updated for the OTel scope.

---

## [1.0.10] — 2026-05-16

Security maintenance release addressing GitHub Dependabot alerts and adding CI coverage for reachable Go vulnerabilities.

### Security

- **gRPC-Go authorization bypass**: Overrode transitive `google.golang.org/grpc` resolution to `v1.79.3` to address `GHSA-p77j-4mvh-x3m3` / `CVE-2026-33186`.
- **golang.org/x/crypto SSH vulnerabilities**: Upgraded `golang.org/x/crypto` from `v0.35.0` to `v0.51.0`, addressing `GHSA-f6x5-jh6r-wrfv` / `CVE-2025-47914` and `GHSA-j5w8-q4qc-rx2x` / `CVE-2025-58181`.
- **Moby/Docker advisory cleanup**: Upgraded the `testcontainers-go` dependency chain and removed the vulnerable `github.com/docker/docker` module from the final Go module graph, addressing `GHSA-x744-4wpc-v9h2` / `CVE-2026-34040`, `GHSA-pxq6-2prw-chj9` / `CVE-2026-33997`, and `GHSA-4vq8-7jfc-9cvp` / `CVE-2025-54410`.
- **containerd advisory cleanup**: Upgraded the `testcontainers-go` dependency chain and removed the vulnerable `github.com/containerd/containerd` module from the final Go module graph, addressing `GHSA-pwhc-rpq9-4c8w` / `CVE-2024-25621`, `GHSA-265r-hfxg-fhmg` / `CVE-2024-40635`, and `GHSA-m6hq-p25p-ffr2` / `CVE-2025-64329`.
- **Go standard library scan coverage**: Added CI `govulncheck` scanning and configured CI/CodeQL workflows to use the latest Go 1.25 patch release, covering reachable standard-library findings such as `GO-2026-4982`, `GO-2026-4980`, `GO-2026-4976`, `GO-2026-4971`, and `GO-2026-4918`.
- **Repository security settings**: Enabled Dependabot security updates, secret scanning, and secret scanning push protection for the GitHub repository.

### Changed

- Upgraded `github.com/testcontainers/testcontainers-go/modules/postgres` from `v0.34.0` to `v0.42.0`.
- Upgraded `golang.org/x/oauth2` from `v0.30.0` to `v0.34.0` as part of the dependency refresh.
- Added a dedicated `Vulnerability Scan` job to CI using `govulncheck`.

---

## [1.0.9] — 2026-05-14

Maintenance release updating the project baseline from Go 1.24 to Go 1.25. No public API or behaviour changes.

### Changed

- **Go toolchain baseline**: Updated `go.mod` to require Go 1.25.
- **Container builds**: Updated the source-build Docker image from `golang:1.24-alpine` to `golang:1.25-alpine`.
- **CI and releases**: Updated GitHub Actions test, integration, lint, and release jobs to use Go 1.25.x.
- **Lint tooling**: Updated the CI `golangci-lint` version from `v2.1.0` to `v2.4.0` for Go 1.25 support.
- **Documentation**: Updated README badges and contributor/internal docs to advertise Go 1.25+.

---

## [1.0.8] — 2026-05-12

Internal quality release completing the integration-test harness. No public API or behaviour changes.

### Added

- **`test/integration/http/`** — HTTP-layer integration tests (build tag: `integration`):
  - `TestChatCompletion_*`: non-streaming and streaming chat completions through the in-process gateway with stub providers.
  - `TestModels_*`: model listing, provider filtering, and empty-registry edge cases.
  - `TestProxy_PassThrough`, `TestProxy_AuthHeadersInjected`, `TestProxy_NoProvider`: pass-through proxy tests against a live `httptest.Server` upstream.
- **`test/integration/plugins/`** — Plugin-chain integration tests (build tag: `integration`):
  - `TestPluginChain_WordFilter_BlockedWord` / `_CleanRequest`: word-filter blocks/passes requests at `before_request`.
  - `TestPluginChain_ResponseCache_Hit`: cache short-circuits the provider on the second identical request (same plugin instance registered at both `before_request` and `after_request`).
  - `TestPluginChain_OnError_Fires`: verifies `on_error` stage fires when the provider returns an error.
- **`test/integration/strategies/`** — Strategy integration tests (build tag: `integration`):
  - `TestStrategy_Fallback_PrimaryFails_SecondarySucceeds`, `_AllFail`: fallback routing behaviour.
  - `TestStrategy_LoadBalance_DistributesRequests`: 40 requests, each provider must receive ≥20%.
  - `TestStrategy_LeastLatency_LocksOntoFastestSeen`: seeds both providers, then asserts the faster one handles ≥80% of post-seed requests.

### Fixed

- **`gateway.go`**: `pctx.Skip = true` set by a `before_request` plugin (e.g. `response-cache`) was silently ignored — the gateway now short-circuits provider dispatch and returns the cached response directly. `after_request` plugins (logging, metrics) still fire.
- **`internal/strategies/leastlatency.go`**: Cold-start bug where the strategy locked onto the first-ever provider and never explored others. Unseen providers are now sampled before falling back to the lowest-p50 selection.
- **`.github/workflows/ci.yml`**: Integration job now runs `make test-integration` (which includes `-tags=integration`) instead of a bare `go test` that silently skipped all integration tests.

### Changed

- **`AGENTS.md`**: Updated Testing Conventions section to document unit and integration test suites with build tags, Make targets, and Postgres requirements.
- **`CONTRIBUTING.md`**: Replaced outdated "Integration tests require real provider API keys" section with a step-by-step "How to add an integration test" guide.

---

## [1.0.7] — 2026-05-11

Internal architecture release completing the `cmd/ferrogw` refactor. No public API or behaviour changes.

### Changed

- **`cmd/ferrogw` refactor — Phases 2–6**: Moved all remaining business logic out of `cmd/ferrogw/` into dedicated `internal/` packages. `main.go` is now 59 lines of Cobra wiring + plugin imports.
  - `internal/httpserver/` — HTTP server constructor (`server.go`) and Prometheus connection tracker (`conntracker.go`)
  - `internal/proxy/` — Pass-through reverse proxy and model scanner (benchmarks preserved)
  - `internal/handler/` — All `/v1/*` HTTP handlers: chat completions, completions, embeddings, images, models
  - `internal/middleware/` — Rate-limit middleware and proxy-auth middleware (joined existing CORS)
  - `internal/dashboard/` — Template rendering, pprof wiring, and startup logo
  - `internal/httpserver/router.go` — Full Chi router wiring
  - `internal/bootstrap/bootstrap.go` — Gateway construction, provider registration, config loading, startup banner, and `Serve()` entry point

---

## [1.0.6] — 2026-05-06

Feature release adding official Python and TypeScript SDKs, Helm chart distribution via ArtifactHub, and Replicate streaming support.

### Added

- **Official TypeScript SDK** ([ferro-labs/ferrolabs-typescript-sdk](https://github.com/ferro-labs/ferrolabs-typescript-sdk)): First-party TypeScript/JavaScript client library for the Ferro Labs AI Gateway — supports chat, streaming, embeddings, and image generation across 30+ providers.
- **Official Python SDK** ([ferro-labs/ferrolabs-python-sdk](https://github.com/ferro-labs/ferrolabs-python-sdk)): First-party Python client library for the Ferro Labs AI Gateway — works with any LLM or framework.
- **Helm charts on ArtifactHub** ([ferro-labs on ArtifactHub](https://artifacthub.io/packages/search?org=ferro-labs)): Ferro Labs Helm charts are now discoverable and installable via ArtifactHub, the standard Kubernetes package registry.
- **Replicate streaming** ([#108](https://github.com/ferro-labs/ai-gateway/pull/108), relates to [#46](https://github.com/ferro-labs/ai-gateway/issues/46)): The Replicate provider now implements `core.StreamProvider`. Streaming requests send `stream: true` to the Replicate prediction API, follow the returned stream URL, and parse Replicate SSE events (`output`, `done`, `error`) into the gateway's normalized `StreamChunk` format. Includes mock SSE test coverage.
- **Postgres integration tests**: 15 integration tests in `test/integration/` using testcontainers-go to spin up a real Postgres 16 container. Covers key store CRUD, config store persistence, request log write/list/paginate/delete, and bootstrap factory functions. Runs in CI after unit tests pass.

### Changed

- **README**: Added SDK links, ArtifactHub badge, and updated provider/SDK documentation for both English and Chinese READMEs.
- **Internal refactor**: Extracted CORS middleware, SSE streaming, error helpers, and store factories from `cmd/ferrogw/` into `internal/middleware`, `internal/sse`, `internal/apierror`, and `internal/bootstrap` with full test coverage — no public API changes.

---

## [1.0.5] — 2026-04-28

Feature release adding first-class Ollama Cloud support and broader embedding coverage while keeping the gateway's public API OpenAI-compatible for end users.

### Added

- **Ollama Cloud provider** ([#94](https://github.com/ferro-labs/ai-gateway/issues/94)): Added `ollama-cloud` as a separate provider from local `ollama`, using Ollama Cloud's documented `https://ollama.com/api` endpoints with Bearer token authentication via `OLLAMA_API_KEY`.
- **OpenAI-compatible gateway surface for Ollama Cloud**: Users can call the existing `/v1/chat/completions` endpoint with normal OpenAI-style payloads while the provider internally adapts requests to Ollama Cloud's native `/api/chat` API.
- **Streaming and model discovery**: Added native NDJSON streaming support and live model discovery from `/api/tags`, exposed through the gateway's normalized streaming and model-list interfaces.
- **Ollama Cloud model catalog entries**: Added `ollama-cloud/*` catalog entries for initial direct Cloud model IDs, including `gpt-oss:120b`, `gpt-oss:20b`, `qwen3-coder:480b`, and `deepseek-v3.1:671b`, in both the primary and embedded backup catalogs.
- **Expanded embeddings**: Bedrock, Cohere, Databricks, Fireworks, Gemini, Mistral, Novita, Together, and Vertex AI now implement the gateway's `EmbeddingProvider` interface, advertise the `embed` capability, and route `/v1/embeddings` requests through provider-native or OpenAI-compatible embedding APIs with normalized vectors and token-usage responses.
- **Embedding provider tests and registry guardrails**: Added package-local embedding tests for success, invalid input, upstream errors, auth/path mapping, and usage mapping, plus a registry consistency test that keeps `CapabilityEmbed` aligned with actual `EmbeddingProvider` implementations.
- **Implementation plan documentation**: Added `docs/ollama-cloud-implementation-plan.md` documenting provider identity, API mapping, catalog policy, testing scope, and known open questions.

### Changed

- **Provider registry and examples**: Updated provider registration, stability tests, config examples, Docker environment comments, and README provider counts for the new 30th provider.
- **Catalog pricing policy for Ollama Cloud**: Ollama Cloud catalog entries use `null` token pricing because Ollama currently documents plan/GPU-usage-based limits rather than fixed per-token rates.
- **Proxy scope**: Ollama Cloud intentionally does not advertise pass-through proxy support yet because Ollama Cloud's documented direct API is `/api/*`, not `/v1/*`.

---

## [1.0.4] — 2026-04-09

Patch release focused on security maintenance, cache correctness, and refreshed project messaging. This release keeps the `v1.0.x` line stable while improving the published release notes for GitHub releases.

### Security

- **Dependabot: AWS SDK EventStream decoder DoS**: Upgraded `github.com/aws/aws-sdk-go-v2/service/bedrockruntime` from `v1.50.1` to `v1.50.4` and `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` from `v1.7.6` to `v1.7.8` to address GitHub advisory `GHSA-xmrv-pmrh-hhx2`. The patched versions prevent malformed EventStream frames from triggering a panic in affected AWS SDK decoder paths.

### Fixed

- **Cache eviction at capacity** ([#83](https://github.com/ferro-labs/ai-gateway/pull/83), fixes [#43](https://github.com/ferro-labs/ai-gateway/issues/43)): When the response cache reached `max_entries`, new responses were dropped instead of replacing stale entries, which effectively disabled caching once the store filled up. The cache now evicts the earliest-expiring entry first, stores the incoming response, and includes regression coverage for deterministic eviction order.

### Improved

- **README polish**: Refined the README content and release documentation for the `v1.0.4` maintenance release so the project overview, positioning, and upgrade story are clearer for users landing on the repository or release page.
- **GitHub release notes**: Tag-driven releases now use the matching `CHANGELOG.md` section as the source for the GitHub release body, then append a full changelog link at the bottom. This keeps release pages richer, more descriptive, and aligned with the repository changelog.

---

## [1.0.3] — 2026-04-01

### Security

- **Dockerfile runs as non-root user**: Added `ferro` user/group in `Dockerfile.release`. Container no longer runs as root.
- **Constant-time auth comparison**: Admin middleware now uses `subtle.ConstantTimeCompare` consistently for all key comparisons, preventing timing side-channels.

### Improved

- **Template caching**: Dashboard page templates are parsed once at startup instead of on every request — eliminates per-request `template.ParseFS` overhead.
- **Dashboard redesign**: Improved layout, navigation, and styling across all dashboard pages.
- **CLI polish**: Consistent color helpers, cleaner output formatting, ASCII-safe log messages (replaced unicode dashes/arrows).

### Added

- **`MASTER_KEY` environment variable**: Single credential that authenticates at gateway startup — no stored keys required. Checked first in the auth chain using `subtle.ConstantTimeCompare`. Grants full admin scope.
- **`ferrogw init`**: First-run setup command — generates a `fgw_`+32-hex master key with 128-bit entropy, writes a minimal `config.yaml`. Never writes secrets to disk.
- **Dashboard login page** (`/dashboard/login`): Validates key via `/admin/health`, stores in `localStorage`, shows Admin / Read Only badge, and hides write actions for read-only sessions.
- **`/admin/health` returns scopes**: The health endpoint now includes the authenticated key's scopes so clients can determine permission level without a separate request.
- **`.env.example`**: Full reference file documenting `MASTER_KEY`, all 29 provider API keys, storage backends, rate limiting, and CORS origins. Bootstrap env vars marked deprecated. <!-- drift-ok: historical v1.0.3 count -->

### Changed

- **Single binary**: `ferrogw-cli` has been merged into `ferrogw` as Cobra subcommands (`doctor`, `status`, `admin`, `validate`, `plugins`, `version`). Running `ferrogw` with no subcommand still starts the server (backward compatible).
- **`proxyAuth`**: `/v1/*` routes now enforce `AuthMiddleware` by default and are only open when `ALLOW_UNAUTHENTICATED_PROXY=true` is set for local development. Operational endpoints such as `/metrics`, `/debug/vars`, and `/debug/pprof/*` continue to require auth.
- **Enhanced startup banner**: Shows top-5 provider status, masked master key, key store / config store backends, and a warning when deprecated bootstrap keys are in use. <!-- drift-ok: "top-5" is a display limit, not a provider count -->
- **Bootstrap keys deprecated**: `ADMIN_BOOTSTRAP_KEY` and `ADMIN_BOOTSTRAP_READ_ONLY_KEY` still work but are superseded by `MASTER_KEY`. They only activate when the key store is empty.

### Removed

- **`cmd/ferrogw-cli/` directory**: Deleted. All CLI commands live in `internal/cli/` and are wired as Cobra subcommands of `ferrogw`.
- **`make build-cli` Makefile target**: Removed. `make build` produces the single `ferrogw` binary.

---

## [1.0.2] — 2026-03-30

### Added

- **`X-Gateway-Overhead-Ms` response header**: Every response now includes gateway processing overhead in milliseconds, letting clients isolate gateway latency from provider latency.
- **Live upstream benchmark in README**: Measured overhead against live OpenAI API — **0.002 ms** bare proxy, **0.025 ms** with plugins enabled.
- **Docker Compose dev/prod split**: New `docker-compose.dev.yml` (build from source, debug logging, Ollama host access) and `docker-compose.prod.yml` (pinned image, restart policy, health check, resource limits, log rotation).

### Changed

- **Config examples**: Added all 29 providers as targets, conditional routing examples, retry/circuit-breaker config, and previously undocumented plugin fields (`max_input_length`, `burst`, `max_keys`). <!-- drift-ok: historical v1.0.2 count -->
- **`docker-compose.yml`**: Refactored to shared base config with commented env var stubs for all 29 providers. <!-- drift-ok: historical v1.0.2 count -->
- **AGENTS.md / CONTRIBUTING.md**: Updated "Adding a New Provider" checklist with config example and docker-compose steps; removed duplicate sections.

---

## [1.0.1] — 2026-03-27

### Security

- **SQL injection (gosec G701)**: Replaced ad-hoc `db.Exec(query, ...)` calls with pre-compiled prepared statements (`*sql.Stmt`) in `SQLStore`. All six write operations (`Revoke`, `Update`, `SetExpiration`, `Delete`, `ValidateKey`, `RotateKey`) and both SELECT queries (`Get`, `ValidateKey`) now use `stmt.Exec` / `stmt.QueryRow`, eliminating any query-string taint path.
- **SSRF (gosec G704)**: Added `url.Parse` + scheme/host validation in the `New()` constructor of every provider that accepts a configurable base URL (Anthropic, DeepSeek, Groq, OpenAI, Together AI). The catalog remote-fetch helper (`models/catalog.go`) validates the URL before making the HTTP request.

### Changed

- **`SQLStore.scanOne`**: Signature changed from `scanOne(query string, arg interface{})` to `scanOne(stmt *sql.Stmt, arg any)` — callers pass a prepared statement instead of a raw query string.
- **`SQLStore.Close`**: Now closes all prepared statements before closing the database connection.

### Quality

- **staticcheck QF1012**: Replaced `WriteString(fmt.Sprintf(...))` with `fmt.Fprintf` in `internal/admin/sql_store.go`, `internal/requestlog/store.go`, and `providers/bedrock/bedrock.go`.
- **revive unused-parameter**: Renamed unused `cmd` parameter to `_` in `internal/cli/doctor.go`.

### Improved

- **CLI overhaul**:
  - **New banner**: Replaced the block-art ASCII logo with a Figlet "doom" font rendering of `FERRO LABS` — orange bold + dim white side-by-side with proper column alignment.
  - **`version` command**: Expanded output to include `commit`, `built`, `go` runtime version, and `os/arch` alongside the version string. JSON/YAML output formats include all fields.
  - **Custom help template**: Grouped help output into `Commands` and `Admin API` sections for a cleaner overview.
  - **`--no-color` flag**: New persistent flag on the root command; also respects the `NO_COLOR` environment variable (https://no-color.org/).
  - **ANSI colour system**: Centralised `clr(code, s string)` helper in `output.go` with `colorOrange`, `colorBold`, `colorDim`, `colorGreen`, `colorRed`, and `colorYellow` constants. `printSuccess` now renders with a green `✓` prefix.
  - **`status` and `doctor` commands**: Registered in the command tree.

### Developer Experience

- **Git hooks (`.husky/`)**: Added `pre-commit` (runs `go fmt`, `go vet`, `golangci-lint`) and `pre-push` (runs `go test`) hooks. Scripts use direct `go` commands — no `make` dependency, works on Linux, macOS, and Windows (Git Bash).
- **`make vet`**: New Makefile target for `go vet ./...`.

---

## [1.0.0] - 2026-03-24

The first stable release of Ferro Labs AI Gateway — a production-grade, OpenAI-compatible AI gateway written in Go.

### What's in v1.0.0

- **29 built-in providers** — OpenAI, Anthropic, Gemini, Groq, Bedrock, Vertex AI, Hugging Face, OpenRouter, Cloudflare, Azure OpenAI, Azure Foundry, DeepSeek, Mistral, xAI, Cohere, Together AI, Fireworks, Replicate, Ollama, Databricks, DeepInfra, Moonshot, Novita, NVIDIA NIM, Cerebras, Perplexity, Qwen, SambaNova, and AI21.
- **8 routing strategies** — single, fallback, load balance, least latency, cost-optimized, content-based, A/B test, and conditional.
- **6 built-in OSS plugins** — word-filter, max-token, response-cache, request-logger, rate-limit, and budget.
- **MCP tool server integration** — agentic tool-call loops with Streamable HTTP transport, tool filtering, and bounded call depth.
- **Admin API and dashboard** — API key management, usage stats, request logs, config history with rollback, and a built-in dashboard UI.
- **Persistence backends** — memory, SQLite, and PostgreSQL for runtime config, API keys, and request logs.
- **Performance** — 13,925 RPS at 1,000 concurrent users, 32 MB base memory, per-provider HTTP connection pools, sync.Pool for request structs and JSON buffers, zero-allocation stream detection.

### Upgrading from rc.3

No breaking changes from `1.0.0-rc.3`. Updated README and CONTRIBUTING docs for stable release.

---

<details>
<summary><strong>1.0.0-rc.3</strong> — 2026-03-23</summary>

### Highlights

- Gateway hot path overhead reduced from 1,269µs to ~200µs (6.3x faster).
- Throughput at c=50 improved from 2,444 to 25,846 RPS (10.6x faster).
- New `internal/transport` package with per-provider isolated HTTP pools.
- Fixed response-cache bug that collapsed message ordering (#44).

### Bug Fixes

- **response-cache: preserve message order in cache key** (#44): The
  `cacheKey` function sorted messages before hashing, causing two requests
  with identical messages in different order to produce the same cache key.
  Removed `sort.Strings` — cache keys now preserve conversation order using
  incremental `sha256.New()` writes. ([2cd281a])

### Performance

- **`internal/transport/` package**: Per-provider isolated HTTP client pools
  with production-tuned settings. Separate streaming transport with no
  `ResponseHeaderTimeout` for SSE. Known provider presets for OpenAI,
  Anthropic, Gemini, Bedrock, Vertex AI, Groq, Ollama, and Azure OpenAI.
  Prometheus metrics for connection pool observability.
- **Per-provider HTTP clients**: All 28 providers now use <!-- drift-ok: historical rc.3 count -->
  `httpclient.ForProvider(Name)` for isolated connection pools instead of a
  single shared client. Legacy completions handler switched from
  `http.DefaultClient`.
- **sync.Pool for request structs**: `routeChatCompletionRequest` (19-field
  reset) and `plugin.Context` (metadata map capacity preserved) are now
  pooled. All fields explicitly reset before pool return for multi-tenant
  safety.
- **Pooled JSON marshaling buffers**: Added `core.MarshalJSON` and
  `core.JSONBodyReader` backed by `sync.Pool`. All 28 provider subpackages <!-- drift-ok: historical rc.3 count -->
  updated to use pooled buffers for request body serialization.
- **getStrategy() lock contention fix**: Changed from exclusive `Mutex.Lock`
  to double-checked locking with `RLock` fast path. Eliminates write-lock
  serialization on every request under concurrent load.
- **Cached target key slices**: Pre-computed target key ordering for
  single/fallback strategy modes avoids `[]string` allocation on every
  streaming request.
- **Batched RLock in RouteStream**: Merged two separate `g.mu.RLock()`
  acquisitions (provider resolution + catalog snapshot) into one.
- **SSE-optimized buffer pools**: Pooled `bufio.Reader` (64KB) and
  `bufio.Writer` (4KB) for streaming request/response handling.
- **Zero-alloc `IsStreamingRequest`**: Byte-scanning `"stream":true`
  detection with no JSON parsing and 0 allocations.

</details>

<details>
<summary><strong>1.0.0-rc.2</strong> — 2026-03-18</summary>

### Highlights

- Hardened the `rc` line for performance-focused validation ahead of `v1.0.0`.
- Reduced gateway hot-path overhead and tightened streaming control behavior.
- Continued the `cmd/ferrogw` split so startup, routing, and HTTP helpers are
  easier to reason about and maintain.
- Added contribution guidance to keep the gateway architecture and package
  boundaries consistent as the OSS surface stabilizes.

### Performance And Runtime

- Reduced request-path overhead in the core gateway flow.
- Improved SSE streaming timeout and control-path handling.
- Fixed OpenAI completion request decoding behavior used on the
  OpenAI-compatible path.

### Internal Structure

- Split `cmd/ferrogw` startup and HTTP helpers by responsibility.
- Completed the Phase 4 package-shaping work for the `ferrogw` command surface.
- Carried forward the architecture hardening and observability work from the
  post-`rc.1` stabilization phases.

### Release Notes

- `rc.2` is the performance-validation release candidate.
- Benchmarking remains focused on normalized gateway-overhead comparisons before
  the final `v1.0.0` release.

</details>

<details>
<summary><strong>1.0.0-rc.1</strong> — 2026-03-14</summary>

### Highlights

- First `v1` release candidate for Ferro Labs AI Gateway.
- OpenAI-compatible gateway surface for chat, model discovery, embeddings,
  image generation, and transparent provider proxying.
- 29 built-in providers behind one canonical provider registry.
- 8 routing strategies:
  `single`, `fallback`, `loadbalance`, `conditional`, `least-latency`,
  `cost-optimized`, `content-based`, and `ab-test`.
- 6 built-in OSS plugins:
  `word-filter`, `max-token`, `response-cache`, `request-logger`,
  `rate-limit`, and `budget`.
- First-class MCP tool server support for agentic tool-call loops.
- Built-in operational surface including `/health`, `/metrics`, admin APIs, and
  the dashboard UI.

### Provider Coverage

- Added first-class support for:
  `cerebras`, `cloudflare`, `databricks`, `deepinfra`, `moonshot`, `novita`,
  `nvidia-nim`, `openrouter`, `qwen`, and `sambanova`.
- Hardened provider registration with canonical names, ordered factory
  registration, and provider-name stability coverage.

### Platform Capabilities

- OpenAI-compatible request and response flow across providers.
- Chat streaming support across the supported streaming adapters.
- Persistent runtime config, API keys, and request logs with `memory`,
  `sqlite`, and `postgres` backends.
- MCP 2025-11-25 Streamable HTTP integration with tool discovery, allowlists,
  and bounded call depth.
- Cost-aware and latency-aware routing powered by the model catalog and runtime
  latency tracking.

### Release Notes

- This release candidate is the public stabilization point for the current OSS
  gateway surface ahead of `v1.0.0`.
- README, roadmap, and release docs were refreshed together so the project
  presents a consistent first-release story.
- Runnable examples now live in the dedicated
  `ferro-labs/ai-gateway-examples` repository.

</details>
