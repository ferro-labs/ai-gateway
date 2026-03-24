<div align="center">

<h1 align="center">
  <img src="docs/logo.png" alt="Ferro Labs AI Gateway" height="60" align="absmiddle" /> Ferro Labs AI Gateway
</h1>

**Production-grade AI gateway in Go. Route LLM requests across 29 providers via a single OpenAI-compatible API.**

[![v1.0.0](https://img.shields.io/badge/release-v1.0.0-00ADD8)](https://github.com/ferro-labs/ai-gateway/releases/tag/v1.0.0)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/ferro-labs/ai-gateway.svg)](https://pkg.go.dev/github.com/ferro-labs/ai-gateway)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/ferro-labs/ai-gateway?style=flat&color=yellow)](https://github.com/ferro-labs/ai-gateway/stargazers)
[![CI](https://github.com/ferro-labs/ai-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/ci.yml)
[![Code Scanning](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml)
[![Discord](https://img.shields.io/badge/Discord-Join%20Us-5865F2?logo=discord&logoColor=white)](https://discord.gg/yCAeYvJeDV)

🔀 **29 providers, 2,500+ models — one API**<br/>
⚡ **13,925 RPS at 1,000 concurrent users**<br/>
📦 **Single binary, zero dependencies, 32 MB base memory**

<img src="docs/architecture.svg" alt="Ferro Labs AI Gateway Architecture" width="100%" />

</div>

---

## Quick Start

Get from zero to first request in under 2 minutes.

### Option A — Binary (fastest)

```bash
curl -fsSL https://github.com/ferro-labs/ai-gateway/releases/download/v1.0.0/ferro-gw_linux_amd64 -o ferro-gw
chmod +x ferro-gw
./ferro-gw --config config.yaml
```

### Option B — Docker

```bash
docker pull ghcr.io/ferro-labs/ai-gateway:v1.0.0
docker run -p 8080:8080 \
  -e OPENAI_API_KEY=sk-your-key \
  ghcr.io/ferro-labs/ai-gateway:v1.0.0
```

### Option C — Go

```bash
go install github.com/ferro-labs/ai-gateway/cmd/ferrogw@v1.0.0
```

### Minimal config

Create `config.yaml`:

```yaml
strategy:
  mode: fallback

targets:
  - virtual_key: openai
    retry:
      attempts: 3
      on_status_codes: [429, 502, 503]
  - virtual_key: anthropic

aliases:
  fast: gpt-4o-mini
  smart: claude-3-5-sonnet-20241022
```

### First request

```bash
export OPENAI_API_KEY=sk-your-key

curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello from Ferro Labs AI Gateway"}]
  }' | jq
```

---

## Why Ferro Labs

Most AI gateways are Python proxies that crack under load or JavaScript services that eat memory. Ferro Labs AI Gateway is written in Go from the ground up for production throughput — a single binary that routes LLM requests with predictable latency and minimal resource usage.

| Feature          | Ferro Labs  | LiteLLM | Bifrost    | Kong AI     |
|:-----------------|:------------|:--------|:-----------|:------------|
| Language         | Go          | Python  | Go         | Go/Lua      |
| Single binary    | ✅          | ❌      | ✅         | ❌          |
| Providers        | 29          | 100+    | 20+        | 10+         |
| MCP support      | ✅          | ❌      | ✅         | ❌          |
| Response cache   | ✅          | ✅      | ✅         | ❌ (paid)   |
| Guardrails       | ✅          | ✅      | ❌         | ❌ (paid)   |
| OSS license      | Apache 2.0  | MIT     | Apache 2.0 | Apache 2.0  |
| Managed cloud    | Coming Soon | ✅      | ✅         | ✅          |

---

## Benchmarks

All numbers from a controlled GCP run. Hardware: **GCP n2-standard-8** (8 vCPU, 32 GB RAM), Debian 12. Mock backend with 60 ms fixed latency, native processes, 60 s warmup per step.

### Cross-gateway throughput (RPS)

| Gateway             | Language | 150 VU | 300 VU | 500 VU | 1,000 VU | Memory       |
|:--------------------|:---------|-------:|-------:|-------:|---------:|:-------------|
| **Ferro Labs**      | Go       | 2,447  | 4,890  | 8,014  | 13,925   | 32–135 MB    |
| Kong AI Gateway     | Go       | 2,443  | 4,885  | 8,133  | 15,891   | 43 MB flat   |
| Bifrost             | Go       | 2,441  | 0 †    | 0 †    | 0 †      | 107–333 MB   |
| LiteLLM             | Python   | 175 ‡  | —      | —      | —        | 335–1,124 MB |

> † Bifrost: 10M+ failures at ≥300 VU. Works well up to ~150 VU.
> ‡ LiteLLM: peaked at 175 RPS, timeouts at higher concurrency.
> Portkey excluded: config limitation in test harness, not a performance issue.

### Ferro Labs AI Gateway latency profile

| VU    | RPS    | p50     | p99      | Memory |
|------:|-------:|--------:|---------:|-------:|
| 50    | 813    | 61.3 ms | 64.1 ms  | 36 MB  |
| 150   | 2,447  | 61.2 ms | 63.4 ms  | 47 MB  |
| 300   | 4,890  | 61.2 ms | 64.4 ms  | 72 MB  |
| 500   | 8,014  | 61.5 ms | 72.9 ms  | 89 MB  |
| 1,000 | 13,925 | 68.1 ms | 111.9 ms | 135 MB |

### Reproduce

```bash
git clone https://github.com/ferro-labs/ai-gateway-performance-benchmarks
cd ai-gateway-performance-benchmarks
cp .env.example .env
make setup && make bench
```

---

## Features

### 🔀 Routing

- **8 routing strategies:** single, fallback, load balance, least latency, cost-optimized, content-based, A/B test, conditional
- Provider failover with configurable retry policies and status code filters
- Per-request model aliases (`fast → gpt-4o-mini`, `smart → claude-3-5-sonnet`)

### 🔌 Providers (29)

| OpenAI & Compatible | Anthropic & Google | Cloud & Enterprise | Open Source & Inference |
|:---|:---|:---|:---|
| OpenAI | Anthropic | AWS Bedrock | Ollama |
| Azure OpenAI | Google Gemini | Azure Foundry | Hugging Face |
| OpenRouter | Vertex AI | Databricks | Replicate |
| DeepSeek | | Cloudflare Workers AI | Together AI |
| Perplexity | | | Fireworks |
| xAI (Grok) | | | DeepInfra |
| Mistral | | | NVIDIA NIM |
| Groq | | | SambaNova |
| Cohere | | | Novita AI |
| AI21 | | | Cerebras |
| Moonshot / Kimi | | | Qwen / DashScope |

### 🛡️ Guardrails & Plugins

- **Word/phrase filtering** — block sensitive terms before they reach providers
- **Token and message limits** — enforce max_tokens and max_messages per request
- **Response caching** — in-memory cache with configurable TTL and entry limits
- **Rate limiting** — global RPS plus per-API-key and per-user RPM limits
- **Budget controls** — per-API-key USD spend tracking with configurable token pricing
- **Request logging** — structured logs with optional SQLite/PostgreSQL persistence

### ⚡ Performance

- Per-provider HTTP connection pools with production-tuned settings
- `sync.Pool` for JSON marshaling buffers and streaming I/O
- Zero-allocation stream detection, async hook dispatch batching
- Single binary, ~32 MB base memory, linear scaling to 1,000+ VUs

### 🤖 MCP (Model Context Protocol)

- Agentic tool-call loop — the gateway drives `tool_calls` automatically
- Streamable HTTP transport (MCP 2025-11-25 spec)
- Tool filtering with `allowed_tools` and bounded `max_call_depth`
- Multiple MCP servers with cross-server tool deduplication

### 📊 Observability

- Prometheus metrics at `/metrics`
- Deep health checks at `/health` with per-provider status
- Structured JSON request logging with SQLite/PostgreSQL persistence
- Admin API with usage stats, request logs, and config history/rollback
- Built-in dashboard UI at `/dashboard`
- HTTP-level connection tracing with DNS, TLS, and first-byte latency

---

## Examples

Integration examples for common use cases are in [ferro-labs/ai-gateway-examples](https://github.com/ferro-labs/ai-gateway-examples):

| Example | Description |
|:--------|:------------|
| [basic](https://github.com/ferro-labs/ai-gateway-examples/tree/main/basic) | Single chat completion to the first configured provider |
| [fallback](https://github.com/ferro-labs/ai-gateway-examples/tree/main/fallback) | Fallback strategy — try providers in order with retries |
| [loadbalance](https://github.com/ferro-labs/ai-gateway-examples/tree/main/loadbalance) | Weighted load balancing across targets (70/30 split) |
| [with-guardrails](https://github.com/ferro-labs/ai-gateway-examples/tree/main/with-guardrails) | Built-in word-filter and max-token guardrail plugins |
| [with-mcp](https://github.com/ferro-labs/ai-gateway-examples/tree/main/with-mcp) | Local MCP server with tool-calling integration |
| [embedded](https://github.com/ferro-labs/ai-gateway-examples/tree/main/embedded) | Embed the gateway as an HTTP handler inside an existing server |

---

## Configuration

Full annotated example — copy to `config.yaml` and customize:

```yaml
# Routing strategy
strategy:
  mode: fallback  # single | fallback | loadbalance | conditional
                  # least-latency | cost-optimized | content-based | ab-test

# Provider targets (tried in order for fallback mode)
targets:
  - virtual_key: openai
    retry:
      attempts: 3
      on_status_codes: [429, 502, 503]
      initial_backoff_ms: 100
  - virtual_key: anthropic
    retry:
      attempts: 2
  - virtual_key: gemini

# Model aliases — resolved before routing
aliases:
  fast: gpt-4o-mini
  smart: claude-3-5-sonnet-20241022
  cheap: gemini-1.5-flash

# Plugins — executed in order at the configured stage
plugins:
  - name: word-filter
    type: guardrail
    stage: before_request
    enabled: true
    config:
      blocked_words: ["password", "secret"]
      case_sensitive: false

  - name: max-token
    type: guardrail
    stage: before_request
    enabled: true
    config:
      max_tokens: 4096
      max_messages: 50

  - name: rate-limit
    type: guardrail
    stage: before_request
    enabled: true
    config:
      requests_per_second: 100
      key_rpm: 60

  - name: request-logger
    type: logging
    stage: before_request
    enabled: true
    config:
      level: info
      persist: true
      backend: sqlite
      dsn: ferrogw-requests.db

# MCP tool servers (optional)
mcp_servers:
  - name: my-tools
    url: https://mcp.example.com/mcp
    headers:
      Authorization: Bearer ${MY_TOOLS_TOKEN}
    allowed_tools: [search, get_weather]
    max_call_depth: 5
    timeout_seconds: 30
```

See [config.example.yaml](config.example.yaml) and [config.example.json](config.example.json) for the full template with all options.

---

## Deployment

### Local development

```bash
export OPENAI_API_KEY=sk-your-key
export GATEWAY_CONFIG=./config.yaml
make build && ./bin/ferrogw
```

### Docker Compose (with PostgreSQL)

```yaml
services:
  ferrogw:
    image: ghcr.io/ferro-labs/ai-gateway:v1.0.0
    ports:
      - "8080:8080"
    environment:
      - OPENAI_API_KEY=${OPENAI_API_KEY}
      - GATEWAY_CONFIG=/etc/ferrogw/config.yaml
      - CONFIG_STORE_BACKEND=postgres
      - CONFIG_STORE_DSN=postgresql://ferrogw:ferrogw@db:5432/ferrogw?sslmode=disable
      - API_KEY_STORE_BACKEND=postgres
      - API_KEY_STORE_DSN=postgresql://ferrogw:ferrogw@db:5432/ferrogw?sslmode=disable
      - REQUEST_LOG_STORE_BACKEND=postgres
      - REQUEST_LOG_STORE_DSN=postgresql://ferrogw:ferrogw@db:5432/ferrogw?sslmode=disable
    volumes:
      - ./config.yaml:/etc/ferrogw/config.yaml:ro
    depends_on:
      - db

  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: ferrogw
      POSTGRES_PASSWORD: ferrogw
      POSTGRES_DB: ferrogw
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

### Kubernetes via Helm

```bash
helm repo add ferro-labs https://ferro-labs.github.io/helm-charts
helm repo update
helm install ferro-gw ferro-labs/ai-gateway \
  --set env.OPENAI_API_KEY=sk-your-key
```

Helm charts: [github.com/ferro-labs/helm-charts](https://github.com/ferro-labs/helm-charts)

---

## Migrate to Ferro Labs AI Gateway

### From LiteLLM

LiteLLM users can migrate in one step. Ferro Labs AI Gateway is OpenAI-compatible — change one line in your code:

**Python (before — LiteLLM):**

```python
from litellm import completion

response = completion(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}]
)
```

**Python (after — Ferro Labs AI Gateway):**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="your-ferro-api-key",
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}],
)
```

**Node.js (after — Ferro Labs AI Gateway):**

```typescript
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "http://localhost:8080/v1",
  apiKey: "your-ferro-api-key",
});

const response = await client.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "Hello" }],
});
```

**Why migrate from LiteLLM:**

- 14x higher throughput at 150 concurrent users (2,447 vs 175 RPS)
- 23x less memory at peak load (47 MB vs 1,124 MB under streaming)
- Single binary — no Python environment, no pip, no virtualenv
- Predictable latency — p99 stays under 65 ms at 150 VU vs LiteLLM's timeouts at the same concurrency

**Config migration:**

```
# LiteLLM config.yaml               # Ferro Labs config.yaml
model_list:                          strategy:
  - model_name: gpt-4o                mode: fallback
    litellm_params:
      model: gpt-4o                  targets:
      api_key: sk-...                  - virtual_key: openai
  - model_name: claude-3-5-sonnet     - virtual_key: anthropic
    litellm_params:
      model: claude-3-5-sonnet       aliases:
      api_key: sk-ant-...              fast: gpt-4o
                                       smart: claude-3-5-sonnet-20241022
```

Provider API keys are set via environment variables (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, etc.) — not in the config file.

### From Portkey

Portkey users: Ferro Labs AI Gateway uses the standard OpenAI SDK — no custom headers required in self-hosted mode.

**Before (Portkey hosted):**

```python
from portkey_ai import Portkey

client = Portkey(api_key="portkey-key")
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}],
)
```

**After (Ferro Labs AI Gateway self-hosted):**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="your-ferro-api-key",
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}],
)
```

**Why migrate from Portkey:**

- Fully open source — no per-request pricing, no log limits
- Self-hosted — your data never leaves your infrastructure
- No vendor lock-in — Apache 2.0 license
- MCP support — Portkey self-hosted lacks native MCP
- FerroCloud (coming soon) for teams that want a managed service

### From OpenAI SDK directly

No gateway yet? Add Ferro Labs AI Gateway in front of your existing code with a single `base_url` change. No other code changes required.

```python
# Before — calling OpenAI directly
client = OpenAI(api_key="sk-...")

# After — routing through Ferro Labs AI Gateway
# Gains: failover, caching, rate limiting, cost tracking
client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="your-ferro-api-key",
)
```

Ferro Labs AI Gateway handles provider failover automatically — if OpenAI is down, your requests fall through to Anthropic or Gemini with zero application code changes.

---

## FerroCloud

FerroCloud — the managed version of Ferro Labs AI Gateway with multi-tenancy, analytics, and cost governance — is coming soon.

👉 **Join the waitlist at [ferrolabs.ai](https://ferrolabs.ai)**

---

## OpenAI SDK Migration

Point existing OpenAI SDK clients to Ferro Labs AI Gateway by changing only the base URL.

**Python:**

```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-ferro-...",
    base_url="http://localhost:8080/v1",
)
```

**TypeScript:**

```typescript
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-ferro-...",
  baseURL: "http://localhost:8080/v1",
});
```

---

## Contributing

We welcome contributions. New providers go in this OSS repo only — never in FerroCloud. See [CONTRIBUTING.md](CONTRIBUTING.md) for branch strategy, commit conventions, and PR guidelines.

---

## Community

- [GitHub Discussions](https://github.com/ferro-labs/ai-gateway/discussions)
- [Discord](https://discord.gg/yCAeYvJeDV)
- Built with Ferro Labs AI Gateway? Open a PR to add to our showcase.

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
