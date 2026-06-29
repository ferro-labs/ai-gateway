# Ferro Labs AI Gateway — Contributing Guide

Thank you for contributing to Ferro Labs AI Gateway.

---

## Branching Strategy

```
main          — stable, always releasable, protected
release/*     — integration branch for the next release (branch from main)
feature/*     — new features (branch from the active release/* branch)
fix/*         — bug fixes (branch from the active release/* branch)
hotfix/*      — critical production fixes (branch from main)
```

Work for a release lands on its `release/*` branch via short-lived
`feature/*` or `fix/*` branches; the release branch is merged into `main`
when the release ships.

### Creating a Release Branch

```bash
git checkout main
git checkout -b release/v1.0.0
git push origin release/v1.0.0
```

---

## Pull Request Guidelines

- All PRs must target the active `release/*` branch (never `main` directly)
- New providers: OSS repo ONLY — never add to FerroCloud
- Every PR requires:
  - Clear description of what and why
  - Tests for new functionality
  - Documentation update if behavior changes
- Breaking changes require an RFC issue first
- Keep commits atomic — one logical change per commit

---

## Local Development

### Prerequisites

- **Go 1.25+** — the toolchain version is pinned in [`go.mod`](go.mod) (`go 1.25.0`).
- **golangci-lint** — install via the [official instructions](https://golangci-lint.run/welcome/install/). CI pins v2; match that locally.

### Running Checks

All checks are wrapped as Make targets (see the [`Makefile`](Makefile)) — run them rather than memorising the exact commands:

- `make fmt` — formats the tree with `gofmt -s -w`.
- `make vet` — runs `go vet ./...`.
- `make lint` — runs `golangci-lint`.
- `make lint-fix` — runs `golangci-lint run --fix` to auto-apply fixable lint issues.
- `make test` — unit tests with the race detector (`-short -race`). See [Running Tests](#running-tests) for the integration/live suites.
- `make precommit` — convenience target: `fmt` + `test`.

### Pre-Commit Hook

The repo ships git hooks in [`.husky/`](.husky) (plain POSIX shell scripts — no Node/husky tooling required). They are **not** active until you point git at them once per clone:

```bash
git config core.hooksPath .husky
```

- `pre-commit` runs **fmt → vet → lint** (fast, no tests).
- `pre-push` runs the **unit test suite** (`go test -short -race`).

### CI Checks

`.github/workflows/ci.yml` runs on pushes and PRs to `main` and must pass before merge:

- **Test** — `go mod verify`, a `gofmt -l` formatting gate, `go build ./...`, `go test -short -race`, and a coverage upload to Codecov.
- **Integration** — `make test-integration`.
- **Vulnerability** — `govulncheck ./...`.
- **Lint** — `golangci-lint` (v2).

The workflow sets `paths-ignore: ["**.md", "docs/**"]`, so Markdown- and docs-only
changes do **not** trigger CI. A docs-only PR (for example, one touching only
`*.md` files or `docs/`) will show no CI run — that is expected, not a failure.

---

## Adding a New Provider

1. Create `providers/<id>/<id>.go` — implement `core.Provider` and optional interfaces (`core.StreamProvider`, etc.)
2. Add `const Name = "<id>"` and re-export in `providers/names.go`
3. Add a `ProviderEntry` to `providers/providers_list.go`
4. Add `providers/<id>/<id>_test.go` — run `go test ./providers/...`
5. Add or update model metadata in `ferro-labs/model-catalog`
6. Update the provider table in README.md
7. Add a `{ "virtual_key": "<id>" }` entry to `config.example.json` and a `- virtual_key: <id>` line to `config.example.yaml`
8. Add the provider's env var(s) (commented out) to `docker-compose.yml`
9. Add an example in [ferro-labs/ai-gateway-examples](https://github.com/ferro-labs/ai-gateway-examples)

**Important:** Providers go in the OSS repo only. Never add provider integrations to FerroCloud.

---

## Adding a Plugin

1. Create `internal/plugins/<name>/<name>.go` implementing `plugin.Plugin`
2. Register via `plugin.RegisterFactory(...)` in `init()`
3. Add blank import in `cmd/ferrogw/main.go`: `_ "github.com/ferro-labs/ai-gateway/internal/plugins/<name>"`
4. Plugin config is passed as `map[string]interface{}` to `Init()`

See `internal/plugins/wordfilter/wordfilter.go` for a minimal example.

---

## Observability Exporters

Exporters bridge gateway events (`gateway.request.completed` / `.failed`) to a
backend (LangSmith, Langfuse, Datadog, New Relic, …). They live in a separate
repository and are linked into a binary at build time — self-register via
`init()` → `observability.RegisterExporter(name, factory)`, then blank-imported,
exactly like plugins. The gateway ships the contract and wiring; no exporters
are built in.

When implementing or modifying anything on the `observability.Exporter` pathway,
follow these rules:

1. Implement `observability.Exporter`: `Name`, `Init(ctx, cfg)`,
   `Export(ctx, Event)`, `Shutdown(ctx)`. Do auth/connection work in `Init` and
   honour its context; `Export` must be safe for concurrent use.
2. **The `Exporter` and `Event` contracts are stable.** Never change the
   signatures of `Exporter`'s methods, and never remove or retype fields on
   `Event` / `RequestAttrs` / `CostBreakdown` — plugins in other repos depend on
   them.
3. **Evolve additively only.** Carry new per-event data through
   `Event.Attributes map[string]any`; add new capabilities as **new optional
   interfaces** discovered via type assertion (the way `EventRecordingProvider`
   extends `Provider`) — never by adding methods to an existing interface.
4. Events are delivered to exporters **asynchronously, off the request path**
   (buffered; dropped under sustained backpressure). Never assume `Export` runs
   inline with the request, and keep it from blocking indefinitely.
5. Use the `gen_ai.*` / `ferro.*` attribute-name constants in
   `observability/attributes.go` — don't hardcode attribute strings. Mark
   not-yet-emitted attributes as Planned.

The gateway exports spans via OTLP to an external collector; it does not store
traces itself.

---

## Commit Convention (Conventional Commits)

```
feat: add support for Cerebras provider
fix: resolve connection pool exhaustion under high load
perf: reduce hot-path allocations with sync.Pool
docs: update benchmark results for v1.0.0
test: add integration tests for failover strategy
chore: bump Go version to 1.25
```

---

## Running Tests

The gateway has three test suites with separate build tags and Make targets.

### Unit tests (no build tag)

```bash
make test           # go test -v -short -race ./...
make test-coverage  # with HTML coverage report
```

### Integration tests (build tag: `integration`)

Spin up an in-process gateway with stub providers — no real LLM calls. The
`test/integration/` root package uses testcontainers-go for a real Postgres 16
container; the `http/`, `plugins/`, and `strategies/` sub-packages run without
Docker.

```bash
make test-integration   # go test -tags=integration -race -timeout 180s ./test/integration/...
```

### How to add an integration test

1. Choose the right sub-package:
   - `test/integration/http/` — HTTP routing, proxy pass-through, auth
   - `test/integration/plugins/` — plugin chain behaviour (word-filter, cache, rate-limit, etc.)
   - `test/integration/strategies/` — routing strategy behaviour (fallback, load-balance, least-latency, etc.)
   - `test/integration/` (root) — admin store, config store, request log persistence (uses Postgres)

2. Add the build-tag header at the top of every new file:

   ```go
   //go:build integration
   // +build integration
   ```

3. Use the shared `stubProvider` in `test/integration/http/stub_provider_test.go` (or
   `internal/testutil/stub_provider.go` if one is added later) to simulate provider
   responses without real API calls.

4. Run your new test to verify it compiles and passes:

   ```bash
   go test -tags=integration -race -run TestYourNewTest ./test/integration/...
   ```

5. Make sure `go test ./...` (no tag) still compiles cleanly — integration files
   must not leak symbols into the unit-test build.

---

## Code Style

- Standard Go formatting (`gofmt`)
- `context.Context` on every DB/HTTP call
- `fmt.Errorf` with `%w` for error wrapping
- Interfaces over concrete types for testability
- `go func()` for async work — use `context.Background()` for detached goroutines that outlive the request; derive from the request context for goroutines scoped to the request lifetime
- All exported types and functions must have a godoc comment

---

## Architecture Rules

- Prefer the Go standard library first
- No DI frameworks, ORM frameworks, or generic middleware frameworks
- Keep provider SDK usage isolated to provider packages
- Keep SQL drivers isolated to storage packages
- Add interfaces only when there is a real consumer-side boundary

See the full dependency policy in the [Architecture Rules](AGENTS.md#architecture--design-patterns) section of AGENTS.md.

---

## Reporting Issues

- **Bug reports:** [GitHub Issues](https://github.com/ferro-labs/ai-gateway/issues) with reproduction steps
- **Security issues:** security@ferrolabs.ai (do not open a public issue)
- **Feature requests:** [GitHub Discussions](https://github.com/ferro-labs/ai-gateway/discussions)

---

## Code of Conduct

Please follow our [Code of Conduct](CODE_OF_CONDUCT.md).
