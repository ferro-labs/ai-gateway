<div align="center">

<p align="right">
  <a href="README.md">English</a> | <a href="README.zh-CN.md">中文</a>
</p>

<table border="0" cellspacing="0" cellpadding="0"><tr>
  <td rowspan="2"><img src="docs/logo.png" alt="Ferro Labs AI Gateway" width="64" /></td>
  <td align="center"><h1>Ferro Labs AI Gateway</h1></td>
</tr><tr>
  <td align="center"><strong>Open-Source, OpenAI-Compatible LLM Gateway</strong></td>
</tr></table>

**High-performance AI gateway in Go. Route LLM requests across 30 providers via a single OpenAI-compatible API.**

**Deploy templates**

[![Deploy on Railway: SQLite](https://railway.com/button.svg)](https://railway.com/deploy/ferro-labs-ai-sqlite-storage?referralCode=KblxKX&utm_medium=integration&utm_source=template&utm_campaign=generic)
[![Deploy on Railway: PostgreSQL](https://railway.com/button.svg)](https://railway.com/deploy/ferro-labs-ai-postgresql-storage?referralCode=KblxKX&utm_medium=integration&utm_source=template&utm_campaign=generic)
[![Deploy to Render: PostgreSQL](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/ferro-labs/ai-gateway)

[![Go](https://img.shields.io/badge/go-1.25+-00ADD8.svg)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/ferro-labs/ai-gateway.svg)](https://pkg.go.dev/github.com/ferro-labs/ai-gateway)
[![codecov](https://codecov.io/gh/ferro-labs/ai-gateway/branch/main/graph/badge.svg)](https://codecov.io/gh/ferro-labs/ai-gateway)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/ferro-labs/ai-gateway?style=flat&color=yellow)](https://github.com/ferro-labs/ai-gateway/stargazers)
[![CI](https://github.com/ferro-labs/ai-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/ci.yml)
[![Code Scanning](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml)
[![Ask DeepWiki](https://deepwiki.com/badge.svg?url=https%3A%2F%2Fdeepwiki.com%2Fferro-labs%2Fai-gateway)](https://deepwiki.com/ferro-labs/ai-gateway)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/ferro-labs)](https://artifacthub.io/packages/search?org=ferro-labs)
[![Docs](https://img.shields.io/badge/docs-ferrolabs.ai-2ea44f)](https://docs.ferrolabs.ai)
[![Discord](https://img.shields.io/badge/Discord-Join%20Us-5865F2?logo=discord&logoColor=white)](https://discord.gg/yCAeYvJeDV)

📖 **Documentation:** [docs.ferrolabs.ai](https://docs.ferrolabs.ai)

🔀 **30 providers, 2,500+ models — one API**<br/>
⚡ **13,925 RPS at 1,000 concurrent users** ([v1.0.0 benchmark](#performance))<br/>
📦 **Single static binary, no external services required, 32 MB base memory**

<img src="docs/architecture.svg" alt="Ferro Labs AI Gateway Architecture" width="100%" />

</div>

---

## Quick Start

Get from zero to first request in under 2 minutes.

### Option A — Binary (fastest)

```bash
VER=$(curl -fsSL https://api.github.com/repos/ferro-labs/ai-gateway/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
curl -fsSL "https://github.com/ferro-labs/ai-gateway/releases/download/${VER}/ferrogw_${VER#v}_linux_amd64.tar.gz" | tar xz
chmod +x ferrogw
./ferrogw init                        # generates config.yaml + MASTER_KEY
export GATEWAY_CONFIG=./config.yaml   # the server reads a config file only when this is set
export OPENAI_API_KEY=sk-your-key     # providers are registered at startup, so export before starting
export MASTER_KEY=fgw_your-master-key # the key ferrogw init printed
./ferrogw                             # starts the server
```

Releases are signed and ship SBOMs — verification steps are in
[SECURITY.md](SECURITY.md#verifying-releases).

### Option B — Docker

```bash
docker pull ghcr.io/ferro-labs/ai-gateway:latest
docker run -p 8080:8080 \
  -e OPENAI_API_KEY=sk-your-key \
  -e MASTER_KEY=fgw_your-master-key \
  ghcr.io/ferro-labs/ai-gateway:latest
```

### Option C — Go

```bash
go install github.com/ferro-labs/ai-gateway/cmd/ferrogw@latest
ferrogw init                          # first-run setup
export GATEWAY_CONFIG=./config.yaml   # the server reads a config file only when this is set
export OPENAI_API_KEY=sk-your-key     # providers are registered at startup, so export before starting
export MASTER_KEY=fgw_your-master-key # the key ferrogw init printed
ferrogw                               # start the server
```

`ferrogw init` generates the master key and writes a minimal `config.yaml`. The
key is shown **once** and never written to disk — store it in your `.env` file
or secret manager.

<div align="center">
  <img src="docs/demo.gif" alt="Ferro Labs AI Gateway — Quick Start Demo" width="720" />
</div>

### Minimal config

Create `config.yaml` (or use `ferrogw init`), then point the gateway at it with `export GATEWAY_CONFIG=./config.yaml` — a config file is loaded only when that variable names it:

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
export MASTER_KEY=fgw_your-master-key   # the key ferrogw init printed, in whichever shell you curl from

curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $MASTER_KEY" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello from Ferro Labs AI Gateway"}]
  }' | jq
```

---

## Why Ferro Labs AI Gateway

Most AI gateways are Python proxies that crack under load or JavaScript services that eat memory. Ferro Labs AI Gateway is written in Go from the ground up for real-world throughput — a single binary that routes LLM requests with predictable latency and minimal resource usage.

| Feature          | Ferro Labs  | LiteLLM | Bifrost    | Kong AI     |
|:-----------------|:------------|:--------|:-----------|:------------|
| Language         | Go          | Python  | Go         | Go/Lua      |
| Single binary    | ✅          | ❌      | ✅         | ❌          |
| Providers        | 30          | 100+    | 20+        | 10+         |
| MCP support      | ✅          | ❌      | ✅         | ❌          |
| Response cache   | ✅          | ✅      | ✅         | ❌ (paid)   |
| Guardrails       | ✅          | ✅      | ❌         | ❌ (paid)   |
| OSS license      | Apache 2.0  | MIT     | Apache 2.0 | Apache 2.0  |
| Managed cloud    | Coming Soon | ✅      | ✅         | ✅          |

---

## Performance

Every figure in this section comes from one run: **Ferro Labs v1.0.0, measured
2026-03-23**. Benchmarked against Kong OSS, Bifrost, LiteLLM, and Portkey on
**GCP n2-standard-8** (8 vCPU, 32 GB RAM) using a **60ms fixed-latency
mock upstream** — results reflect gateway overhead only. Later releases have not
been re-measured; reproduce against the version you intend to run using the
commands below.

![Throughput comparison — Ferro Labs vs Kong, Bifrost, LiteLLM, Portkey across 150–1,000 VU](docs/benchmarks/throughput-comparison.png)

| VU | RPS | p50 | p99 | Memory |
|---:|---:|---:|---:|---:|
| 50 | 813 | 61.3ms | 64.1ms | 36 MB |
| 150 | 2,447 | 61.2ms | 63.4ms | 47 MB |
| 300 | 4,890 | 61.2ms | 64.4ms | 72 MB |
| 500 | 8,014 | 61.5ms | 72.9ms | 89 MB |
| 1,000 | 13,925 | 68.1ms | 111.9ms | 135 MB |

At 1,000 VU: **13,925 RPS**, p50 overhead **8.1ms**, memory **135 MB**.
Against the live OpenAI API, the gateway itself adds **25 microseconds** p50 in
a typical plugin configuration (2µs bare).

Full methodology, raw results, and flamegraph analysis:
[ferro-labs/ai-gateway-performance-benchmarks](https://github.com/ferro-labs/ai-gateway-performance-benchmarks)
(`make setup && make bench` reproduces it).

---

## Features

### 🔀 Routing

- **8 routing strategies:** single, fallback, load balance, least latency, cost-optimized, content-based, A/B test, conditional — see [internal/strategies/README.md](internal/strategies/README.md)
- Provider failover with configurable retry policies and status code filters
- Cost-optimized routing can explicitly fallback, skip, or allow providers with unknown catalog prices
- Per-request model aliases (`fast → gpt-4o-mini`, `smart → claude-3-5-sonnet`)

### 🔌 Providers (30)

| OpenAI & Compatible | Anthropic & Google | Cloud & Enterprise | Open Source & Inference |
|:---|:---|:---|:---|
| OpenAI | Anthropic | AWS Bedrock | Ollama, Ollama Cloud |
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

Beyond chat and streaming, providers serve **embeddings, images, rerank,
moderations, speech-to-text, text-to-speech, and batch** where the vendor offers
them — the full per-provider endpoint matrix is
[providers/README.md](providers/README.md).

### 🛡️ Guardrails & Plugins

Six plugins ship built-in — word filter, token/message limits, response cache,
rate limiting, per-key budgets, and request logging — and the framework is
public for writing your own. See [plugin/README.md](plugin/README.md).

### 🎯 Provider Capabilities

- **Capability matrix** — one declarative record of which OpenAI parameters each provider forwards, translates, or cannot express
- **`GET /v1/capabilities`** — compare providers programmatically before you route to them
- **Strict mode** — `compatibility.on_unsupported_param: warn | drop | reject`; a parameter the provider cannot honor is no longer silently discarded
- **Conformance-tested** — every provider is built through the same seam the gateway uses and asserted against its real upstream payload shape

### 🤖 MCP (Model Context Protocol)

The gateway connects to MCP tool servers (stdio and Streamable HTTP), injects
their tools into chat completions, and drives the agentic `tool_calls` loop
itself — bounded depth, tool filtering, cross-server dedup. See
[mcp/README.md](mcp/README.md).

### 📊 Observability

- **OpenTelemetry tracing** — OTLP export, W3C propagation, GenAI semantic conventions plus `ferro.*` cost/routing/MCP attributes; a zero-allocation no-op until enabled. See [observability/README.md](observability/README.md)
- **Prometheus metrics** at `/metrics` (authenticated) and deep health checks at `/health`, `/livez`, `/readyz`
- One trace ID threads logs, spans, and the `X-Request-ID` header; request logs persist to SQLite/PostgreSQL

---

## Dashboard

Every gateway binary serves a built-in operations console at `/` — same port as
the API, compiled in with `go:embed`, no second image and no second origin. Sign
in with your `MASTER_KEY` or any admin / read-only key and it reads the live
gateway: traffic, spend, provider health, routing, plugins, request logs, and the
audit trail.

<div align="center">
  <img src="docs/dashboard.gif" alt="Ferro Labs AI Gateway operations console: Overview, Analytics, Providers, Routing Strategies, Plugins, Playground, Tracing, Request Logs, Audit Trail, Configuration, and API Keys" width="100%" />
</div>

Overview, Analytics (latency/TTFT/cost percentiles), Providers, Routing,
Plugins, a Playground over the real routing path, Tracing, Request Logs, the
Audit Trail, Configuration with history/rollback, and scoped API key management.

Run the gateway and open <http://localhost:8080>. To see it filled like the
recording above, bring up the self-contained demo stack:

```bash
make up-fullstack   # gateway + Postgres + Jaeger + Prometheus + Grafana + mock upstream + load generator
# then open http://localhost:8080
```

Build and development details are in [web/README.md](web/README.md).

---

## Documentation

The root README is the overview; each subsystem keeps its own reference beside
its code:

| Reference | Covers |
|:---|:---|
| [providers/README.md](providers/README.md) | The 30 providers, the per-provider endpoint matrix, and every `/v1/*` surface |
| [config/README.md](config/README.md) | Config loading, validation, `${VAR}` secrets, declared models, trusted proxies |
| [internal/strategies/README.md](internal/strategies/README.md) | All 8 routing strategies and their failure semantics |
| [plugin/README.md](plugin/README.md) | The plugin framework and the six built-in plugins |
| [mcp/README.md](mcp/README.md) | MCP tool servers, transports, the subprocess trust boundary, readiness |
| [observability/README.md](observability/README.md) | Tracing setup, managed backends, emitted attributes, privacy levels, exporters |
| [deploy/README.md](deploy/README.md) | Dockerfiles, Compose files, the fullstack demo stack |
| [web/README.md](web/README.md) | Dashboard development and the embed contract |
| [AGENTS.md](AGENTS.md) | The complete operator/developer reference: architecture, every env var, request flow |
| [SECURITY.md](SECURITY.md) | Reporting, security posture, release verification |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Branch strategy, commit conventions, provider/plugin checklists |

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

One YAML/JSON file, named by `GATEWAY_CONFIG`, drives routing, guardrails, MCP
tools, and observability:

```yaml
strategy:
  mode: fallback              # 8 modes — see internal/strategies/README.md

targets:
  - virtual_key: openai       # an allowlist: only listed providers are routable
    retry: { attempts: 3 }    # per target, honoured under every routing mode
    concurrency: { max_concurrency: 32, queue_size: 1000 }
  - virtual_key: anthropic

aliases:
  fast: gpt-4o-mini

plugins:                      # guardrails first, then cache — see plugin/README.md
  - name: word-filter
    type: guardrail
    stage: before_request
    enabled: true
    config: { blocked_words: ["password", "secret"] }

mcp_servers:                  # tool servers — see mcp/README.md
  - name: search
    url: https://mcp.example.com/mcp
    headers: { Authorization: "Bearer ${SEARCH_TOKEN}" }
```

`${VAR}` references (braced form only) resolve when a component is constructed,
never at file load — so secrets are never stored, served by `GET /admin/config`,
or restored by a rollback. A bare `$` is data; an undefined variable is an
error.

The full annotated reference with every option is
[config.example.yaml](config.example.yaml) /
[config.example.json](config.example.json), and the schema guide is
[config/README.md](config/README.md). `ferrogw validate` checks a file without
starting the server.

### Key environment variables

| Variable | Purpose |
|----------|---------|
| `MASTER_KEY` | Bootstrap and break-glass admin credential (generated by `ferrogw init`); give each operator their own key from `POST /admin/keys` for day-to-day use |
| `GATEWAY_CONFIG` | Path to config YAML/JSON |
| `GATEWAY_ENV` | Set to `production` to enable production-mode safety guards: it refuses to start on `ALLOW_UNAUTHENTICATED_PROXY=true` or a `*` entry in `CORS_ORIGINS`, and warns when per-IP rate limiting is off, pprof is mounted, or the API key store is in-memory |
| `PORT` | Server port (default: `8080`) |
| `ALLOW_UNAUTHENTICATED_PROXY` | Set to `true` to disable proxy-route auth (dev only; blocked when `GATEWAY_ENV=production`) |
| `CORS_ORIGINS` | Comma-separated allowed CORS origins; cross-origin is denied when unset. Matched **literally** — there is no wildcard, so list each origin explicitly |
| `TRUSTED_PROXIES` | CIDRs of trusted reverse proxies; forwarded headers are honored only from these (default: loopback). See [config/README.md](config/README.md#trusted-proxies-trusted_proxies) |
| `<PROVIDER>_BASE_URL` | Points a provider at a proxy, self-hosted server, or regional endpoint. It is the **API root**, used verbatim — write it exactly as the vendor documents it, version segment included (`https://api.groq.com/openai/v1`); a bare host resolves to the provider's own version segment |

See [AGENTS.md](AGENTS.md) for the full environment variable reference including provider API keys, store backends, and OTel settings.

---

## Observability

**See everything your gateway does** — every request, what it cost, how long it took, which provider served it, and which guardrails ran. Ferro Labs AI Gateway ships first-class **OpenTelemetry tracing** and **Prometheus metrics** out of the box, and stays at a **zero-allocation no-op until you turn it on**. Point it at **Jaeger, Grafana, New Relic, LangSmith, Datadog, or Honeycomb** — anything that speaks OTLP — and every request emits a `gateway.request` span carrying GenAI semantic conventions (`gen_ai.*`) plus `ferro.*` extensions for cost, routing, MCP tool calls, and stream timings. The same trace ID threads your logs, spans, and the `X-Request-ID` response header.

> 📈 **Full observability guide → [observability/README.md](observability/README.md)** — managed-backend setup, endpoint & transport rules, every emitted attribute, privacy levels, and exporter plugins.

Bring up the gateway wired to a full monitoring stack — **Prometheus, Grafana, and Jaeger** — driven by generated traffic, in one command:

```bash
make up-fullstack   # then open Grafana at http://localhost:3000
```

<p align="center">
  <img src="docs/observability/grafana-dashboard.gif" alt="Grafana dashboard: per-provider request rate, latency percentiles, token cost, and circuit-breaker state" width="100%" />
  <br/>
  <em>Grafana — request rate, latency percentiles, per-provider breakdown, token cost, and circuit-breaker state, all from the gateway's Prometheus metrics.</em>
</p>

<p align="center">
  <img src="docs/observability/jaeger-trace.gif" alt="Jaeger trace: one gateway.request span expanding to show its gen_ai.* and ferro.* attributes" width="100%" />
  <br/>
  <em>Jaeger — one request's <code>gateway.request</code> span, opened to reveal its <code>gen_ai.*</code> and <code>ferro.*</code> attributes.</em>
</p>

Enable tracing with one variable (or the `observability:` config block —
endpoint, protocol, sampling, privacy, headers are all documented in the guide):

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
ferrogw serve
```

---

## CLI

`ferrogw` is a single binary — no separate CLI tool required.

| Command | Description |
|:--------|:------------|
| `ferrogw` | Start the gateway server (default) |
| `ferrogw serve` | Start the gateway server (explicit) |
| `ferrogw init` | First-run setup — generate master key and config |
| `ferrogw validate` | Validate a config file without starting |
| `ferrogw doctor` | Check environment (API keys, config, connectivity) |
| `ferrogw status` | Show gateway health and provider status |
| `ferrogw version` | Print version, commit, and build info |
| `ferrogw admin keys list` | List API keys |
| `ferrogw admin keys create --name <name>` | Create an API key (`--scope`, `--expires-in`) |
| `ferrogw admin logs stats` | Show request log statistics |
| `ferrogw plugins` | List registered plugins |

Global flags available on all subcommands: `--gateway-url`, `--api-key`, `--format` (table/json/yaml).

---

## Deployment

### Local development

```bash
export OPENAI_API_KEY=sk-your-key
export MASTER_KEY=fgw_your-master-key
export GATEWAY_CONFIG=./config.yaml
make build && ./bin/ferrogw
```

### Railway & Render

The deploy buttons at the top of this README provision either platform: Railway
with SQLite on a volume (set the three `*_STORE_DSN` variables to paths under
`/data`) or PostgreSQL, and Render from the repo's `render.yaml` Blueprint,
which generates `MASTER_KEY` and wires the store DSNs to a managed Postgres
automatically.

### Docker Compose

Three Compose files in `deploy/` follow the standard override pattern — a shared
base, a dev override that builds from source, and a prod override with a pinned
tag, health check, and resource limits. Run everything from the repository root:

```bash
make up             # dev: builds from source
IMAGE_TAG=v1.4.0 CORS_ORIGINS=https://your-domain.com make up-prod
make down           # tears down either
```

One container serves both the API and the dashboard — no second image, no second
origin. Provider keys go in a repository-root `.env` or the environment.
[deploy/README.md](deploy/README.md) has the full reference, including a
self-contained PostgreSQL pairing and the fullstack observability stack.

### Kubernetes via Helm

```bash
helm repo add ferro-labs https://ferro-labs.github.io/helm-charts
helm repo update
helm install ferro-gw ferro-labs/ai-gateway \
  --set env.OPENAI_API_KEY=sk-your-key
```

Helm charts: [github.com/ferro-labs/helm-charts](https://github.com/ferro-labs/helm-charts) | [ArtifactHub](https://artifacthub.io/packages/search?org=ferro-labs)

---

## Migrate to Ferro Labs AI Gateway

The gateway is OpenAI-compatible, so migration from any gateway — or from
calling a provider directly — is a `base_url` change.

### From LiteLLM

**Before (LiteLLM):**

```python
from litellm import completion

response = completion(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}]
)
```

**After (Ferro Labs AI Gateway):**

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

Provider API keys move to environment variables (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, …); the model list becomes `targets` + `aliases` in
`config.yaml`.

**Why migrate from LiteLLM:**

- 14x higher throughput at 150 concurrent users (2,447 vs 175 RPS)
- 23x less memory at peak load (47 MB vs 1,124 MB under streaming)
- Single binary — no Python environment, no pip, no virtualenv
- Predictable latency — p99 stays under 65 ms at 150 VU vs LiteLLM's timeouts at the same concurrency

### From Portkey

The code change is the same one line — Ferro Labs uses the standard OpenAI SDK
with no custom headers in self-hosted mode.

**Why migrate from Portkey:**

- Fully open source — no per-request pricing, no log limits
- Self-hosted — your data never leaves your infrastructure
- No vendor lock-in — Apache 2.0 license
- MCP support — Portkey self-hosted lacks native MCP
- FerroCloud (coming soon) for teams that want a managed service

---

## FerroCloud

FerroCloud — the managed version of Ferro Labs AI Gateway with multi-tenancy, analytics, and cost governance — is coming soon.

👉 **Join the waitlist at [ferrolabs.ai](https://ferrolabs.ai)**

---

## SDKs

Official client libraries for the Ferro Labs AI Gateway — and the standard
OpenAI SDK works unchanged: point `base_url` at `http://your-gateway:8080/v1`.

| SDK | Install | Repository |
|:----|:--------|:-----------|
| Python | `pip install ferrolabs` | [ferro-labs/ferrolabs-python-sdk](https://github.com/ferro-labs/ferrolabs-python-sdk) |
| TypeScript | `npm install ferrolabs` | [ferro-labs/ferrolabs-typescript-sdk](https://github.com/ferro-labs/ferrolabs-typescript-sdk) |

<details>
<summary><strong>Python</strong></summary>

```python
from ferrolabs import FerroClient

client = FerroClient(
    base_url="http://localhost:8080/v1",
    api_key="your-ferro-api-key",
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}],
)
```

</details>

<details>
<summary><strong>TypeScript</strong></summary>

```typescript
import { FerroClient } from "ferrolabs";

const client = new FerroClient({
  baseURL: "http://localhost:8080/v1",
  apiKey: "your-ferro-api-key",
});

const response = await client.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "Hello" }],
});
```

</details>

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
