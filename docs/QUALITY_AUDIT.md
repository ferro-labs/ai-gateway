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
- [ ] **Phase 1 — Decompose `gateway.go`** (L): split 2351 → ~9 cohesive files <800; extract
      Route/RouteStream helpers; generic model-lookup; dedup MCP wiring; named constants.
- [ ] **Phase 2 — Provider cross-cutting dedup** (M): `openaicompat.PostEmbeddings`;
      `core.CoerceEmbeddingInput`; promote `APIError` to `core`; extend `internal/anthropicwire`; shared SSE framing.
- [ ] **Phase 3 — Split big provider files** (M): bedrock→family files; shared request/stream builders;
      `defaultMaxTokens` const; stray-godoc fixes.
- [ ] **Phase 4 — admin split & dedup** (M): `handlers.go` by resource; shared query-param parsing; `maskKey`.
- [ ] **Phase 5 — plugins + strategies dedup** (M): shared strategy `dispatch()`; shared plugin
      config decode+validate (**fixes silent maxtoken/cache config-fallback bug**); weighted-pick helper.
- [ ] **Phase 6 — CLI/bootstrap/handler wiring** (M): `adminClientFromCmd`/`printResult`; break up
      `Serve()`; shared `decodeJSONBody`; named consts. **+ from Phase 0:** uniform provider-init
      failure policy (env `os.Exit` vs Bedrock warn-only); Warn on invalid `RATE_LIMIT_RPS/BURST`
      instead of silently disabling; `router.ensureGateway` log/propagate the `New()` error.
- [ ] **Phase 7 — transport & streaming hygiene** (M): remove/wire dead transport metrics + tracing
      transport (~200 lines); extract `drainSrc` leak-guard; cache `ReverseProxy`.
- [ ] **Phase 8 — otel/mcp decomposition** (M): **span-error redaction bypass (from Phase 0)** —
      route MCP (`executor.go`) and plugin (`manager.go`) child-span errors through the privacy-aware
      redactor instead of raw `RecordError`/`SetStatus`; split `Init` + `ResolvePendingToolCalls`;
      single privacy-level validator; `defaultShutdownGrace` const; bound audit goroutines.
- [ ] **Phase 9 — doc & style hygiene** (S): rewrite stale `redact/doc.go`; document 4 metrics vars;
      `interface{}`→`any`; label unwired `Attr*` as Planned; godoc public `plugin` API.
- [ ] **Phase 10 — test determinism + lint hardening** (M): CircuitBreaker clock seam (kills 18
      sleeps); fix `sse_test` race / mcp-executor sleep / strategy self-skip; enable
      `dupl`/`funlen`/`file-length`/`nestif`/`errorlint`/`copyloopvar` in `.golangci.yml`.
- [ ] **Phase 11 — completeness gaps** (—): `web/` dashboard JS (~2,624 LOC, never audited) XSS/DOM
      review; `.github/workflows` action SHA-pinning + CodeQL; top-level `mcp/config.go`; release tooling.

_Tally: ~66 findings — 4 high (all file-size), ~26 medium, ~36 low._
