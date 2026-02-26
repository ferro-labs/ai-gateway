# CLAUDE.md

## Project Overview

**Ferro AI Gateway** is a high-performance, open-source AI gateway written in Go. It acts as a unified routing layer between applications and 100+ LLM providers (OpenAI, Anthropic, Gemini, Mistral, etc.), offering smart routing, plugin middleware, and API key management — all with an OpenAI-compatible API and transparent pass-through proxy.

- **Module**: `github.com/ferro-labs/ai-gateway`
- **Go version**: 1.24+
- **License**: Apache 2.0

---

## Build, Test, and Run Commands

```bash
# Build
make build          # builds ./bin/ferrogw
make build-cli      # builds ./bin/ferrogw-cli
make all            # fmt + lint + test + coverage + build

# Run
make run            # requires at least one provider key, e.g. OPENAI_API_KEY=sk-...

# Test
make test           # unit tests
make test-coverage  # with coverage report
make test-integration  # requires provider API keys

# Code quality
make fmt            # gofmt
make lint           # golangci-lint
make precommit      # fmt + test

# Docker
docker-compose up   # local dev environment
```

---

## Project Structure

```sh
ai-gateway/
├── cmd/
│   ├── ferrogw/          # HTTP server entry point
│   │   ├── main.go       # Server setup, provider registration, router
│   │   ├── cors.go       # CORS middleware
│   │   ├── completions.go # Legacy /v1/completions handler
│   │   └── proxy.go      # Pass-through proxy for /v1/*
│   └── ferrogw-cli/      # CLI management tool
│       └── main.go
├── internal/
│   ├── admin/            # API key management + auth middleware
│   ├── cache/            # Cache interface + in-memory implementation
│   ├── plugins/          # Built-in plugin implementations
│   │   ├── cache/        # Request/response caching
│   │   ├── logger/       # Request/response logging
│   │   ├── maxtoken/     # Token/message limit guardrail
│   │   └── wordfilter/   # Blocked word guardrail
│   ├── strategies/       # Routing strategy implementations
│   └── version/
├── plugin/               # Public plugin framework (interfaces + manager + registry)
├── providers/            # LLM provider implementations + registry
├── examples/
├── docs/
├── gateway.go            # Core Gateway struct and orchestration
├── config.go             # Config structs (Config, Strategy, Target, Plugin)
├── config_load.go        # LoadConfig(), ValidateConfig()
├── config.example.yaml
└── config.example.json
```

---

## Key Files

| File | Role |
|------|------|
| `gateway.go` | Core `Gateway` struct — routing, plugin lifecycle, strategy execution |
| `config.go` | Config schema: `Config`, `StrategyConfig`, `Target`, `PluginConfig` |
| `config_load.go` | `LoadConfig()` and `ValidateConfig()` for YAML/JSON |
| `providers/provider.go` | `Provider`, `StreamProvider`, `ProxiableProvider` interfaces |
| `plugin/plugin.go` | `Plugin` interface, `PluginType`, `Stage`, `Context` |
| `plugin/manager.go` | Plugin lifecycle: before/after/error stage execution |
| `internal/strategies/strategy.go` | `Strategy` interface |
| `cmd/ferrogw/main.go` | HTTP server setup and entry point |
| `internal/admin/middleware.go` | Bearer token auth middleware |

---

## Architecture & Design Patterns

- **Strategy Pattern**: Routing strategies (`Single`, `Fallback`, `LoadBalance`, `Conditional`) all implement `Strategy` interface in `internal/strategies/`
- **Factory/Registry Pattern**: `providers/registry.go` and `plugin/registry.go` for loose coupling
- **Plugin Middleware**: `plugin/manager.go` runs plugins at `before_request`, `after_request`, `on_error` stages
- **OpenAI Compatibility**: All requests/responses match OpenAI spec — other provider responses are translated
- **Pass-Through Proxy**: Unhandled `/v1/*` endpoints forwarded transparently via `cmd/ferrogw/proxy.go`
- **Blank import registration**: Providers and plugins self-register via `init()` using `_` imports

### Request Flow

```sh
Client → HTTP Router → before_request plugins → Strategy selection
  → Provider.Complete() / CompleteStream() → after_request plugins → Response
```

### Concurrency

- `sync.RWMutex` in `Gateway` for thread-safe reads/writes
- Streaming uses `<-chan providers.StreamChunk` channels
- Async event dispatch via goroutines

---

## Configuration

Config is loaded from YAML or JSON (auto-detected). Path defaults from env var `GATEWAY_CONFIG`.

```yaml
strategy:
  mode: fallback  # single | fallback | loadbalance | conditional

targets:
  - virtual_key: openai
    weight: 1.0
    retry:
      attempts: 3
  - virtual_key: anthropic
    weight: 1.0

plugins:
  - name: word-filter
    type: guardrail
    stage: before_request
    enabled: true
    config:
      blocked_words: ["password", "secret"]
```

### Key Environment Variables

| Variable | Purpose |
|----------|---------|
| `GATEWAY_CONFIG` | Path to config YAML/JSON |
| `PORT` | Server port (default: 8080) |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `GROQ_API_KEY` | Groq API key |
| `MISTRAL_API_KEY` | Mistral API key |
| `TOGETHER_API_KEY` | Together AI API key |
| `COHERE_API_KEY` | Cohere API key |
| `DEEPSEEK_API_KEY` | DeepSeek API key |
| `AZURE_OPENAI_API_KEY` | Azure OpenAI API key |
| `AZURE_OPENAI_ENDPOINT` | Azure OpenAI endpoint URL |
| `AZURE_OPENAI_DEPLOYMENT` | Azure deployment name |
| `AZURE_OPENAI_API_VERSION` | Azure API version |
| `OLLAMA_HOST` | Ollama server URL |
| `OLLAMA_MODELS` | Comma-separated Ollama model list |
| `CORS_ORIGINS` | Comma-separated allowed CORS origins |

---

## HTTP API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/v1/models` | GET | List all available models |
| `/v1/chat/completions` | POST | Chat completion (supports `stream: true`) |
| `/v1/completions` | POST | Legacy text completion |
| `/v1/*` | Any | Pass-through proxy to provider |
| `/admin/keys` | GET, POST | API key management (requires auth) |

---

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/go-chi/chi/v5` | HTTP router |
| `github.com/openai/openai-go` | OpenAI Go SDK |
| `gopkg.in/yaml.v3` | YAML config parsing |
| `github.com/tidwall/gjson` | JSON path parsing (indirect) |

Minimal by design — no database, no heavy logging framework, no ORM.

---

## Adding a New Provider

1. Create `providers/<name>.go` implementing `providers.Provider` (and optionally `StreamProvider`, `ProxiableProvider`)
2. Register in the provider registry via `init()` using `providers.Register(...)`
3. Add the corresponding environment variable handling in `cmd/ferrogw/main.go`
4. Add tests in `providers/<name>_test.go`

## Adding a New Plugin

1. Create `internal/plugins/<name>/plugin.go` implementing `plugin.Plugin`
2. Register via `plugin.Register(...)` in `init()`
3. Add blank import in `cmd/ferrogw/main.go`: `_ "github.com/ferro-labs/ai-gateway/internal/plugins/<name>"`
4. Plugin config is passed as `map[string]interface{}` to `Init()`

## Adding a New Strategy

1. Create `internal/strategies/<name>.go` implementing `strategies.Strategy`
2. Handle the new `StrategyMode` constant in `gateway.go`'s strategy selection logic
3. Add tests in `internal/strategies/<name>_test.go`

---

## Testing Conventions

- Unit tests live alongside implementation as `*_test.go`
- Integration tests require real provider API keys; run with `make test-integration`
- Use `make precommit` (fmt + test) before committing
- Benchmarks with `make bench`
