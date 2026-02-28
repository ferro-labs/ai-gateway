# Ferro AI Gateway — Public Roadmap

> Ferro AI Gateway is an open-source AI Gateway that routes requests across LLM
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
| Naming mismatch in docs | Aligned security doc naming to Ferro AI Gateway | ✅ Done |

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

**Status**: 📋 Planned  
**Theme**: Broader provider coverage and beyond chat completions.

| Feature | Description |
|---|---|
| **Embeddings API** | `/v1/embeddings` endpoint with provider routing |
| **Image generation** | `/v1/images/generations` endpoint (DALL-E, Stability AI) |
| **Additional providers** | AWS Bedrock, Perplexity, AI21, Fireworks AI, Replicate |
| **Provider auto-discovery** | Auto-detect available models from provider APIs |
| **Model aliasing** | Map friendly names (`fast`, `smart`, `cheap`) to specific models |
| **Cost tracking** | Per-request cost calculation with provider pricing tables |
| **Streaming test coverage** | End-to-end tests for streaming code paths across all providers |
| **Proxy handler tests** | Tests for the reverse-proxy pass-through (`/v1/*` catch-all) |

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

## v0.5.0 — Advanced Routing & Intelligence

**Status**: 📋 Planned  
**Theme**: Smart routing based on cost, latency, and content.

| Feature | Description |
|---|---|
| **CLI UX overhaul** | Improve `ferrogw-cli` with richer admin command groups, clearer help output, structured output modes (`table/json/yaml`), and shell completions |
| **Least-latency routing** | Route to the provider with lowest p50 latency |
| **Cost-optimized routing** | Route to cheapest provider that meets quality threshold |
| **Content-based routing** | Route based on prompt content (code → Codex, chat → GPT) |
| **A/B testing** | Split traffic between models for comparison |
| **Prompt templates** | Server-side prompt template management and versioning |
| **Retry policies** | Configurable retry with status code filtering per provider |

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
