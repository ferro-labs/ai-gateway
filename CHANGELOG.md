# Changelog

All notable changes to Ferro Labs AI Gateway are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0-rc.3] - 2026-03-23

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
- **Per-provider HTTP clients**: All 28 providers now use
  `httpclient.ForProvider(Name)` for isolated connection pools instead of a
  single shared client. Legacy completions handler switched from
  `http.DefaultClient`.
- **sync.Pool for request structs**: `routeChatCompletionRequest` (19-field
  reset) and `plugin.Context` (metadata map capacity preserved) are now
  pooled. All fields explicitly reset before pool return for multi-tenant
  safety.
- **Pooled JSON marshaling buffers**: Added `core.MarshalJSON` and
  `core.JSONBodyReader` backed by `sync.Pool`. All 28 provider subpackages
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

## [1.0.0-rc.2] - 2026-03-18

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

## [1.0.0-rc.1] - 2026-03-14

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
