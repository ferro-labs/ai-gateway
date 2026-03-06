# Ferro Labs AI Gateway — Public Roadmap

> Ferro Labs AI Gateway is an open-source AI Gateway that routes requests across LLM
> providers with a single OpenAI-compatible API. This roadmap outlines the
> planned evolution from the initial v0.1.0 release through production-grade v1.0.

---

## v0.1.0 — Foundation Release

**Status**: ✅ Released  
**Theme**: A working gateway that people can download, configure, and run today.

### v0.1.0 hardening

| Gap | Action | Status |
|---|---|---|
| Missing Docker quickstart in README | Added build/run and compose commands | ✅ Done |
| Config-less startup bypassed strategy engine | Added default fallback strategy over discovered providers | ✅ Done |
| Streaming route selection ignored strategy mode | `RouteStream` now resolves providers by configured strategy mode | ✅ Done |
| CORS multi-origin behavior invalid | Origin-aware CORS response with allowlist matching | ✅ Done |
| Naming mismatch in docs | Aligned security doc naming to Ferro Labs AI Gateway | ✅ Done |

### What ships

| Category | Features |
|---|---|
| **Providers** | 10 LLM providers: OpenAI, Anthropic, Groq, Together AI, Google Gemini, Mistral, Azure OpenAI, Cohere, DeepSeek, Ollama (local) |
| **Routing** | 4 strategies: single, fallback (with retries + exponential backoff), weighted load balance, conditional (model-based) |
| **Streaming** | Server-Sent Events (SSE) streaming for all providers that support it |
| **Plugins** | Extensible plugin system with lifecycle hooks (before_request, after_request, on_error) |
| **Guardrails** | Built-in: word/phrase blocklist filter, max token enforcer |
| **Caching** | Exact-match response cache (in-memory LRU with TTL) |
| **API Keys** | In-memory API key management with scoped RBAC (admin, read_only) |
| **API** | OpenAI-compatible `/v1/chat/completions`, `/v1/models`, `/health` |
| **Config** | JSON and YAML config files with validation |
| **CLI** | `ferrogw-cli validate` and `ferrogw-cli plugins` commands |
| **Deployment** | Docker, docker-compose, single static binary |

---

## v0.2.0 — Observability & Resilience

**Status**: ✅ Released
**Theme**: Production visibility and operational confidence.

| Feature | Description | Status |
|---|---|---|
| **Structured logging** | JSON structured logs with trace IDs, latency, token counts | ✅ Done |
| **Prometheus metrics** | `/metrics` endpoint: request count, latency histograms, token usage, provider errors | ✅ Done |
| **Health checks** | Deep health checks per provider (ping/list models) | ✅ Done |
| **Rate limiting** | Per-key and per-provider rate limiting plugin (token bucket) | ✅ Done |
| **Circuit breaker** | Per-provider circuit breaker (auto-disable failing providers) | ✅ Done |
| **Request timeout** | Configurable per-provider and per-request timeouts | ✅ Done |
| **Graceful shutdown** | Drain in-flight requests on SIGTERM | ✅ Done |
| **Consistent error schema** | Unified `{"error":{"message","type","code"}}` format across all endpoints (admin, completions, proxy) | ✅ Done |
| **Streaming strategy unification** | `RouteStream` should use the configured strategy engine (fallback, load balance, conditional) instead of manual target walking | ✅ Done |
| **BaseProvider extraction** | Extract shared provider boilerplate (`Name`, `Models`, HTTP client) into an embeddable struct to reduce ~400 LOC duplication | ✅ Done |
| **File-backed key storage** | JSON or SQLite file persistence for API keys (survives restarts without external database) | ⏭️ Deferred to v0.4.0 |
| **EventPublisher refactor** | Replace the baked-in `EventPublisher` interface with a plugin/hook pattern to keep the core clean for OSS | ✅ Done |
| **Registry consolidation** | Unify `providers.Registry` with `Gateway`'s internal provider map to eliminate duplicate registration logic | ✅ Done |

### What ships

| Category | Features |
|---|---|
| **Observability** | slog JSON logs, `X-Request-ID` trace IDs, Prometheus `/metrics`, 7 built-in metrics |
| **Resilience** | Per-provider circuit breaker (3-state), token-bucket rate limiting (middleware + plugin) |
| **Health** | Deep `/health` returns per-provider status and model count |
| **Error handling** | Unified `{"error":{"message","type","code"}}` schema across all HTTP endpoints |
| **Internals** | `BaseProvider` embeddable struct, `ProviderSource` interface, `EventHookFunc` hooks |
| **Logging** | Single `logging.Logger` used everywhere — stdlib `log` removed, trace IDs in plugin logs |

---

## v0.3.0 — More Providers & Multi-Modal

**Status**: ✅ Released  
**Theme**: Broader provider coverage and beyond chat completions.

| Feature | Description | Status |
|---|---|---|
| **Embeddings API** | `/v1/embeddings` endpoint with provider routing | ✅ Done |
| **Image generation** | `/v1/images/generations` endpoint (DALL-E, Stability AI) | ✅ Done |
| **Additional providers** | AWS Bedrock, Perplexity, AI21, Fireworks AI, Replicate | ✅ Done |
| **Provider auto-discovery** | Auto-detect available models from provider APIs | ✅ Done |
| **Model aliasing** | Map friendly names (`fast`, `smart`, `cheap`) to specific models | ✅ Done |
| **Cost tracking** | Per-request cost calculation with provider pricing tables | ✅ Done |
| **Streaming test coverage** | End-to-end tests for streaming code paths across all providers | ✅ Done |
| **Proxy handler tests** | Tests for the reverse-proxy pass-through (`/v1/*` catch-all) | ✅ Done |

---

## v0.4.0 — Persistent Storage & Management API

**Status**: ✅ Released  
**Theme**: Move beyond in-memory state for production deployments.

| Feature | Description | Status |
|---|---|---|
| **SQLite storage** | Optional SQLite backend for API keys and config (zero-dependency) | ✅ Done |
| **PostgreSQL storage** | Optional PostgreSQL backend for larger deployments | ✅ Done |
| **Config API** | REST API to manage gateway config at runtime (CRUD) | ✅ Done |
| **API key management API** | Full CRUD with expiration, rotation, usage tracking | ✅ Done |
| **Request logging** | Persistent request/response log storage | ✅ Done |
| **Admin dashboard** | Minimal web UI for gateway status and config | ✅ Done |

---

## v0.4.5 — Model Catalog & Accurate Pricing

**Status**: ✅ Released  
**Theme**: Replace hardcoded pricing with a live 2500+ model catalog for accurate, per-request cost tracking.

| Feature | Description | Status |
|---|---|---|
| **Model catalog** | `models` package: typed catalog with 2531 entries loaded from an embedded JSON file (with remote refresh fallback) | ✅ Done |
| **Full pricing coverage** | Per-model input, output, cache-read, cache-write, reasoning, image, audio, and embedding prices (USD/1M tokens) | ✅ Done |
| **Cost calculator** | `models.Calculate()` dispatches by model mode; returns itemised `CostResult` with 9 cost buckets | ✅ Done |
| **Provider token extensions** | OpenAI extracts `reasoning_tokens` + `cached_tokens`; Anthropic extracts `cache_creation` / `cache_read` tokens from API responses | ✅ Done |
| **Gateway wiring** | `Gateway.New()` loads catalog; `Route()` calls `models.Calculate()` replacing `EstimateCost()` (streaming cost tracking is a v0.5 item) | ✅ Done |
| **Event cost breakdown** | `publishEvent` emits 9 cost fields: `cost_usd`, `cost_input_usd`, `cost_output_usd`, `cost_cache_read_usd`, `cost_cache_write_usd`, `cost_reasoning_usd`, `cost_image_usd`, `cost_audio_usd`, `cost_embedding_usd` | ✅ Done |
| **`/v1/models` enrichment** | Response enriched with `context_window`, `max_output_tokens`, `capabilities`, `status`, `deprecated` from catalog | ✅ Done |
| **Catalog CI check** | Weekly GitHub Action (`catalog-check.yml`) validates all remote pricing source URLs; opens an issue on failure | ✅ Done |
| **Remove hardcoded pricing** | `providers/pricing.go` deleted; catalog is the single source of truth | ✅ Done |

---

## v0.5.0 — Advanced Routing & Intelligence

**Status**: ✅ Released
**Theme**: Smart routing based on cost, latency, and content.

| Feature | Description | Status |
|---|---|---|
| **CLI UX overhaul** | `ferrogw-cli` migrated to Cobra: richer admin command groups (`admin keys`, `admin config`, `admin logs`, `admin providers`), `--format table/json/yaml` output flag, shell completions via `ferrogw-cli completion` | ✅ Done |
| **Streaming cost tracking** | `RouteStream()` now wraps the SSE channel in a metering goroutine; emits Prometheus metrics (duration, tokens, cost) and event hooks on stream close, matching `Route()` behaviour | ✅ Done |
| **Retry policies** | `RetryConfig` extended with `on_status_codes` (only retry listed HTTP status codes) and `initial_backoff_ms` (configurable exponential backoff base); applied per-target in the fallback strategy | ✅ Done |
| **Least-latency routing** | New `least-latency` strategy mode; in-process rolling-window p50 tracker (`internal/latency`) records observed latency per provider; routes to fastest compatible provider, falls back to random when no samples exist | ✅ Done |
| **Cost-optimized routing** | New `cost-optimized` strategy mode; estimates prompt cost via the model catalog for each compatible provider and routes to the cheapest; falls back to first compatible provider when pricing is unavailable | ✅ Done |

---

## v0.5.5 — Intelligent Request Handling

**Status**: 📋 Planned
**Theme**: Route based on what the request says, not just what model it targets.

| Feature | Description |
|---|---|
| **Content-based routing** | Extend conditional strategy with `prompt_contains` and `prompt_regex` match keys; `X-Route-Tag` header overrides all rules via `header_routing` config map |
| **A/B testing** | New `ab-test` strategy mode; traffic split by percentage across named variants; `variant` label on all Prometheus metrics; `GET /admin/experiments` endpoint for live stats |

---

## v0.6.0 — Developer Experience

**Status**: ✅ Completed  
**Release Date**: 2026-03-06  
**Theme**: Server-side prompt management to eliminate client-side template sprawl.

| Feature | Description |
|---|---|
| **Prompt templates** | First-class `PromptTemplate` entity with CRUD admin API (`/admin/templates`); `template_id` + `variables` fields in request body; Go `text/template` rendering injected into `messages` before routing; memory / SQLite / PostgreSQL backends |

---

## v1.0.0 — Production Ready

**Status**: 🔮 Future  
**Theme**: Enterprise-grade stability, security, and ecosystem.

| Feature | Description |
|---|---|
| **Semantic caching** | Embedding-based cache for semantically similar prompts |
| **PII redaction** | Regex + NER-based PII stripping (SSN, email, phone, etc.) |
| **Webhook notifications** | Notify external systems on events (errors, thresholds) |
| **OpenTelemetry** | Full distributed tracing with OTLP export |
| **Helm chart** | Production Kubernetes Helm chart with HPA |
| **SDK** | Official Go client SDK for embedding the gateway |
| **Plugin marketplace** | Community plugin registry and discovery |
| **Comprehensive docs** | Full documentation site with guides, API reference, examples |

---

## Contributing

We welcome contributions! Priority areas for community involvement:

1. **New providers** — Add support for additional LLM providers
2. **Plugins** — Build guardrails, transforms, or logging plugins
3. **Documentation** — Improve guides, examples, and API docs
4. **Testing** — Expand test coverage and add integration tests
5. **Bug fixes** — Report and fix issues

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

---
