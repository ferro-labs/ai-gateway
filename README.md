<div align="center">

<h1 align="center">
  <img src="docs/logo.png" alt="Ferro Logo" height="60" align="absmiddle" /> Ferro AI Gateway
</h1>

**The high-performance, open-source control plane for your AI applications.**  
Route, observe, and secure requests across 100+ LLM providers via a single OpenAI-compatible API.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/ferro-labs/ai-gateway.svg)](https://pkg.go.dev/github.com/ferro-labs/ai-gateway)
[![Code Scanning](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml)
[![Discord](https://img.shields.io/badge/Discord-Join%20Us-5865F2?logo=discord&logoColor=white)](https://discord.gg/yCAeYvJeDV)

<br/>

<img src="docs/architecture.svg" alt="AI Gateway Architecture" width="100%" />

</div>

---

Ferro Gateway is a remarkably fast, lightweight routing tier built in Go. It acts as an intelligent intermediary between your applications and upstream foundation models, effectively transforming fragmented API integration into a unified, secure, and observable infrastructure layer.

Zero SDK changes required. Drop it into your existing OpenAI-reliant code in one line.

## ✨ Core Capabilities

* **Unified API:** Connect to 100+ top-tier models (OpenAI, Anthropic, Gemini, Mistral, Ollama, DeepSeek, and more) using the exact same standard OpenAI request/response format.
* **Smart Routing Engine:** Mitigate downtime and optimize costs using 4 robust routing strategies: Single, Fallback (w/ exponential backoff), Weighted Load Balancing, and Conditional (model-based).
* **Transparent Pass-Through Proxy:** Automatically forwards requests for non-chat endpoints (like `/v1/audio`, `/v1/images`, `/v1/files`, etc.) directly to the provider. The gateway securely injects your auth credentials while proxying raw bytes!
* **Observability Built-In:** Structured JSON logs with per-request trace IDs, Prometheus `/metrics` endpoint (request count, latency histograms, token usage), and a deep `/health` endpoint with per-provider status.
* **Resilience by Default:** Per-provider circuit breakers (Closed/Open/HalfOpen) auto-disable failing backends. Token-bucket rate limiting is available as both an HTTP middleware (per-IP) and a plugin (per-provider).
* **Extensible Middleware:** Intercept requests via pluggable plugins for Guardrails (PII/word filtering), Token Limiting, exact-match Caching, Rate Limiting, and Request Logging.
* **Secure Access Manager:** Centrally issue scoped, auto-expiring API keys with native RBAC. Zero external database required for stand-up.

---

## ✅ Integrated Model Providers

The following providers are integrated in the gateway codebase:

| Provider | Integrated |
| --- | --- |
| OpenAI | ✅ |
| Anthropic | ✅ |
| Gemini (Google) | ✅ |
| Mistral | ✅ |
| Groq | ✅ |
| Cohere | ✅ |
| DeepSeek | ✅ |
| Together AI | ✅ |
| Perplexity | ✅ |
| Fireworks | ✅ |
| AI21 | ✅ |
| Azure OpenAI | ✅ |
| Ollama (local/self-hosted) | ✅ |
| Replicate | ✅ |
| AWS Bedrock | ✅ |

> Providers are enabled when their corresponding environment variables/credentials are configured.

---

## ⚡ Quick Start

### Run via Docker

The fastest way to get started is pulling the official image from GitHub Container Registry.

```bash
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY=sk-your-key \
  ghcr.io/ferro-labs/ai-gateway:latest
```

### Build from Source

Ensure you have Go 1.24+ installed.

```bash
git clone https://github.com/ferro-labs/ai-gateway.git
cd ai-gateway

export OPENAI_API_KEY=sk-your-key
make run
# Server listens locally on :8080
```

---

## 🧾 Persistent Request Logging

The built-in `request-logger` plugin can persist request lifecycle events (`before_request`, `after_request`, `on_error`) into SQLite or PostgreSQL.

### Plugin config (YAML)

```yaml
plugins:
  - name: request-logger
    type: logging
    stage: before_request
    enabled: true
    config:
      level: info
      persist: true
      backend: sqlite   # sqlite | postgres
      dsn: ferrogw-requests.db
```

For PostgreSQL:

```yaml
plugins:
  - name: request-logger
    type: logging
    stage: before_request
    enabled: true
    config:
      level: info
      persist: true
      backend: postgres
      dsn: postgresql://user:pass@localhost:5432/ferrogw?sslmode=disable
```

---

## 🧪 Postgres Integration Tests

PostgreSQL store integration tests are opt-in. Set `FERROGW_TEST_POSTGRES_DSN` and run the admin package tests.

```bash
export FERROGW_TEST_POSTGRES_DSN='postgresql://user:pass@localhost:5432/ferrogw_test?sslmode=disable'
go test ./internal/admin
```

Without that environment variable, Postgres integration tests are skipped automatically.

---

## ⚙️ Storage Backend Env Quick Reference

Use these environment variables to enable persistent backends:

| Area | Backend Env | DSN Env | Supported Values |
| --- | --- | --- | --- |
| Runtime config store | `CONFIG_STORE_BACKEND` | `CONFIG_STORE_DSN` | `memory` (default), `sqlite`, `postgres` |
| API key store | `API_KEY_STORE_BACKEND` | `API_KEY_STORE_DSN` | `memory` (default), `sqlite`, `postgres` |
| Request log store | `REQUEST_LOG_STORE_BACKEND` | `REQUEST_LOG_STORE_DSN` | `sqlite`, `postgres` (unset = disabled) |

Example (fully persistent local setup with SQLite):

```bash
export CONFIG_STORE_BACKEND=sqlite
export CONFIG_STORE_DSN=./ferrogw-config.db

export API_KEY_STORE_BACKEND=sqlite
export API_KEY_STORE_DSN=./ferrogw-keys.db

export REQUEST_LOG_STORE_BACKEND=sqlite
export REQUEST_LOG_STORE_DSN=./ferrogw-requests.db
```

Example (production-style PostgreSQL setup):

```bash
export CONFIG_STORE_BACKEND=postgres
export CONFIG_STORE_DSN='postgresql://user:pass@db:5432/ferrogw?sslmode=disable'

export API_KEY_STORE_BACKEND=postgres
export API_KEY_STORE_DSN='postgresql://user:pass@db:5432/ferrogw?sslmode=disable'

export REQUEST_LOG_STORE_BACKEND=postgres
export REQUEST_LOG_STORE_DSN='postgresql://user:pass@db:5432/ferrogw?sslmode=disable'
```

Production note:

* You can use a single shared DSN for all three stores (simpler operations).
* For stronger isolation, use separate databases or schemas per area (config, API keys, request logs) with least-privilege credentials.

---

## 🔎 API Key Usage Analytics

Admin API provides a usage analytics endpoint:

```http
GET /admin/keys/usage
Authorization: Bearer <admin-or-readonly-key>
```

Supported query params:

* `limit` (default `20`, max `100`)
* `offset` (default `0`)
* `sort` (`usage` or `last_used`; default `usage`)
* `active` (`true` or `false`)
* `since` (RFC3339 timestamp; filters by `last_used_at >= since`)

Example:

```bash
curl "http://localhost:8080/admin/keys/usage?limit=10&offset=0&sort=usage&active=true&since=2026-02-01T00:00:00Z" \
  -H "Authorization: Bearer gw-..."
```

Response contains `data` (sorted by `usage_count` desc) and `summary` totals.

---

## ⏱️ API Key Expiration Management

Update key expiration without rotating or recreating the key:

```http
PUT /admin/keys/{id}
Authorization: Bearer <admin-key>
Content-Type: application/json
```

Supported expiration fields in request body:

* `expires_at` (RFC3339 timestamp) to set/update expiration
* `clear_expiration` (`true`) to remove expiration

Examples:

```bash
curl -X PUT "http://localhost:8080/admin/keys/<id>" \
  -H "Authorization: Bearer gw-..." \
  -H "Content-Type: application/json" \
  -d '{"expires_at":"2026-03-15T00:00:00Z"}'
```

```bash
curl -X PUT "http://localhost:8080/admin/keys/<id>" \
  -H "Authorization: Bearer gw-..." \
  -H "Content-Type: application/json" \
  -d '{"clear_expiration":true}'
```

---

## 🔑 API Key Detail API

Fetch a single API key by ID (masked key value):

```http
GET /admin/keys/{id}
Authorization: Bearer <admin-or-readonly-key>
```

Returns `404` if the key does not exist.

---

## 📜 Admin Request Logs API

When request log storage is enabled, admin API exposes persisted request logs:

```http
GET /admin/logs
Authorization: Bearer <admin-or-readonly-key>
```

Supported query params:

* `limit` (default `50`, max `200`)
* `offset` (default `0`)
* `stage` (e.g. `before_request`, `after_request`, `on_error`)
* `model`
* `provider`
* `since` (RFC3339 timestamp)

Example:

```bash
curl "http://localhost:8080/admin/logs?limit=20&offset=0&stage=on_error&since=2026-02-01T00:00:00Z" \
  -H "Authorization: Bearer gw-..."
```

If request log storage is disabled, endpoint returns `501 Not Implemented`.

### Cleanup old request logs

```http
DELETE /admin/logs?before=<RFC3339>
Authorization: Bearer <admin-key>
```

Optional filters:

* `stage`
* `model`
* `provider`

Example:

```bash
curl -X DELETE "http://localhost:8080/admin/logs?before=2026-02-01T00:00:00Z&stage=on_error" \
  -H "Authorization: Bearer gw-..."
```

Response includes the number of deleted entries in `deleted`.

### Request log stats

```http
GET /admin/logs/stats
Authorization: Bearer <admin-or-readonly-key>
```

Optional filters:

* `limit` (positive integer; caps `by_provider` and `by_model` cardinality)
* `stage`
* `model`
* `provider`
* `since` (RFC3339 timestamp)

Response contains aggregated `summary`, `by_stage`, `by_provider`, and `by_model` counts.

---

## 📊 Admin Dashboard Summary API

For a minimal admin dashboard UI, fetch aggregate status from one endpoint:

```http
GET /admin/dashboard
Authorization: Bearer <admin-or-readonly-key>
```

Response sections:

* `providers` (`total`, `available`)
* `keys` (`total`, `active`, `expired`, `total_usage`)
* `request_logs` (`enabled`, `total`)

---

## 🕘 Config History API

Create/update/delete runtime config over admin APIs:

```http
POST /admin/config
PUT /admin/config
DELETE /admin/config
Authorization: Bearer <admin-key>
```

Notes:

* `POST /admin/config` creates a new runtime config version (same payload schema as `PUT`)
* `PUT /admin/config` updates the current runtime config
* `DELETE /admin/config` resets to startup config and clears persisted override

Persist config across restarts via env vars:

* `CONFIG_STORE_BACKEND`: `memory` (default), `sqlite`, `postgres`
* `CONFIG_STORE_DSN`: backend DSN or SQLite file path

Runtime config updates are tracked in-memory and exposed via:

```http
GET /admin/config/history
Authorization: Bearer <admin-or-readonly-key>
```

Response includes:

* `data[]` with `version`, `updated_at`, and `config`
* `summary.total_versions`

Rollback to a previous version:

```http
POST /admin/config/rollback/{version}
Authorization: Bearer <admin-key>
```

Example:

```bash
curl -X POST "http://localhost:8080/admin/config/rollback/2" \
  -H "Authorization: Bearer gw-..."
```

---

## 🖥️ Built-in Admin Dashboard UI

A minimal dashboard page is available at:

```http
GET /dashboard
```

It prompts for an admin/read-only key and then calls `GET /admin/dashboard` to render provider, key, and request-log summary cards.

The page also loads `GET /admin/config/history` and includes per-version rollback actions using `POST /admin/config/rollback/{version}`.

---

## 🔌 1-Line Migration

FerroGateway natively speaks the OpenAI spec. Point your existing client SDKs to the Gateway by changing simply the `baseURL`—**no SDK changes, no prompt edits, no refactoring.**

#### Python

```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-ferro-...", # Managed via ferro
    base_url="http://localhost:8080/v1",  # ← Only change this line
)
```

#### TypeScript / Node.js

```typescript
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-ferro-...",
  baseURL: "http://localhost:8080/v1",  // ← Only change this line 
});
```

#### cURL

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-ferro-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"Hello!"}]}'
  # The gateway automatically detects the model and routes to Anthropic!
```

---

## 🛣️ Project Roadmap

Ferro Gateway is actively developed to support an end-to-end AI operating environment. We are currently transitioning through major foundational and production-grade phases:

* [x] **v0.1.0** — Foundation Release: Core routing, multi-provider execution, basic guardrails, and streaming capabilities.
* [x] **v0.2.0** — Observability & Resilience: Structured JSON logging with trace IDs, Prometheus metrics, per-provider circuit breakers, token-bucket rate limiting, deep health checks, and consistent error schema.
* [x] **v0.3.0** — Modality Expansions: Embeddings, Image generation mapping, Cost tracking via pricing tables, and Model aliasing.
* [x] **v0.4.0** — Persistent State: Dedicated Admin API, SQLite/PostgreSQL persistence, persistent request logs, dashboard, and runtime config CRUD.
* [ ] **v0.5.0** — Advanced Intelligence: Least-latency and Cost-optimized algorithmic routing, A/B Testing modules, and Semantic Caching.
* [ ] **v1.0.0** — Production Ready: Helm charts, open-telemetry export, edge caching, and official SDK embeddings.

*Review our detailed [ROADMAP.md](ROADMAP.md) for deeper implementation plans.*

---

## 🤝 Contributing

We welcome community contributions! The priority areas for ecosystem growth are:

1. Adding support for new niche LLM providers.
2. Building new middleware plugins (Guardrails, Modifiers, Analyzers).
3. Enhancing test coverage and documentation.

Please see our [CONTRIBUTING.md](CONTRIBUTING.md) for style guidelines and PR processes.
By participating, you agree to follow our [Code of Conduct](CODE_OF_CONDUCT.md).

## 📄 License

FerroGateway is proudly open-source and released under the [Apache 2.0 License](LICENSE).
