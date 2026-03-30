# Ferro Labs AI Gateway — Contributing Guide

Thank you for contributing to Ferro Labs AI Gateway.

---

## Branching Strategy

```
main          — stable, always releasable, protected
develop       — integration branch for next release
feature/*     — new features (branch from develop)
fix/*         — bug fixes (branch from develop)
release/*     — release preparation (branch from develop)
hotfix/*      — critical production fixes (branch from main)
```

### Creating the v1.0.0 Release Branch

```bash
git checkout develop || git checkout main
git checkout -b release/v1.0.0
git push origin release/v1.0.0
```

---

## Pull Request Guidelines

- All PRs must target `develop` (never `main` directly)
- New providers: OSS repo ONLY — never add to FerroCloud
- Every PR requires:
  - Clear description of what and why
  - Tests for new functionality
  - Documentation update if behavior changes
- Breaking changes require an RFC issue first
- Keep commits atomic — one logical change per commit

---

## Adding a New Provider

1. Create `providers/<id>/<id>.go` — implement `core.Provider` and optional interfaces (`core.StreamProvider`, etc.)
2. Add `const Name = "<id>"` and re-export in `providers/names.go`
3. Add a `ProviderEntry` to `providers/providers_list.go`
4. Add `providers/<id>/<id>_test.go` — run `go test ./providers/...`
5. Add models to `models/catalog.json`
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

## Commit Convention (Conventional Commits)

```
feat: add support for Cerebras provider
fix: resolve connection pool exhaustion under high load
perf: reduce hot-path allocations with sync.Pool
docs: update benchmark results for v1.0.0
test: add integration tests for failover strategy
chore: bump Go version to 1.24
```

---

## Running Tests

```bash
go test ./...              # all tests
go test ./... -race        # with race detector
go test -run TestProvider  # specific test
go vet ./...               # vet
```

Integration tests require real provider API keys:

```bash
export OPENAI_API_KEY=sk-...
go test -v -race -timeout 60s ./... -run Integration
```

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
