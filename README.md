<div align="center">

<h1 align="center">
  <img src="docs/logo.png" alt="Ferro Logo" height="60" align="absmiddle" /> Ferro Labs AI Gateway
</h1>

**Open-source, OpenAI-compatible AI gateway built in Go.**  
Route, govern, and observe LLM traffic across multiple providers through one API.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/ferro-labs/ai-gateway.svg)](https://pkg.go.dev/github.com/ferro-labs/ai-gateway)
[![Code Scanning](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml)
[![Discord](https://img.shields.io/badge/Discord-Join%20Us-5865F2?logo=discord&logoColor=white)](https://discord.gg/yCAeYvJeDV)

<br/>

<img src="docs/architecture.svg" alt="AI Gateway Architecture" width="100%" />

</div>

---

Ferro Labs AI Gateway is a lightweight control plane that sits between your app and model providers.
It exposes OpenAI-style endpoints (`/v1/chat/completions`, `/v1/models`, `/v1/embeddings`, `/v1/images/generations`) while handling routing, retries, guardrails, logging, admin controls, and provider auth centrally.

## Why use it

- **OpenAI-compatible API surface** for easier migration and standard client support.
- **Multi-provider routing** with 8 strategy modes: `single`, `fallback`, `loadbalance`, `conditional`, `least-latency`, `cost-optimized`, `content-based`, `ab-test`.
- **Built-in resilience** via per-target retry controls and circuit breakers.
- **Built-in governance hooks** via plugin lifecycle stages (`before_request`, `after_request`, `on_error`).
- **Operational visibility** through structured logs, `/metrics`, deep `/health`, admin APIs, and dashboard UI.
- **Production-friendly storage options** for runtime config, API keys, and request logs (`memory`, `sqlite`, `postgres`).
- **MCP tool server integration** — attach any MCP 2025-11-25 Streamable HTTP server and let the LLM drive an agentic tool-call loop without changing client code.

## Current highlights

Recent releases include:

- **Content-based routing, A/B testing, and budget controls (v0.8.5)** — route by prompt content with `contains`/`not-contains`/`regex` rules, split traffic with weighted A/B testing, enforce per-key USD spend limits with the `budget` plugin, and rate-limit by API key or user (`key_rpm`, `user_rpm`).
- **MCP integration (v0.8.0)** — attach external MCP 2025-11-25 Streamable HTTP tool servers; the gateway drives an agentic loop over `tool_calls` transparently before returning the final LLM response.
- Reliability hardening: fallback-strategy error propagation, event hook panic recovery, config persistence rollback safety (v0.7.0).
- Provider layer refactor into per-provider subpackages and canonical `Name*` constants.
- Unified provider factory with `ProviderConfig` and `AllProviders()` registry entries.

See [CHANGELOG.md](CHANGELOG.md) for full release notes.

## Quick start

### Run with Docker

```bash
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY=sk-your-key \
  ghcr.io/ferro-labs/ai-gateway:latest
```

### Build from source

```bash
git clone https://github.com/ferro-labs/ai-gateway.git
cd ai-gateway

export OPENAI_API_KEY=sk-your-key
make run
```

### Verify gateway is running

```bash
curl -s http://localhost:8080/health | jq
curl -s http://localhost:8080/v1/models | jq '.data | length'
```

### Send your first request

```bash
curl -s http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-ferro-or-upstream-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role":"user","content":"Say hello from Ferro Gateway"}]
  }' | jq
```

## Core endpoints

### Public/API routes

- `GET /health` — deep provider health summary.
- `GET /metrics` — Prometheus metrics.
- `GET /v1/models` — OpenAI-style model list, enriched from catalog when available.
- `POST /v1/chat/completions` — chat (streaming + non-streaming).
- `POST /v1/completions` — legacy text completions.
- `POST /v1/embeddings` — embeddings.
- `POST /v1/images/generations` — image generation.
- `GET /dashboard` — built-in admin dashboard page.
- `GET /v1/*` (other) — transparent proxy pass-through to provider.

### Admin routes (Bearer auth required)

Read scope (`read_only` or `admin`):

- `GET /admin/dashboard`
- `GET /admin/keys`, `GET /admin/keys/{id}`, `GET /admin/keys/usage`
- `GET /admin/providers`, `GET /admin/health`
- `GET /admin/config`, `GET /admin/config/history`
- `GET /admin/logs`, `GET /admin/logs/stats`

Admin scope (`admin` only):

- `POST /admin/keys`, `PUT /admin/keys/{id}`, `DELETE /admin/keys/{id}`
- `POST /admin/keys/{id}/revoke`, `POST /admin/keys/{id}/rotate`
- `POST /admin/config`, `PUT /admin/config`, `DELETE /admin/config`
- `POST /admin/config/rollback/{version}`
- `DELETE /admin/logs`

## MCP tool server integration

Attach any [Model Context Protocol](https://modelcontextprotocol.io) 2025-11-25 Streamable HTTP server. The gateway initialises all configured servers at startup, injects the discovered tools into every LLM request, and drives the agentic `tool_calls` loop automatically up to `max_call_depth` iterations.

```yaml
mcp_servers:
  - name: my-tools
    url: https://mcp.example.com/mcp
    headers:
      Authorization: Bearer ${MY_TOOLS_TOKEN}
    allowed_tools: [search, get_weather]   # omit to allow all
    max_call_depth: 5
    timeout_seconds: 30
```

Multiple servers can be registered. Tools are deduplicated by name across servers.

## Routing strategy modes

Set `strategy.mode` in your config:

- `single` — fixed provider target.
- `fallback` — try targets in order, with per-target retry.
- `loadbalance` — weighted selection across targets.
- `conditional` — route by request metadata conditions.
- `least-latency` — pick lowest observed latency provider.
- `cost-optimized` — pick cheapest compatible provider from model catalog pricing.
- `content-based` — route by prompt content using `contains`, `not-contains`, or `regex` rules; first matching rule wins, falls back to the first target.
- `ab-test` — weighted random traffic split across named variants; zero-weight variants are treated as equal weight.

## Configuration

Use `GATEWAY_CONFIG` to load YAML/JSON config at startup.

```bash
export GATEWAY_CONFIG=./config.yaml
```

Minimal production-style example:

```yaml
strategy:
  mode: fallback

targets:
  - virtual_key: openai
    retry:
      attempts: 3
      on_status_codes: [429, 502, 503]
      initial_backoff_ms: 100

  - virtual_key: anthropic
    retry:
      attempts: 2

aliases:
  fast: gpt-4o-mini
  smart: claude-3-5-sonnet-20241022

plugins:
  - name: request-logger
    type: logging
    stage: before_request
    enabled: true
    config:
      level: info
      persist: true
      backend: sqlite
      dsn: ferrogw-requests.db
```

See [config.example.yaml](config.example.yaml) and [config.example.json](config.example.json) for a full template.

## Built-in providers

Canonical provider keys used in config (`targets[].virtual_key`):

| Virtual Key | Provider | Enablement Env |
| --- | --- | --- |
| `ai21` | AI21 | `AI21_API_KEY` |
| `anthropic` | Anthropic | `ANTHROPIC_API_KEY` |
| `azure-foundry` | Azure Foundry | `AZURE_FOUNDRY_API_KEY` + `AZURE_FOUNDRY_ENDPOINT` |
| `azure-openai` | Azure OpenAI | `AZURE_OPENAI_API_KEY` + `AZURE_OPENAI_ENDPOINT` + `AZURE_OPENAI_DEPLOYMENT` |
| `bedrock` | AWS Bedrock | `AWS_REGION` **or** `AWS_ACCESS_KEY_ID` |
| `cohere` | Cohere | `COHERE_API_KEY` |
| `deepseek` | DeepSeek | `DEEPSEEK_API_KEY` |
| `fireworks` | Fireworks | `FIREWORKS_API_KEY` |
| `gemini` | Google Gemini | `GEMINI_API_KEY` |
| `groq` | Groq | `GROQ_API_KEY` |
| `hugging-face` | Hugging Face | `HUGGING_FACE_API_KEY` |
| `mistral` | Mistral | `MISTRAL_API_KEY` |
| `ollama` | Ollama | `OLLAMA_HOST` |
| `openai` | OpenAI | `OPENAI_API_KEY` |
| `perplexity` | Perplexity | `PERPLEXITY_API_KEY` |
| `replicate` | Replicate | `REPLICATE_API_TOKEN` |
| `together` | Together AI | `TOGETHER_API_KEY` |
| `vertex-ai` | Vertex AI | `VERTEX_AI_PROJECT_ID` + `VERTEX_AI_REGION` + (`VERTEX_AI_API_KEY` or `VERTEX_AI_SERVICE_ACCOUNT_JSON`) |
| `xai` | xAI | `XAI_API_KEY` |

## Built-in plugins

Registered plugin set:

- Guardrails: `word-filter`, `max-token`, `pii-redact`, `secret-scan`, `prompt-shield`, `schema-guard`, `regex-guard`
- Transform: `response-cache`
- Logging: `request-logger`
- Rate limiting: `rate-limit` — global RPM/RPS plus optional `key_rpm` (per-API-key) and `user_rpm` (per-user) limits.
- Budget control: `budget` — per-API-key USD spend tracking and enforcement with configurable input/output token pricing.

Inspect available plugins with:

```bash
make build-cli
./bin/ferrogw-cli plugins
```

## Persistence backends

Configure state backends with environment variables:

| Area | Backend Env | DSN Env | Values |
| --- | --- | --- | --- |
| Runtime config | `CONFIG_STORE_BACKEND` | `CONFIG_STORE_DSN` | `memory` (default), `sqlite`, `postgres` |
| API keys | `API_KEY_STORE_BACKEND` | `API_KEY_STORE_DSN` | `memory` (default), `sqlite`, `postgres` |
| Request logs | `REQUEST_LOG_STORE_BACKEND` | `REQUEST_LOG_STORE_DSN` | `sqlite`, `postgres` (`unset` = disabled) |

SQLite local example:

```bash
export CONFIG_STORE_BACKEND=sqlite
export CONFIG_STORE_DSN=./ferrogw-config.db

export API_KEY_STORE_BACKEND=sqlite
export API_KEY_STORE_DSN=./ferrogw-keys.db

export REQUEST_LOG_STORE_BACKEND=sqlite
export REQUEST_LOG_STORE_DSN=./ferrogw-requests.db
```

PostgreSQL example:

```bash
export CONFIG_STORE_BACKEND=postgres
export CONFIG_STORE_DSN='postgresql://user:pass@db:5432/ferrogw?sslmode=require'

export API_KEY_STORE_BACKEND=postgres
export API_KEY_STORE_DSN='postgresql://user:pass@db:5432/ferrogw?sslmode=require'

export REQUEST_LOG_STORE_BACKEND=postgres
export REQUEST_LOG_STORE_DSN='postgresql://user:pass@db:5432/ferrogw?sslmode=require'
```

## Security and production notes

- **CORS defaults to wildcard** when `CORS_ORIGINS` is unset/empty. Set explicit origins in production.
- **Bootstrap keys** (`ADMIN_BOOTSTRAP_KEY`, `ADMIN_BOOTSTRAP_READ_ONLY_KEY`) are for first-run setup only.
- Prefer TLS-backed Postgres DSNs (`sslmode=require` or stronger).
- Use scoped admin keys and rotate periodically.

Bootstrap key quick setup:

```bash
export ADMIN_BOOTSTRAP_ENABLED=true
export ADMIN_BOOTSTRAP_KEY='change-me-admin'
export ADMIN_BOOTSTRAP_READ_ONLY_KEY='change-me-readonly'
```

## CLI

`ferrogw-cli` manages and inspects a running gateway:

```bash
make build-cli

./bin/ferrogw-cli validate config.example.yaml
./bin/ferrogw-cli plugins
./bin/ferrogw-cli admin providers list --api-key "$FERROGW_API_KEY"
./bin/ferrogw-cli admin config history --api-key "$FERROGW_API_KEY"
```

Persistent CLI flags:

- `--gateway-url` (env: `FERROGW_URL`, default: `http://localhost:8080`)
- `--api-key` (env: `FERROGW_API_KEY`)
- `--format` (`table`, `json`, `yaml`)

## Development

Common Make targets:

```bash
make deps
make fmt
make lint
make test
make test-coverage
make test-integration
make bench
```

Run release validation locally:

```bash
make release-check
make release-dry-run
```

## 1-line SDK migration

Point existing OpenAI SDK clients to Ferro Gateway by changing only the base URL.

### Python

```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-ferro-...",
    base_url="http://localhost:8080/v1",
)
```

### TypeScript

```typescript
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-ferro-...",
  baseURL: "http://localhost:8080/v1",
});
```

## Roadmap

The project roadmap is maintained in [ROADMAP.md](ROADMAP.md).

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) and follow [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## License

Licensed under [Apache 2.0](LICENSE).
