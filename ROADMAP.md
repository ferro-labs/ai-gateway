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

## v1.1.0

Status: Planning

### Priorities

- **OpenTelemetry integration** — distributed tracing with OTLP export
- **Semantic caching** — PostgreSQL + pgvector/HNSW for similarity-based response cache
- **Redis support** — auth caching and rate limit state for multi-instance deployments
- **Streaming improvements** — SSE backpressure handling, chunked transfer optimizations
- **Provider expansion** — additional providers based on community requests

## v1.2.0

Status: Planning

### Priorities

- **SDK packages** — official Go, Python, and TypeScript client libraries
- **Webhook notifications** — configurable alerts for budget limits, error spikes, circuit breaker events
- **Plugin SDK** — external plugin loading for custom guardrails and transforms
- **Enhanced A/B testing** — metrics collection and winner determination for variant experiments

## Future

- Continue expanding provider coverage based on community demand
- Deepen production deployment guidance (Kubernetes operators, Terraform modules)
- Expand the [ai-gateway-examples](https://github.com/ferro-labs/ai-gateway-examples) repo
- Strengthen benchmark reporting and cross-gateway comparisons
