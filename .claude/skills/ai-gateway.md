---
name: ai-gateway
description: Use this skill for ANY task inside the ai-gateway repo.
  Triggers when working on LLM providers, routing logic, model lists,
  gateway config, middleware, or any ai-gateway code. Always load before
  editing Go files in ai-gateway, adding providers, fixing gateway bugs,
  or writing gateway tests.
---

# AI Gateway — Codebase Skill

## 1. Folder Structure

```
ai-gateway/
├── gateway.go              # Core Gateway struct — routing, plugin lifecycle, strategy execution
├── gateway_test.go         # Gateway orchestration tests (36KB)
├── config.go               # Config schema: Config, StrategyConfig, Target, PluginConfig
├── config_load.go          # LoadConfig(), ValidateConfig() for YAML/JSON
├── config_load_test.go     # Config loading tests
├── config.example.yaml     # Example YAML config
├── config.example.json     # Example JSON config
├── Makefile                # Build/test/lint/release targets
├── go.mod / go.sum         # Module: github.com/ferro-labs/ai-gateway (Go 1.24+)
│
├── cmd/
│   ├── ferrogw/            # HTTP server entry point (21 files)
│   │   ├── main.go         # Server setup, provider auto-registration, router init
│   │   ├── router.go       # chi router initialization
│   │   ├── router_routes.go # All route definitions
│   │   ├── chat_request.go # POST /v1/chat/completions handler
│   │   ├── completions.go  # POST /v1/completions (legacy)
│   │   ├── models_handler.go # GET /v1/models
│   │   ├── embeddings.go   # Embedding endpoint
│   │   ├── images.go       # Image generation endpoint
│   │   ├── proxy.go        # Pass-through proxy for unhandled /v1/* routes
│   │   ├── sse.go          # Server-Sent Events streaming
│   │   ├── cors.go         # CORS middleware
│   │   ├── server.go       # HTTP server wrapper
│   │   ├── store_init.go   # Store initialization
│   │   ├── server_observability.go # Metrics/tracing setup
│   │   └── http_helpers.go # HTTP utility functions
│   └── ferrogw-cli/        # CLI management tool (cobra-based)
│       ├── main.go         # CLI entry + command definitions
│       ├── admin.go        # Admin commands (keys, config, logs, stats)
│       ├── client.go       # HTTP client for CLI
│       └── output.go       # Output formatting
│
├── providers/
│   ├── core/               # Shared interfaces and types
│   │   ├── contracts.go    # Provider, StreamProvider, EmbeddingProvider,
│   │   │                   #   ImageProvider, DiscoveryProvider, ProxiableProvider
│   │   ├── chat.go         # Request, Response, Message, Choice, Usage types
│   │   ├── stream.go       # StreamChunk type
│   │   ├── embedding.go    # EmbeddingRequest, EmbeddingResponse
│   │   ├── image.go        # ImageRequest, ImageResponse
│   │   ├── model.go        # ModelInfo type
│   │   ├── constants.go    # Core constants
│   │   ├── errors.go       # ParseStatusCode(), error regex
│   │   └── bufpool.go      # JSONBodyReader() — sync.Pool buffer reuse
│   ├── factory.go          # ProviderConfig, ProviderEntry, CfgKey*/Capability* consts
│   ├── providers_list.go   # allProviders slice — all 29 ProviderEntry registrations
│   ├── names.go            # NameXxx constants re-exported from subpackages
│   ├── registry.go         # Registry type for runtime lookup by name
│   ├── facade_aliases.go   # Type aliases re-exporting core.* for compat
│   ├── stability_test.go   # Auto-validates all providers (name, capabilities)
│   └── <id>/               # 29 provider subpackages:
│       ├── <id>.go         #   openai, anthropic, gemini, groq, bedrock, vertex_ai,
│       └── <id>_test.go    #   azure_openai, azure_foundry, mistral, cohere, deepseek,
│                           #   together, replicate, ollama, fireworks, hugging_face,
│                           #   cerebras, cloudflare, databricks, deepinfra, moonshot,
│                           #   novita, nvidia_nim, openrouter, perplexity, qwen,
│                           #   sambanova, xai, ai21
│
├── plugin/                 # Public plugin framework
│   ├── plugin.go           # Plugin interface, PluginType, Stage, Context
│   ├── manager.go          # RunBefore/RunAfter/RunOnError lifecycle
│   ├── registry.go         # RegisterFactory() for plugin init()
│   └── errors.go           # RejectionError type
│
├── internal/
│   ├── admin/              # API key CRUD, dashboard, config history/rollback (13 files)
│   │   ├── handlers.go     # Admin API endpoints (26KB)
│   │   ├── keys.go         # API key management
│   │   ├── middleware.go   # Bearer token auth
│   │   ├── config_store.go # Config versioning & rollback
│   │   ├── sql_store.go    # SQLite/PostgreSQL backend
│   │   └── store.go        # Store interface
│   ├── plugins/            # Built-in plugin implementations
│   │   ├── budget/         # API cost budget enforcement
│   │   ├── cache/          # Request/response caching
│   │   ├── logger/         # Request/response logging
│   │   ├── maxtoken/       # Token/message limit guardrail
│   │   ├── ratelimit/      # Rate limiting
│   │   └── wordfilter/     # Blocked word guardrail
│   ├── strategies/         # Routing strategy implementations
│   │   ├── strategy.go     # Strategy interface
│   │   ├── single.go       # Single provider
│   │   ├── fallback.go     # Fallback on error
│   │   ├── loadbalance.go  # Round-robin
│   │   ├── leastlatency.go # Route to lowest latency
│   │   ├── costoptimized.go # Cost-efficient routing
│   │   ├── conditional.go  # Conditional routing
│   │   ├── contentbased.go # Route by request content
│   │   └── abtest.go       # A/B testing
│   ├── circuitbreaker/     # Per-provider circuit breaker
│   ├── discovery/          # OpenAI-compatible model discovery (shared helper)
│   ├── events/             # Async event hook dispatch
│   ├── httpclient/         # HTTP client with connection pooling
│   ├── latency/            # Latency tracking for least-latency strategy
│   ├── logging/            # Centralized logger
│   ├── mcp/               # Model Context Protocol integration
│   ├── metrics/            # Prometheus metrics (/metrics endpoint)
│   ├── ratelimit/          # Token bucket algorithm
│   ├── requestlog/         # Request log persistence (SQLite/Postgres)
│   ├── streamwrap/         # Stream wrapping utilities
│   ├── transport/          # HTTP transport pooling, sync.Pool optimization
│   ├── cache/              # Cache interface + in-memory implementation
│   └── version/            # Version info (set via ldflags)
│
├── models/                 # Model catalog (catalog.go, calculator.go, catalog.json)
├── web/                    # Admin dashboard (dashboard.html, assets.go)
├── docs/                   # Architecture diagram, logo
├── docker-compose.yml      # Local dev environment
├── Dockerfile              # Release container
└── .env.example            # Template for all provider env vars
```

---

## 2. Code Patterns

### 2a. Provider Structure

Every provider follows this exact pattern. Example from `providers/openai/openai.go`:

```go
package openai

const Name = "openai"   // single source of truth, re-exported in providers/names.go

type Provider struct {
    name       string
    apiKey     string
    baseURL    string
    httpClient *http.Client
}

// Compile-time interface assertions (REQUIRED for every provider)
var (
    _ core.Provider          = (*Provider)(nil)
    _ core.StreamProvider    = (*Provider)(nil)
    _ core.ProxiableProvider = (*Provider)(nil)
)

// Constructor — baseURL can be empty for default
func New(apiKey, baseURL string) (*Provider, error) { ... }

// Required methods
func (p *Provider) Name() string                { return p.name }
func (p *Provider) SupportedModels() []string   { return []string{"gpt-4o", "gpt-4-turbo", ...} }
func (p *Provider) SupportsModel(model string) bool { return strings.HasPrefix(model, "gpt-") || ... }
func (p *Provider) Models() []core.ModelInfo    { return core.ModelsFromList(p.name, p.SupportedModels()) }
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) { ... }

// Optional interfaces
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) { ... }
func (p *Provider) BaseURL() string             { return p.baseURL }
func (p *Provider) AuthHeaders() map[string]string { return map[string]string{"Authorization": "Bearer " + p.apiKey} }
```

**Registration** in `providers/providers_list.go`:
```go
{
    ID:           NameOpenAI,
    Capabilities: []string{CapabilityChat, CapabilityStream, CapabilityProxy, CapabilityEmbedding, CapabilityImage},
    EnvMappings: []EnvMapping{
        {CfgKeyAPIKey, "OPENAI_API_KEY", true},
        {CfgKeyBaseURL, "OPENAI_BASE_URL", false},
    },
    Build: func(cfg ProviderConfig) (Provider, error) {
        return openai.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
    },
},
```

**Name constant** in `providers/names.go`:
```go
import openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
const NameOpenAI = openaipkg.Name
```

### 2b. Plugin Middleware Wiring

Plugins register via `init()` in their package, wired through `plugin.Manager`.
See `internal/plugins/wordfilter/wordfilter.go` for a complete example:

- `init()` calls `plugin.RegisterFactory("word-filter", func() plugin.Plugin { return &WordFilter{} })`
- Implement `Name()`, `Type()`, `Init(config map[string]interface{})`, `Execute(ctx, *plugin.Context)`
- Set `pctx.Reject = true` and `pctx.Reason` to block requests (return nil — manager wraps as RejectionError)
- **Lifecycle**: `RunBefore()` → strategy/provider → `RunAfter()` (errors → `RunOnError()`)

### 2c. Error Handling Style

The codebase uses **wrapped errors with status codes** — no custom error types for providers:

```go
// Provider errors include HTTP status in parens for ParseStatusCode()
return nil, fmt.Errorf("openai API error (429): rate limited")
return nil, fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, body)

// Stream errors sent via channel
ch <- core.StreamChunk{Error: fmt.Errorf("stream decode: %w", err)}

// Validation errors are plain
return nil, fmt.Errorf("api key is required")

// ParseStatusCode extracts status from "provider error (NNN): message"
// providers/core/errors.go — used by fallback strategy for retry decisions
code := core.ParseStatusCode(err)  // returns 0 if no match
```

**No external error libraries.** Always `fmt.Errorf` with `%w` for wrapping.

---

## 3. How to Add a New Provider

### Step 1: Create the provider package

Create `providers/<id>/<id>.go` (package name = provider id). Use `providers/deepseek/deepseek.go`
as a minimal template or `providers/openai/openai.go` for a full-featured example. Required:

```go
package myprovider

const Name = "myprovider"

type Provider struct {
    name, apiKey, baseURL string
    httpClient            *http.Client
}

// Compile-time assertions (add all interfaces you implement)
var _ core.Provider = (*Provider)(nil)

func New(apiKey, baseURL string) (*Provider, error) { /* validate, set defaults, return */ }
func (p *Provider) Name() string                    { return p.name }
func (p *Provider) SupportedModels() []string       { return []string{"model-a"} }
func (p *Provider) SupportsModel(model string) bool { /* prefix or exact match */ }
func (p *Provider) Models() []core.ModelInfo        { return core.ModelsFromList(p.name, p.SupportedModels()) }
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
    // Transform core.Request → provider JSON, POST, transform response back
    // Errors: fmt.Errorf("myprovider API error (%d): %s", code, body)
}
```

### Step 2: Add the name constant

In `providers/names.go`:
```go
import myprovider "github.com/ferro-labs/ai-gateway/providers/myprovider"
const NameMyProvider = myprovider.Name
```

### Step 3: Register in providers_list.go

In `providers/providers_list.go`, add to the `allProviders` slice:
```go
{
    ID:           NameMyProvider,
    Capabilities: []string{CapabilityChat, CapabilityStream},
    EnvMappings: []EnvMapping{
        {CfgKeyAPIKey, "MYPROVIDER_API_KEY", true},
        {CfgKeyBaseURL, "MYPROVIDER_BASE_URL", false},
    },
    Build: func(cfg ProviderConfig) (Provider, error) {
        return myprovider.New(cfg[CfgKeyAPIKey], cfg[CfgKeyBaseURL])
    },
},
```

### Step 4: Write tests

Create `providers/<id>/<id>_test.go`:
- Test `New()` with valid/invalid args
- Test `Name()`, `SupportedModels()`, `SupportsModel()`
- Test `Complete()` with `httptest.NewServer` mock
- Test streaming with SSE mock if applicable
- Integration test gated by `os.Getenv("MYPROVIDER_API_KEY")`

### Step 5: Verify

```bash
make build && make lint && make test
```

The `stability_test.go` will automatically verify your provider's name constant
matches `Name()` and capabilities are valid. **No changes to `cmd/ferrogw/main.go` needed.**

---

## 4. Testing Patterns

### Style: Table-Driven Tests (used exclusively)

```go
func TestSupportsModel(t *testing.T) {
    p, _ := New("test-key", "")
    tests := []struct {
        name  string
        model string
        want  bool
    }{
        {"known model", "gpt-4o", true},
        {"unknown model", "llama-3", false},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if got := p.SupportsModel(tt.model); got != tt.want {
                t.Errorf("SupportsModel(%q) = %v, want %v", tt.model, got, tt.want)
            }
        })
    }
}
```

### Mocks: Hand-Written, Composition-Based

No external mock libraries. Mocks are minimal structs in test files:

```go
type mockProvider struct {
    name   string
    models []string
    resp   *providers.Response
    err    error
    calls  int
}
func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
    m.calls++
    return m.resp, m.err
}

// Streaming layered via embedding
type mockStreamProvider struct {
    mockProvider
    streamErr error
}
```

### HTTP Mocks: stdlib httptest

```go
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(responsePayload)
}))
defer srv.Close()
provider, _ := New("test-key", srv.URL)
```

### Integration Tests: Environment-Gated

```go
func TestProvider_Integration(t *testing.T) {
    key := os.Getenv("MYPROVIDER_API_KEY")
    if key == "" {
        t.Skip("Skipping: MYPROVIDER_API_KEY not set")
    }
    // ...live API calls...
}
```

### Stability Tests (automatic)

`providers/stability_test.go` auto-validates ALL providers:
- `Name()` returns the canonical `NameXxx` constant
- Every provider has at least `CapabilityChat`
- No duplicate names in registry
- All entries in `allProviders` are buildable

### Running Tests

```bash
# Unit tests (short mode, race detection)
make test
# → go test -v -short -race -timeout 30s ./...

# Single package
go test -v -short -race ./providers/openai/...

# Single test
go test -v -short -race -run TestSupportsModel ./providers/openai/...

# With coverage
make test-coverage
# → outputs coverage.html

# Integration tests (requires API keys)
make test-integration
# → go test -v -race -timeout 60s ./... -run Integration

# Benchmarks
make bench
# → go test -v -bench=. -benchmem ./...

# Full quality check
make all
# → deps + fmt + lint + test + test-coverage
```

---

## 5. Common Debugging

### Running Locally

```bash
# Minimum viable run (one provider key required)
OPENAI_API_KEY=sk-... make run
# → builds ./bin/ferrogw then starts server on :8080

# With config file
GATEWAY_CONFIG=config.example.yaml OPENAI_API_KEY=sk-... ./bin/ferrogw

# Docker
docker-compose up

# Health check
curl http://localhost:8080/health

# List models
curl http://localhost:8080/v1/models

# Test a completion
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'
```

### Log Inspection

The gateway uses Go's `log` package (no heavy framework). Key log patterns:

- **Provider init**: `"registered provider: openai"` — logged at startup
- **Request routing**: logged by the logger plugin if enabled
- **Plugin rejections**: `"plugin word-filter rejected request: blocked word detected"`
- **Circuit breaker**: `"circuit breaker open for provider: openai"`
- **Errors**: `"provider API error (429): rate limited"` — status code in parens

### Common Build/Test Failures

| Symptom | Fix |
|---------|-----|
| `golangci-lint not installed` | Install: `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` |
| `stability_test.go` fails | Your provider's `Name()` doesn't match the `NameXxx` constant in `names.go` |
| `compile-time assertion fails` | Missing interface method — check `providers/core/contracts.go` for required methods |
| `import cycle` | Don't import from `providers/` in `providers/core/` — core has zero internal deps |
| `OPENAI_API_KEY required` | Set at least one provider key for `make run` |
| Integration test skipped | Set the provider's env var (e.g., `ANTHROPIC_API_KEY`) |
| `go mod tidy` needed | Run `go mod tidy` after adding new dependencies |

### Makefile Quick Reference

```bash
make build          # Build ./bin/ferrogw
make build-cli      # Build ./bin/ferrogw-cli
make test           # Unit tests (short, race)
make lint           # golangci-lint
make fmt            # gofmt
make precommit      # fmt + test (run before committing)
make clean          # Remove bin/, coverage, caches
make bench          # Benchmarks with memory stats
make snapshot       # GoReleaser local snapshot build
```

### Request Flow (debugging order)

```
Client → cors.go → router_routes.go → chat_request.go
  → plugin/manager.go RunBefore() → gateway.go strategy selection
  → providers/<id>/<id>.go Complete() → plugin/manager.go RunAfter()
  → sse.go (if streaming) → response to client
```

When debugging a request failure, check in this order:
1. `cmd/ferrogw/router_routes.go` — is the route registered?
2. `gateway.go` — is the strategy/provider selected correctly?
3. `providers/<id>/<id>.go` — is the request transformed correctly?
4. `plugin/manager.go` — is a plugin rejecting the request?
5. `internal/circuitbreaker/` — is the circuit breaker open?

Key env vars: `GATEWAY_CONFIG`, `PORT` (default 8080), `CORS_ORIGINS`, provider keys (see `.env.example`).
