# Contributing to Ferro Labs AI Gateway

Thank you for your interest in contributing! This document covers everything you need to get started.

## Table of Contents

- [Development Setup](#development-setup)
- [Project Structure](#project-structure)
- [Making Changes](#making-changes)
- [Testing](#testing)
- [Submitting a Pull Request](#submitting-a-pull-request)
- [Adding a New Provider](#adding-a-new-provider)
- [Writing a Plugin](#writing-a-plugin)
- [Code Style](#code-style)

---

## Development Setup

**Prerequisites:** Go 1.24+

```bash
git clone https://github.com/ferro-labs/ai-gateway.git
cd ai-gateway
go build ./...
go test ./...
```

No external services are required to build or run unit tests.

---

## Project Structure

```sh
gateway.go          # Gateway type — main entry point for library users
config.go           # Config, Target, StrategyMode types
config_load.go      # LoadConfig / ValidateConfig
providers/          # Public: Provider & StreamProvider interfaces + shared types
plugin/             # Public: Plugin interface, Stage constants, Context
cmd/
  ferrogw/          # Server binary (main.go, cors.go, events_oss.go)
  ferrogw-cli/      # CLI helper
internal/
  admin/            # HTTP admin API (key management, model listing)
  cache/            # In-process response cache implementation
  strategies/       # Routing strategies (single, fallback, loadbalance, conditional)
  plugins/
    cache/          # response-cache plugin
    logger/         # request-logger plugin
    maxtoken/       # max-token guardrail plugin
    wordfilter/     # word-filter guardrail plugin
examples/           # Runnable examples
```

---

## Making Changes

1. Fork the repository and create a feature branch:

   ```bash
   git checkout -b feat/my-feature
   ```

2. Make your changes and add tests.
3. Ensure the full test suite and race detector pass:

   ```bash
   go test -race ./...
   ```

4. Run `go vet ./...` — zero warnings expected.

---

## Testing

Unit tests cover all packages and can run without any API keys or network access:

```bash
# Run all tests
go test ./...

# Run with race detector (required before submitting a PR)
go test -race ./...

# Run tests for a specific package
go test ./internal/strategies/...
```

Provider integration tests require real API keys and are gated behind the `-short` flag (if `-short` is set, integration tests are skipped).

---

## Submitting a Pull Request

- Keep PRs focused — one feature or fix per PR.
- Update `CHANGELOG.md` under `[Unreleased]`.
- All CI checks must be green before a PR will be reviewed.
- PRs that drop test coverage will not be merged.

---

## Adding a New Provider

Since v0.6.5 all provider implementations live in a dedicated subpackage.
Adding a new provider is a **5-step process with zero changes to `main.go`**:

1. **Create `providers/<id>/impl.go`** — package should be named after the ID
   (e.g. `package groq`). Import `providers/core` for interfaces and types.
   Add compile-time assertions at the package level:
   ```go
   var (
       _ core.Provider       = (*Provider)(nil)
       _ core.StreamProvider = (*Provider)(nil) // if streaming is supported
   )
   ```

2. **Add a `Name<ID>` constant to `providers/names.go`** — this string is
   an immutable data contract (persisted in routing configs). Choose carefully.

3. **Add a `ProviderEntry` to `providers/factory.go`** (`allProviders` slice) —
   fill in `ID`, `Capabilities`, `EnvMappings`, and `Build`. The gateway
   auto-registers it; `main.go` needs no changes.

4. **Add a root constructor wrapper in `providers/<id>.go`** (optional but
   recommended for backwards compatibility):
   ```go
   import idpkg "github.com/ferro-labs/ai-gateway/providers/<id>"
   type <ID>Provider = idpkg.Provider
   func New<ID>(apiKey, baseURL string) (*<ID>Provider, error) { return idpkg.New(apiKey, baseURL) }
   ```

5. **Run `go test ./providers/...`** — the stability tests in
   `providers/stability_test.go` automatically catch missing registry entries,
   empty capability lists, or name constant drift.

See `providers/groq/impl.go` for a minimal example and `providers/groq.go` for
the corresponding root wrapper.

---

## Writing a Plugin

1. Create `internal/plugins/<name>/<name>.go` with `package <name>`.
2. Implement the `plugin.Plugin` interface:

   ```go
   type Plugin interface {
       Name() string
       Type() plugin.PluginType
       Init(config map[string]interface{}) error
       Execute(ctx context.Context, pctx *plugin.Context) error
   }
   ```

3. Register a factory in an `init()` function:

   ```go
   func init() {
       plugin.RegisterFactory("my-plugin", func() plugin.Plugin { return &MyPlugin{} })
   }
   ```

4. Add a blank import in `cmd/ferrogw/main.go`:

   ```go
   _ "github.com/ferro-labs/ai-gateway/internal/plugins/myplugin"
   ```

See `internal/plugins/wordfilter/wordfilter.go` for a simple example.

---

## Code Style

- Follow standard Go formatting (`gofmt`).
- All exported types and functions must have a godoc comment.
- Avoid global mutable state. Use the plugin registry pattern (`plugin.RegisterFactory`) for plugin registration only.
- Handle `float64` config values from JSON by checking both `float64` and `int` type assertions (see `internal/plugins/maxtoken/maxtoken.go`).
