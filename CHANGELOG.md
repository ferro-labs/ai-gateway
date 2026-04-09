# Changelog

All notable changes to Ferro Labs AI Gateway are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

---

## [1.0.4] — 2026-04-09

### Security

- **Dependabot: AWS SDK EventStream decoder DoS**: Upgraded `github.com/aws/aws-sdk-go-v2/service/bedrockruntime` from `v1.50.1` to `v1.50.4` and `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` from `v1.7.6` to `v1.7.8` to address GitHub advisory `GHSA-xmrv-pmrh-hhx2`.

### Fixed

- **Cache eviction at capacity** fixes [#43](https://github.com/ferro-labs/ai-gateway/issues/43)): When the response cache reached `max_entries`, new responses could be dropped permanently. The cache now evicts the earliest-expiring entry first so new responses continue to be stored, with regression coverage for deterministic eviction order.

### Improved

- **README polish**: Refined the README content and release documentation for the `v1.0.4` maintenance release.

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
- **`.env.example`**: Full reference file documenting `MASTER_KEY`, all 29 provider API keys, storage backends, rate limiting, and CORS origins. Bootstrap env vars marked deprecated.

### Changed

- **Single binary**: `ferrogw-cli` has been merged into `ferrogw` as Cobra subcommands (`doctor`, `status`, `admin`, `validate`, `plugins`, `version`). Running `ferrogw` with no subcommand still starts the server (backward compatible).
- **`proxyAuth`**: `/v1/*` routes now enforce `AuthMiddleware` by default and are only open when `ALLOW_UNAUTHENTICATED_PROXY=true` is set for local development. Operational endpoints such as `/metrics`, `/debug/vars`, and `/debug/pprof/*` continue to require auth.
- **Enhanced startup banner**: Shows top-5 provider status, masked master key, key store / config store backends, and a warning when deprecated bootstrap keys are in use.
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

- **Config examples**: Added all 29 providers as targets, conditional routing examples, retry/circuit-breaker config, and previously undocumented plugin fields (`max_input_length`, `burst`, `max_keys`).
- **`docker-compose.yml`**: Refactored to shared base config with commented env var stubs for all 29 providers.
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
