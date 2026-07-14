<div align="center">

<p align="right">
  <a href="README.md">English</a> | <a href="README.zh-CN.md">中文</a>
</p>

<table border="0" cellspacing="0" cellpadding="0"><tr>
  <td rowspan="2"><img src="docs/logo.png" alt="Ferro Labs AI 网关" width="64" /></td>
  <td align="center"><h1>Ferro Labs AI 网关</h1></td>
</tr><tr>
  <td align="center"><strong>开源、OpenAI 兼容的 LLM 网关</strong></td>
</tr></table>

**高性能 Go 语言 AI 网关。通过单一 OpenAI 兼容 API 路由 30+ 个提供商的 LLM 请求。**

**一键部署**

[![部署至 Railway: SQLite](https://railway.com/button.svg)](https://railway.com/deploy/ferro-labs-ai-sqlite-storage?referralCode=KblxKX&utm_medium=integration&utm_source=template&utm_campaign=generic)
[![部署至 Railway: PostgreSQL](https://railway.com/button.svg)](https://railway.com/deploy/ferro-labs-ai-postgresql-storage?referralCode=KblxKX&utm_medium=integration&utm_source=template&utm_campaign=generic)
[![部署至 Render: PostgreSQL](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/ferro-labs/ai-gateway)

[![Go](https://img.shields.io/badge/go-1.25+-00ADD8.svg)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/ferro-labs/ai-gateway.svg)](https://pkg.go.dev/github.com/ferro-labs/ai-gateway)
[![codecov](https://codecov.io/gh/ferro-labs/ai-gateway/branch/main/graph/badge.svg)](https://codecov.io/gh/ferro-labs/ai-gateway)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/ferro-labs/ai-gateway?style=flat&color=yellow)](https://github.com/ferro-labs/ai-gateway/stargazers)
[![CI](https://github.com/ferro-labs/ai-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/ci.yml)
[![Code Scanning](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml/badge.svg)](https://github.com/ferro-labs/ai-gateway/actions/workflows/code-scanning.yml)
[![Ask DeepWiki](https://deepwiki.com/badge.svg?url=https%3A%2F%2Fdeepwiki.com%2Fferro-labs%2Fai-gateway)](https://deepwiki.com/ferro-labs/ai-gateway)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/ferro-labs)](https://artifacthub.io/packages/search?org=ferro-labs)
[![Discord](https://img.shields.io/badge/Discord-加入我们-5865F2?logo=discord&logoColor=white)](https://discord.gg/yCAeYvJeDV)

🔀 **30 个提供商，2,500+ 个模型 — 统一 API**<br/>
⚡ **1,000 并发用户下达 13,925 RPS**<br/>
📦 **单一静态二进制文件，无需外部服务，32 MB 基础内存**

<img src="docs/architecture.svg" alt="Ferro Labs AI 网关架构" width="100%" />

</div>

---

## 快速开始

从零到第一个请求，不超过 2 分钟。

### 方式 A — 二进制文件（最快）

```bash
VER=$(curl -fsSL https://api.github.com/repos/ferro-labs/ai-gateway/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
curl -fsSL "https://github.com/ferro-labs/ai-gateway/releases/download/${VER}/ferrogw_${VER#v}_linux_amd64.tar.gz" | tar xz
chmod +x ferrogw
./ferrogw init          # 生成 config.yaml + MASTER_KEY
./ferrogw               # 启动服务器
```

### 方式 B — Docker

```bash
docker pull ghcr.io/ferro-labs/ai-gateway:latest
docker run -p 8080:8080 \
  -e OPENAI_API_KEY=sk-your-key \
  -e MASTER_KEY=fgw_your-master-key \
  ghcr.io/ferro-labs/ai-gateway:latest
```

### 方式 C — Go

```bash
go install github.com/ferro-labs/ai-gateway/cmd/ferrogw@latest
ferrogw init            # 首次运行配置
ferrogw                 # 启动服务器
```

### 首次配置

`ferrogw init` 会生成主密钥并写入最小化的 `config.yaml`：

```
$ ferrogw init

  主密钥（设置为 MASTER_KEY 环境变量）：
  fgw_a3f2e1d4c5b6a7f8e9d0c1b2a3f4e5d6

  配置已写入：./config.yaml

  后续步骤：
    export MASTER_KEY=fgw_a3f2e1d4c5b6a7f8e9d0c1b2a3f4e5d6
    export OPENAI_API_KEY=sk-...
    ferrogw
```

主密钥仅显示一次——请将其保存到 `.env` 文件或密钥管理器中。它不会写入磁盘。

<div align="center">
  <img src="docs/demo.gif" alt="Ferro Labs AI 网关 — 快速开始演示" width="720" />
</div>

### 最简配置

创建 `config.yaml`（或使用 `ferrogw init`）：

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

### 第一个请求

```bash
export OPENAI_API_KEY=sk-your-key
export MASTER_KEY=fgw_your-master-key

curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $MASTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello from Ferro Labs AI Gateway"}]
  }' | jq
```

---

## 为什么选择 Ferro Labs AI 网关

大多数 AI 网关是在高负载下崩溃的 Python 代理，或是占用大量内存的 JavaScript 服务。Ferro Labs AI 网关从头开始用 Go 编写，专为真实世界的吞吐量设计——单一二进制文件，以可预测的延迟和极低的资源消耗路由 LLM 请求。

| 功能             | Ferro Labs  | LiteLLM | Bifrost    | Kong AI     |
|:-----------------|:------------|:--------|:-----------|:------------|
| 开发语言         | Go          | Python  | Go         | Go/Lua      |
| 单一二进制       | ✅          | ❌      | ✅         | ❌          |
| 提供商数量       | 30          | 100+    | 20+        | 10+         |
| MCP 支持         | ✅          | ❌      | ✅         | ❌          |
| 响应缓存         | ✅          | ✅      | ✅         | ❌（付费）  |
| 防护栏           | ✅          | ✅      | ❌         | ❌（付费）  |
| 开源协议         | Apache 2.0  | MIT     | Apache 2.0 | Apache 2.0  |
| 托管云服务       | 即将推出    | ✅      | ✅         | ✅          |

---

## 性能

在 **GCP n2-standard-8**（8 vCPU，32 GB RAM）上使用 **60ms 固定延迟模拟上游**，对 Kong OSS、Bifrost、LiteLLM 和 Portkey 进行基准测试——结果反映的是纯网关开销。

![吞吐量对比 — Ferro Labs vs Kong、Bifrost、LiteLLM、Portkey，150–1,000 VU](docs/benchmarks/throughput-comparison.png)

### Ferro Labs 延迟概况

| 虚拟用户数 | RPS | p50 | p99 | 内存 |
|---:|---:|---:|---:|---:|
| 50 | 813 | 61.3ms | 64.1ms | 36 MB |
| 150 | 2,447 | 61.2ms | 63.4ms | 47 MB |
| 300 | 4,890 | 61.2ms | 64.4ms | 72 MB |
| 500 | 8,014 | 61.5ms | 72.9ms | 89 MB |
| 1,000 | 13,925 | 68.1ms | 111.9ms | 135 MB |

1,000 VU 下：**13,925 RPS**，p50 开销 **8.1ms**，内存 **135 MB**。无连接池失败，无吞吐量上限。

### 真实上游开销（OpenAI API）

针对**真实 OpenAI API**（gpt-4o-mini）使用两种独立方法测量：网关的 `X-Gateway-Overhead-Ms` 响应头（精确内部计时）和成对的直连与网关请求对比（外部黑盒验证）。

| 配置 | 开销 p50 | 开销 p99 |
|:---|---:|---:|
| 无插件（裸代理） | **0.002ms**（2 微秒） | 0.03ms |
| 含插件（词过滤、最大令牌、日志、速率限制） | **0.025ms**（25 微秒） | 0.074ms |

在典型生产配置下，网关每个请求增加 **25 微秒**的处理开销。LLM API 调用需要 500ms-2s——网关比其代理的提供商快 20,000 倍。

### 如何复现

```bash
git clone https://github.com/ferro-labs/ai-gateway-performance-benchmarks
cd ai-gateway-performance-benchmarks
make setup && make bench
```

完整方法论、原始结果和火焰图分析：
[ferro-labs/ai-gateway-performance-benchmarks](https://github.com/ferro-labs/ai-gateway-performance-benchmarks)

---

## 功能特性

### 🔀 路由

- **8 种路由策略：** 单一、回退、负载均衡、最低延迟、成本优化、基于内容、A/B 测试、条件路由
- 提供商故障转移，支持可配置的重试策略和状态码过滤
- 每请求模型别名（`fast → gpt-4o-mini`，`smart → claude-3-5-sonnet`）

### 🔌 提供商（30 个）

| OpenAI 及兼容 | Anthropic 及 Google | 云端及企业 | 开源及推理 |
|:---|:---|:---|:---|
| OpenAI | Anthropic | AWS Bedrock | Ollama, Ollama Cloud |
| Azure OpenAI | Google Gemini | Azure Foundry | Hugging Face |
| OpenRouter | Vertex AI | Databricks | Replicate |
| DeepSeek | | Cloudflare Workers AI | Together AI |
| Perplexity | | | Fireworks |
| xAI（Grok） | | | DeepInfra |
| Mistral | | | NVIDIA NIM |
| Groq | | | SambaNova |
| Cohere | | | Novita AI |
| AI21 | | | Cerebras |
| Moonshot / Kimi | | | Qwen / DashScope |

### 🛡️ 防护栏与插件

- **词语/短语过滤** — 在请求到达提供商前屏蔽敏感词
- **令牌和消息限制** — 对每个请求强制执行 max_tokens 和 max_messages
- **响应缓存** — 内存缓存，支持可配置的 TTL 和条目限制
- **速率限制** — 全局 RPS 以及每个 API 密钥和每个用户的 RPM 限制
- **预算控制** — 每个 API 密钥的美元支出跟踪，支持可配置的令牌定价
- **请求日志** — 结构化日志，支持可选的 SQLite/PostgreSQL 持久化

### ⚡ 性能

- 每个提供商的 HTTP 连接池，设置经过优化
- `sync.Pool` 用于 JSON 序列化缓冲区和流式 I/O
- 零分配流检测，异步钩子派发批处理
- 单一二进制，约 32 MB 基础内存，线性扩展至 1,000+ VU

### 🤖 MCP（模型上下文协议）

- 代理工具调用循环 — 网关自动驱动 `tool_calls`
- 可流式传输的 HTTP 传输（MCP 2025-11-25 规范）
- 使用 `allowed_tools` 进行工具过滤，并通过 `max_call_depth` 进行有界控制
- 多个 MCP 服务器，支持跨服务器工具去重

### 📊 可观测性

- **OpenTelemetry 链路追踪**（v1.1.0+）—— OTLP gRPC/HTTP 导出器、W3C `traceparent` 传播、GenAI 语义约定（`gen_ai.*`）以及用于成本、路由、MCP 和流式时序的 `ferro.*` 扩展属性；错误记录受 `privacy_level` 约束；`shutdown_grace` 可配置
- `/metrics` 端点提供 Prometheus 指标
- `/health` 端点提供深度健康检查，包含每个提供商的状态
- 结构化 JSON 请求日志，支持 SQLite/PostgreSQL 持久化（trace ID 在日志、OTel span 与 `X-Request-ID` 响应头之间保持统一）
- 管理 API，提供使用统计、请求日志和配置历史/回滚
- `/dashboard` 内置仪表盘 UI
- HTTP 级连接追踪，包含 DNS、TLS 和首字节延迟

---

## 示例

常见用例的集成示例位于 [ferro-labs/ai-gateway-examples](https://github.com/ferro-labs/ai-gateway-examples)：

| 示例 | 描述 |
|:--------|:------------|
| [basic](https://github.com/ferro-labs/ai-gateway-examples/tree/main/basic) | 向第一个已配置提供商发送单次对话补全 |
| [fallback](https://github.com/ferro-labs/ai-gateway-examples/tree/main/fallback) | 回退策略 — 按顺序尝试提供商并重试 |
| [loadbalance](https://github.com/ferro-labs/ai-gateway-examples/tree/main/loadbalance) | 跨目标加权负载均衡（70/30 分配） |
| [with-guardrails](https://github.com/ferro-labs/ai-gateway-examples/tree/main/with-guardrails) | 内置词过滤和最大令牌防护栏插件 |
| [with-mcp](https://github.com/ferro-labs/ai-gateway-examples/tree/main/with-mcp) | 本地 MCP 服务器与工具调用集成 |
| [embedded](https://github.com/ferro-labs/ai-gateway-examples/tree/main/embedded) | 将网关作为 HTTP 处理器嵌入现有服务器 |

---

## 配置

完整注释示例——复制到 `config.yaml` 并自定义：

```yaml
# 路由策略
strategy:
  mode: fallback  # single | fallback | loadbalance | conditional
                  # least-latency | cost-optimized | content-based | ab-test

# 提供商目标（回退模式下按顺序尝试）
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

# 模型别名 — 在路由前解析
aliases:
  fast: gpt-4o-mini
  smart: claude-3-5-sonnet-20241022
  cheap: gemini-1.5-flash

# 插件 — 按配置阶段顺序执行
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

# MCP 工具服务器（可选）
mcp_servers:
  - name: my-tools
    url: https://mcp.example.com/mcp
    headers:
      Authorization: Bearer ${MY_TOOLS_TOKEN}
    allowed_tools: [search, get_weather]
    max_call_depth: 5
    timeout_seconds: 30
```

完整模板及所有选项，请参阅 [config.example.yaml](config.example.yaml) 和 [config.example.json](config.example.json)。

### 关键环境变量

| 变量 | 用途 |
|------|------|
| `MASTER_KEY` | 所有认证的单一管理凭证（由 `ferrogw init` 生成） |
| `GATEWAY_CONFIG` | 配置文件路径（YAML/JSON） |
| `GATEWAY_ENV` | 设置为 `production` 以启用生产模式安全守卫 |
| `PORT` | 服务端口（默认：`8080`） |
| `ALLOW_UNAUTHENTICATED_PROXY` | 设置为 `true` 以禁用代理路由认证（仅开发环境；当 `GATEWAY_ENV=production` 时被阻止） |
| `CORS_ORIGINS` | 逗号分隔的允许 CORS 来源；未设置时拒绝跨域访问 |
| `TRUSTED_PROXIES` | 逗号分隔的可信反向代理 CIDR；仅来自这些地址的 `X-Forwarded-For`/`X-Real-IP` 会被信任（默认：回环地址） |

完整环境变量参考（含提供商 API 密钥和 OTel 配置），请参阅 [AGENTS.md](AGENTS.md)。

---

## 可观测性

Ferro Labs AI 网关在 v1.1.0+ 中提供一流的 **OpenTelemetry** 支持。当 OTel 关闭时（默认），网关使用零分配的 no-op provider 运行——保持关闭没有任何开销。一旦设置了 OTLP 端点，每个请求都会生成一个 `gateway.request` 根 span，携带丰富的 GenAI 语义约定以及用于成本、路由和流式时序的 Ferro 专有扩展。

### 一步启用

设置标准的 OTel 环境变量：

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
ferrogw serve
```

……或在 `config.yaml` 中添加 `observability` 配置块：

```yaml
observability:
  tracing:
    enabled: true
    endpoint: localhost:4317   # 留空则读取 OTEL_EXPORTER_OTLP_ENDPOINT
    protocol: grpc             # grpc | http/protobuf
    service_name: ferrogw
    sample_ratio: 1.0
    privacy_level: metadata    # none | metadata | full（见下文）
    shutdown_grace: 10s        # 每个 OTel 关闭阶段的等待时间；总耗时最多可达该值的 2 倍
    # headers:                          # 认证后端所需的 OTLP 导出请求头
    #   dd-api-key: "${DATADOG_API_KEY}"  # 值支持 ${ENV_VAR} 环境变量插值

  # exporters 用于接入插件式可观测性导出器（见下文"插件导出器"）。
  # exporters:
  #   - name: langsmith
  #     enabled: true
  #     config:
  #       api_key: "${LANGSMITH_API_KEY}"
```

标准 `OTEL_*` 环境变量（如 `OTEL_EXPORTER_OTLP_ENDPOINT`、`OTEL_TRACES_SAMPLER`）始终优先于配置文件——这符合 OTel SDK 约定，也是容器化部署可预测性的要求。

`observability.tracing.headers` 允许向已认证的托管后端（Datadog、New Relic、Honeycomb、Grafana Cloud）发送 OTLP 链路数据，只需配置对应的 vendor 请求头（如 API Key）。值支持 `${ENV_VAR}` 插值，因此密钥不会以明文形式存储在配置文件中。标准环境变量 `OTEL_EXPORTER_OTLP_HEADERS` 同样适用，符合 OTel 约定。

**端点 scheme 决定传输安全性**：`https://` 端点使用 TLS，而 `http://` 端点或裸 `host:port`（如 `localhost:4317`）以明文连接。托管后端需使用 `https://` 形式。

### 会发出哪些数据

以下属性**当前会**在 `gateway.request` 根 span 上发出。标注为"计划中"的属性已预留但尚未接入。

- **`gateway.request`** 每个请求一个根 span（`SERVER` 类型），包含 `gen_ai.system`、`gen_ai.operation.name`、`gen_ai.request.model`、`gen_ai.response.model`、`gen_ai.usage.{input,output}_tokens`
- **`HTTP {GET,POST}`** 每个出站提供商调用一个子 span（`CLIENT` 类型，通过 `otelhttp` 传输包装）——将 `traceparent` 传播给上游提供商
- **`ferro.*` 已发出属性**：`ferro.cost.{usd,input_usd,output_usd,cache_read_usd,cache_write_usd,reasoning_usd,model_found}`、`ferro.routing.{strategy,target_key}`、`ferro.stream.time_to_{first,last}_token_ms`、`ferro.gateway.trace_id`、`ferro.plugin.{name,kind,stage,outcome,reason}`、`ferro.mcp.{server,tool,latency_ms}`
- **W3C TraceContext + Baggage** 传播：尊重入站 `traceparent`；出站请求继续向下传递
- **统一 trace ID**：对于通过网关 HTTP 栈处理的所有请求，OTel `trace_id`、`X-Request-ID` 响应头以及每条日志行的 `trace_id` 字段在每个请求内保证相等。（绕过 `logging.Middleware` 的嵌入式调用方会获得一个一致但独立的 span trace ID。）

### 使用 Jaeger 本地试用

```bash
docker run --rm -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 ferrogw serve
# 发起一个请求，然后打开 http://localhost:16686
```

### 隐私级别

`privacy_level` 控制错误消息在 span 上的记录方式。任何级别都**不会**导出 prompt 或响应内容——那需要未来的 L3 导出器插件。

| 级别 | span 上的错误记录 | 默认 |
|:------|:------|:------|
| `none` | 状态和异常仅携带静态字符串 `"redacted"`——不暴露任何内容或内部类型 | — |
| `metadata` | 错误消息在附加前先经过脱敏（email / JWT / AWS 密钥替换为标记） | ✅ |
| `full` | 原始错误文本不经脱敏直接记录——仅用于受信任的自托管调试 | — |

非法取值会在启动时被配置校验拒绝。

### 插件导出器

`observability.exporters` 配置块用于接入插件导出器，它们在每个请求上接收 `gateway.request.completed` 和 `gateway.request.failed` 事件。导出器的工作与是否配置 OTLP 追踪端点无关。

**本仓库不内置任何导出器插件。** 它们由 `ai-gateway-plugins` 仓库提供，并在其 `init()` 中通过 `observability.RegisterExporter` 自注册。`observability.Exporter` 契约自 v1.1.0 起保持稳定。无法识别或初始化失败的导出器会发出警告并被跳过——网关仍会正常启动。

---

## CLI

`ferrogw` 是单一二进制文件——无需单独的 CLI 工具。

| 命令 | 描述 |
|:--------|:------------|
| `ferrogw serve` | 启动网关服务器 |
| `ferrogw init` | 首次运行配置——生成主密钥和配置文件 |
| `ferrogw validate` | 验证配置文件而不启动服务 |
| `ferrogw doctor` | 检查环境（API 密钥、配置、连通性） |
| `ferrogw status` | 显示网关健康状态和提供商状态 |
| `ferrogw version` | 打印版本、提交和构建信息 |
| `ferrogw admin keys list` | 列出 API 密钥 |
| `ferrogw admin keys create <name>` | 创建 API 密钥 |
| `ferrogw admin logs stats` | 显示使用统计 |
| `ferrogw plugins` | 列出已注册插件 |

所有子命令可用的全局标志：`--gateway-url`、`--api-key`、`--format`（table/json/yaml）。

---

## 部署

### 本地开发

```bash
export OPENAI_API_KEY=sk-your-key
export MASTER_KEY=fgw_your-master-key
export GATEWAY_CONFIG=./config.yaml
make build && ./bin/ferrogw
```

### Railway（SQLite）

如需使用持久化 SQLite 存储快速部署到 Railway，请在 `/data` 挂载 Railway Volume 并设置：

```bash
MASTER_KEY=fgw_your-master-key
OPENAI_API_KEY=sk-your-key
PORT=8080
API_KEY_STORE_BACKEND=sqlite
API_KEY_STORE_DSN=/data/keys.db
CONFIG_STORE_BACKEND=sqlite
CONFIG_STORE_DSN=/data/config.db
REQUEST_LOG_STORE_BACKEND=sqlite
REQUEST_LOG_STORE_DSN=/data/logs.db
RAILWAY_RUN_UID=0
```

### Render（PostgreSQL）

仓库包含 `render.yaml` Blueprint，用于一键 Render 部署，包含 Docker Web 服务和托管 Postgres 数据库。它会自动生成 `MASTER_KEY`，向用户询问 `OPENAI_API_KEY`，并自动将三个存储 DSN 连接到数据库的内部连接字符串。

使用 README 顶部的按钮，或直接从以下地址部署：

```text
https://render.com/deploy?repo=https://github.com/ferro-labs/ai-gateway
```

### 方式 D — Docker Compose（开发和生产）

仓库附带三个遵循标准覆盖模式的 Compose 文件：

| 文件 | 用途 |
|---|---|
| `docker-compose.yml` | 基础 — 共享镜像、端口映射、所有提供商环境变量存根 |
| `docker-compose.dev.yml` | 开发 — 从源码构建、调试日志、实时配置挂载、Ollama 主机访问 |
| `docker-compose.prod.yml` | 生产 — 固定镜像标签、重启策略、健康检查、资源限制、日志轮转 |

**开发**（从源码构建）：

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up
```

**生产**（固定发布标签——生产环境切勿使用 `latest`）：

```bash
# 替换为最新发布标签
IMAGE_TAG=v1.1.7 CORS_ORIGINS=https://your-domain.com \
  docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

提供商 API 密钥在 `docker-compose.yml` 中已注释。取消注释并设置所需的密钥，或通过同目录下的 `.env` 文件提供。

---

### Docker Compose（含 PostgreSQL）

```yaml
services:
  ferrogw:
    image: ghcr.io/ferro-labs/ai-gateway:latest
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

### Kubernetes（通过 Helm）

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/ferro-labs)](https://artifacthub.io/packages/search?org=ferro-labs)

```bash
helm repo add ferro-labs https://ferro-labs.github.io/helm-charts
helm repo update
helm install ferro-gw ferro-labs/ai-gateway \
  --set env.OPENAI_API_KEY=sk-your-key
```

Helm 图表：[github.com/ferro-labs/helm-charts](https://github.com/ferro-labs/helm-charts) | [ArtifactHub](https://artifacthub.io/packages/search?org=ferro-labs)

---

## 迁移至 Ferro Labs AI 网关

### 从 LiteLLM 迁移

LiteLLM 用户可一步完成迁移。Ferro Labs AI 网关兼容 OpenAI——只需更改代码中的一行：

**Python（迁移前 — LiteLLM）：**

```python
from litellm import completion

response = completion(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}]
)
```

**Python（迁移后 — Ferro Labs AI 网关）：**

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

**Node.js（迁移后 — Ferro Labs AI 网关）：**

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

**从 LiteLLM 迁移的理由：**

- 150 并发用户下吞吐量高 14 倍（2,447 vs 175 RPS）
- 峰值负载内存减少 23 倍（47 MB vs 流式传输下的 1,124 MB）
- 单一二进制——无需 Python 环境、pip 或 virtualenv
- 可预测的延迟——p99 在 150 VU 下保持在 65ms 以内，而 LiteLLM 在相同并发下会超时

**配置迁移：**

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

提供商 API 密钥通过环境变量（`OPENAI_API_KEY`、`ANTHROPIC_API_KEY` 等）设置——不在配置文件中。

### 从 Portkey 迁移

Portkey 用户：Ferro Labs AI 网关使用标准 OpenAI SDK——自托管模式无需自定义请求头。

**迁移前（Portkey 托管）：**

```python
from portkey_ai import Portkey

client = Portkey(api_key="portkey-key")
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}],
)
```

**迁移后（Ferro Labs AI 网关自托管）：**

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

**从 Portkey 迁移的理由：**

- 完全开源——无按请求计费，无日志限制
- 自托管——您的数据永远不会离开您的基础设施
- 无供应商锁定——Apache 2.0 协议
- MCP 支持——Portkey 自托管版本缺乏原生 MCP
- FerroCloud（即将推出）适合需要托管服务的团队

### 从 OpenAI SDK 直接迁移

还没有网关？只需更改一个 `base_url`，即可在现有代码前添加 Ferro Labs AI 网关，无需其他代码改动。

```python
# 迁移前 — 直接调用 OpenAI
client = OpenAI(api_key="sk-...")

# 迁移后 — 通过 Ferro Labs AI 网关路由
# 优势：故障转移、缓存、速率限制、成本跟踪
client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="your-ferro-api-key",
)
```

Ferro Labs AI 网关自动处理提供商故障转移——如果 OpenAI 宕机，您的请求会自动转至 Anthropic 或 Gemini，无需更改任何应用代码。

---

## FerroCloud

FerroCloud——Ferro Labs AI 网关的托管版本，支持多租户、分析和成本管理——即将推出。

👉 **在 [ferrolabs.ai](https://ferrolabs.ai) 加入候补名单**

---

## SDK

Ferro Labs AI 网关官方客户端库：

| SDK | 安装 | 仓库 |
|:----|:-----|:-----|
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

### 兼容 OpenAI SDK

您也可以直接使用标准 OpenAI SDK——只需更改 base URL：

**Python：**

```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-ferro-...",
    base_url="http://localhost:8080/v1",
)
```

**TypeScript：**

```typescript
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-ferro-...",
  baseURL: "http://localhost:8080/v1",
});
```

---

## 贡献

我们欢迎贡献。新提供商仅在此开源仓库添加——绝不在 FerroCloud 中。请参阅 [CONTRIBUTING.md](CONTRIBUTING.md) 了解分支策略、提交规范和 PR 指南。

---

## 社区

- [GitHub Discussions](https://github.com/ferro-labs/ai-gateway/discussions)
- [Discord](https://discord.gg/yCAeYvJeDV)
- 使用 Ferro Labs AI 网关构建了项目？欢迎提交 PR 将其加入我们的展示列表。

---

## 许可证

Apache 2.0 — 请参阅 [LICENSE](LICENSE)。
