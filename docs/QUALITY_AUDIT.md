# Code Quality Audit & Remediation Roadmap

A phased, **behavior-preserving** hygiene plan for the gateway. No new features. Each phase is
independently shippable; `make fmt lint test` must stay green after every phase.

> Baseline (2026-07-01): 67,909 LOC Go · `golangci-lint` reports **0 issues** · no SQL injection ·
> auth uses constant-time compare. Everything below is maintainability, duplication, or a small
> set of real correctness/security items — not lint/compile failures.

## Metrics at a glance

**Files over the 800-line house max**

| File | LOC | Over |
|---|---|---|
| `gateway.go` | 2351 | 2.9× |
| `providers/bedrock/bedrock.go` | 1131 | 1.4× |
| `internal/admin/handlers.go` | 1089 | 1.4× |
| `providers/gemini/gemini.go` | 794 | borderline |

**Functions over the <50-line guideline:** `RouteStream` 272 · anthropic `CompleteStream` 217 ·
`Route` 210 · bedrock `CompleteStream` 156 · `bootstrap.Serve` 150 · admin `keyUsage` 139 ·
`logsStats` 123 · `mcp.ResolvePendingToolCalls` 105 · `otel.Init` 95.

**Top duplication:** 9 near-identical `embedding.go` (~115 lines each) · 37 inline non-200 error
blocks across 25 files (`openaicompat.APIError` already exists) · ~15 embedding-input coercion
copies · Anthropic wire duplicated anthropic↔bedrock · ~8 strategy-dispatch copies · 13–16 CLI
client-ritual copies.

## Phases

- [x] **Phase 0 — Correctness & security quick wins** (S) ✅ _(commit `38ce13f`)_: bound MCP
      success-body read (`client.go`, 10 MiB cap); admin `ErrKeyNotFound` sentinel so real store
      failures return 500 not 404, and stop leaking wrapped error text; regression tests added.
      _Re-scoped out for correct grouping:_ the MCP/plugin **span-error redaction bypass** moved to
      **Phase 8** (needs proper redaction routing + tests), and **bootstrap init/ratelimit + router
      `ensureGateway`** error handling moved to **Phase 6** (which already refactors that file).
- [x] **Phase 1 — Decompose `gateway.go`** ✅ (L): split into 8 cohesive files, all <800;
      generic model-lookup; `targetKeys`/MCP-wiring dedup; named constants. **gateway.go 2351 → 482.**
      - [x] 1/7 `gateway_modelindex.go` _(`2f694ad`)_ — model index + generic `findByModelLocked[T]`
        / `indexModelsIfImplements[T]`. **2351 → 2201**.
      - [x] 2/7 `gateway_hooks.go` _(`f284043`)_ — async hook workers + event builders +
        `maxHookWorkers` const. **2201 → 2029**.
      - [x] 3/7 `gateway_circuitbreaker.go` _(pure move)_ — cbProvider + CB helpers. **2029 → 1927**.
      - [x] 4/7 `gateway_discovery.go` _(pure move)_ — alias/multimodal/discovery. **1927 → 1796**.
      - [x] 5/7 `gateway_strategy.go` _(pure move)_ — strategy build + condition helpers. **1796 → 1582**.
      - [x] 6/7 `gateway_stream.go` — RouteStream + streaming helpers; `targetKeys` dedup. **1582 → 916**.
      - [x] 7/7 `gateway_route.go` (Route path) + `gateway_mcp.go` (`wireMCPLocked` dedup +
        `mcpInitTimeout` const + stray-doc fix). **916 → 482.**
      - _Deferred (noted): Route's internal helper extraction — its blocks return mid-body / mutate
        shared locals, so not trivially behavior-preserving. Revisit if Route is touched again._
      - _Observed: root `-race` suite runs ~144s vs `make test`'s 180s timeout — flaky-timeout risk → Phase 10._
- [x] **Phase 2 — Provider cross-cutting dedup** ✅ (M): `openaicompat.PostEmbeddings`;
      `core.CoerceEmbeddingInput`; `core.APIError`; `anthropicwire` messages/tool-choice; `core.SSEDataLines`.
      Net −507 lines across 25 files; only byte-for-byte-compatible providers migrated (cohere/ai21/
      replicate/openai/databricks/nvidia_nim/azure/ollama deliberately left as-is).
- [x] **Phase 3 — Split big provider files** ✅ (M): bedrock 1131→333 across 6 family files; shared
      `buildBedrockAnthropicRequest`/`buildAnthropicRequest`/gemini `doJSONRequest`+`parseCandidateParts`
      builders; named max-tokens consts. (invokeModelJSON routing skipped — would add an Accept header.)
- [x] **Phase 4 — admin split & dedup** ✅ (M): `handlers.go` 1089→73 across 5 resource files; shared
      `queryparams` helpers; `maskKey` (const prefix); reuse of `generateAPIKeyString`/`generateID`.
- [x] **Phase 5 — plugins + strategies dedup** ✅ (M): shared strategy `dispatch()` + generic
      `weightedPick[T]`; shared `plugincfg` numeric decode. _(maxtoken/cache silent-config-fallback is a
      real bug but a behavior change → left flagged for the owner, not silently "fixed" in a hygiene pass.)_
- [x] **Phase 6 — CLI/bootstrap/handler wiring** ✅ (M): `adminClientFromCmd`/`printResult`; `Serve()`
      split into build/run/shutdown; shared `decodeJSONBody`; named listen/drain consts; Warn on invalid
      `RATE_LIMIT_RPS/BURST`; `ensureGateway` logs the `New()` error.
      ⚠️ **DEFERRED for owner decision:** uniform provider-init failure policy (env `os.Exit` vs Bedrock
      warn-only) — changes runtime behavior, so kept out of the hygiene commits.
- [x] **Phase 7 — transport & streaming hygiene** ✅ (M): removed dead transport metrics + tracing
      transport (3 files); extracted `drainSrc` leak-guard. (ReverseProxy caching skipped — not trivially
      behavior-preserving.)
- [x] **Phase 8 — otel/mcp decomposition** ✅ (M): **span-error redaction bypass fixed** — MCP + plugin
      child-span errors now go through the privacy redactor (`otel.RecordSpanError`); `Init` +
      `ResolvePendingToolCalls` decomposed; single `observability.ValidatePrivacyLevel`;
      `defaultShutdownGrace` const. (Audit-goroutine bound left documented, not pooled.)
- [x] **Phase 9 — doc & style hygiene** ✅ (S): rewrote stale `redact/doc.go`; documented 4 metrics vars;
      `interface{}`→`any` project-wide (61 files); labelled unwired `Attr*` as Planned; godoc'd public
      `plugin` API; named `requestlog` pagination consts.
- [ ] **Phase 10 — test determinism + lint hardening** (M): CircuitBreaker clock seam (kills 18
      sleeps); fix `sse_test` race / mcp-executor sleep / strategy self-skip; enable
      `dupl`/`funlen`/`file-length`/`nestif`/`errorlint`/`copyloopvar` in `.golangci.yml`.
- [ ] **Phase 11 — completeness gaps** (—): `.github/workflows` action SHA-pinning + CodeQL;
      top-level `mcp/config.go`; release tooling (`.goreleaser.yaml`, `.husky/`, `scripts/`).
      _(`web/` dashboard is intentionally out of scope — owner has a separate plan for it.)_

_Tally: ~66 findings — 4 high (all file-size), ~26 medium, ~36 low._
