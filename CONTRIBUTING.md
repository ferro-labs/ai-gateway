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
providers/
  core/             # Shared interfaces (contracts.go) + types (chat, stream, embedding, image, model)
  <id>/             # One subpackage per provider — <id>/<id>.go + <id>/<id>_test.go  (19 providers)
  factory.go        # ProviderConfig, ProviderEntry types, CfgKey*/Capability* consts, lookup funcs
  providers_list.go # allProviders slice — all 19 ProviderEntry registrations
  names.go          # NameXxx constants (re-exported from each subpackage)
  registry.go       # Runtime provider lookup by name
  facade_aliases.go # Type aliases for backwards compatibility
plugin/             # Public: Plugin interface, Stage constants, Context
cmd/
  ferrogw/          # Server binary (main.go, cors.go, proxy.go, completions.go)
  ferrogw-cli/      # CLI helper
internal/
  admin/            # HTTP admin API (key management, dashboard, usage, logs, config history)
  cache/            # In-process response cache implementation
  circuitbreaker/   # Per-provider circuit breaker
  discovery/        # Shared OpenAI-compatible model discovery helper
  latency/          # Latency tracking for least-latency strategy
  metrics/          # Prometheus metrics
  strategies/       # Routing strategies (single, fallback, loadbalance, leastlatency, costoptimized, conditional)
  plugins/
    budget/         # per-key budget controls
    cache/          # response-cache plugin
    logger/         # request-logger plugin
    maxtoken/       # max-token guardrail
    ratelimit/      # rate limiting
    wordfilter/     # word-filter guardrail
```

Runnable examples are maintained separately in
`github.com/ferro-labs/ai-gateway-examples`.

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

Since v0.5 all provider implementations live in a dedicated subpackage.
**No changes to `cmd/ferrogw/main.go` are needed** — the gateway auto-registers all entries in `providers/providers_list.go`.

Adding a new provider is a **4-step process**:

1. **Create `providers/<id>/<id>.go`** — package should be named after the ID
   (e.g. `package groq`). Import `providers/core` for interfaces and types.
   Define `const Name = "<id>"` and add compile-time assertions:
   ```go
   const Name = "myprovider"

   var (
       _ core.Provider       = (*Provider)(nil)
       _ core.StreamProvider = (*Provider)(nil) // if streaming is supported
   )
   ```

2. **Re-export the `Name` constant in `providers/names.go`**:
   ```go
   import mypkg "github.com/ferro-labs/ai-gateway/providers/myprovider"
   const NameMyProvider = mypkg.Name
   ```
   Also add it to the `AllProviderNames()` return slice (alphabetical order).

3. **Add a `ProviderEntry` to `providers/providers_list.go`** (`allProviders` slice,
   alphabetical by ID) — fill in `ID`, `Capabilities`, `EnvMappings`, and `Build`:
   ```go
   {
       ID:           NameMyProvider,
       Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy},
       EnvMappings: []EnvMapping{
           {CfgKeyAPIKey, "MYPROVIDER_API_KEY", true},
           {CfgKeyBaseURL, "MYPROVIDER_BASE_URL", false},
       },
       Build: func(cfg ProviderConfig) (Provider, error) {
           return mypkg.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
       },
   },
   ```

4. **Add `providers/<id>/<id>_test.go`** and run the stability tests:
   ```bash
   go test ./providers/...
   ```
   The tests in `providers/stability_test.go` automatically catch missing registry
   entries, empty capability lists, and name constant drift.

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
