# Ferro Labs AI Gateway — Roadmap

Where the project is headed, and what it already does. For per-release detail,
see [CHANGELOG.md](CHANGELOG.md).

## Roadmap

What's next, roughly in priority order:

- **Routing depth** — per-target timeouts, `429` cooldown that honours `Retry-After`, context-length failover, sticky hashing for load balancing and A/B tests, rule target chains, input+output cost ranking, and least-latency samples that decay.
- **Plugin SDK & vendor observability bridges** — external guardrail and transform plugins, plus bridges for LangSmith, Langfuse, Datadog, New Relic, Honeycomb, Grafana, and more, shipped from a companion `ai-gateway-plugins` repo so the core binary stays slim.
- **Webhook notifications** — configurable alerts for budget limits, error spikes, and circuit-breaker events.
- **Semantic & Redis-backed caching** — beyond the built-in in-memory cache.
- **Broader provider coverage and an official Go client**, driven by community demand.
- **Deeper deployment guidance** — Kubernetes operators and Terraform modules.

### Plugins & cookbook

Have a plugin idea, or a recipe worth sharing? Plugin proposals and
cookbook-style usage recipes are discussed in the open — ideas there help shape
what lands next:

👉 **[GitHub Discussions](https://github.com/ferro-labs/ai-gateway/discussions)**

## Shipped

Everything below is available today.

### Routing

- 8 strategies: single, fallback, load balance, least latency, cost-optimized, content-based, A/B test, conditional
- Provider failover with per-target retry (jittered backoff, status-code filters), honoured under every routing mode; pool modes move to a sibling only after a failover-safe failure (v1.5.1)
- Per-request model aliases, operator-declared `targets[].models`, and per-target `model_map` — one visible model key, a different upstream id on each provider (v1.5.1)
- One `gateway.routing.attempt` observation per physical provider call and A/B variant attribution on every attempt, opt-in for exporters (v1.5.1)
- Per-target concurrency limits with a bounded queue and 429 shedding
- Per-provider circuit breaker shared across every surface

### Providers & models

- 30 providers behind one OpenAI-compatible API — OpenAI-compatible and native-wire alike (Anthropic, Gemini, Bedrock, Vertex AI, Cohere, …)
- Capability matrix and `GET /v1/capabilities` — machine-readable, per-provider parameter support
- Live model discovery plus a shared model catalog powering `/v1/models`

### API surface

- Chat (with streaming), embeddings, image generation, and legacy completions
- Audio — speech-to-text and text-to-speech — plus rerank and moderations
- Batch and files, and a priced, governed Responses API
- Transparent pass-through proxy for any other `/v1/*` route

### Guardrails & plugins

- Built-in: word-filter, max-token, response-cache, request-logger, rate-limit, budget
- Staged middleware (before / after / on-error) with an explicit deny-versus-fail policy

### MCP (Model Context Protocol)

- stdio and Streamable-HTTP transports with an agentic tool-call loop
- Tool allowlists, bounded call depth, cross-server deduplication, and subprocess environment isolation

### Observability

- OpenTelemetry tracing (OTLP gRPC/HTTP, W3C propagation, GenAI + `ferro.*` attributes, privacy levels) — zero-allocation when off
- Prometheus metrics; `/health`, `/livez`, and `/readyz`; a single trace ID unified across logs, spans, and the `X-Request-ID` header
- Structured request logging with SQLite or PostgreSQL persistence

### Operations dashboard

- A React/TypeScript console compiled into the binary and served at `/` from the same port — one artifact, no second origin
- Overview, analytics, providers, routing, plugins, playground, tracing, request logs, audit trail, configuration, and API keys

### Admin & security

- Scoped API keys, dashboard sessions, an audit trail, and config history with rollback
- Security headers, trusted-proxy client-IP resolution, secret redaction, and `${VAR}` references resolved at construction (never stored), plus production-mode startup guards

### Platform

- Single static binary, ~32 MB base memory, 13,925 RPS at 1,000 concurrent users
- memory / SQLite / PostgreSQL backends
- Multi-arch container images, a Helm chart, GoReleaser packaging, and Railway & Render deploy templates
- Official Python and TypeScript SDKs
- Importable from Go (v1.5.0): `run.Main()` composes the whole `ferrogw` program into your own binary with extra plugins compiled in, `run.Run(ctx, …)` runs it under a context you own, and `httpgateway` mounts the gateway's HTTP surfaces behind your own middleware
