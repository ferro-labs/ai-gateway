<div align="center">

<h1 align="center">
  <img src="docs/logo.png" alt="Ferro Logo" height="60" align="absmiddle" /> Ferro AI Gateway
</h1>

**The high-performance, open-source control plane for your AI applications.**  
Route, observe, and secure requests across 100+ LLM providers via a single OpenAI-compatible API.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/ferro-labs/ai-gateway.svg)](https://pkg.go.dev/github.com/ferro-labs/ai-gateway)
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
* **Enterprise Reliability:** Native server-sent events (SSE) streaming support and drop-in client compatibility.
* **Extensible Middleware:** Intercept requests via pluggable plugins for Guardrails (PII/word filtering), Token Limiting, exact-match Caching, and Request Logging.
* **Secure Access Manager:** Centrally issue scoped, auto-expiring API keys with native RBAC. Zero external database required for stand-up.

---

## ⚡ Quick Start

### Run via Docker

The fastest way to get started is pulling the official image from GitHub Container Registry.

```bash
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY=sk-your-key \
  ghcr.io/ferro-labs/ai-gateway:latest

# To run the absolute latest unreleased code from main:
# ghcr.io/ferro-labs/ai-gateway:edge
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

- [x] **v0.1.0** — Foundation Release: Core routing, multi-provider execution, basic guardrails, and streaming capabilities.
- [ ] **v0.2.0** — Observability & Resilience: Structured telemetry, Prometheus metrics hooks, robust circuit breaking, file-backed key storage, and deep health checks.
- [ ] **v0.3.0** — Modality Expansions: Embeddings, Image generation mapping, Cost tracking via pricing tables, and Model aliasing.
- [ ] **v0.4.0** — Persistent State: Dedicated Admin API, SQLite/PostgreSQL persistence, robust CRUD configuration portals.
- [ ] **v0.5.0** — Advanced Intelligence: Least-latency and Cost-optimized algorithmic routing, A/B Testing modules, and Semantic Caching.
- [ ] **v1.0.0** — Production Ready: Helm charts, open-telemetry export, edge caching, and official SDK embeddings.

*Review our detailed [ROADMAP.md](ROADMAP.md) for deeper implementation plans.*

---

## 🤝 Contributing

We welcome community contributions! The priority areas for ecosystem growth are:

1. Adding support for new niche LLM providers.
2. Building new middleware plugins (Guardrails, Modifiers, Analyzers).
3. Enhancing test coverage and documentation.

Please see our [CONTRIBUTING.md](CONTRIBUTING.md) for style guidelines and PR processes.

## 📄 License

FerroGateway is proudly open-source and released under the [Apache 2.0 License](LICENSE).
