# Plugins

Plugins are middleware that run around each request the gateway routes. They can
inspect or rewrite a request before it reaches a provider, screen or record the
response after, and note failures — enough to build guardrails, caching, rate
limiting, budgets, and logging without touching the routing core. Six ship
built-in; the framework is public, so you can add your own.

This package holds the framework (`plugin.go`, `manager.go`, `registry.go`) and
the built-in plugins (one subpackage each). For the config reference with
inline comments, see [`../config.example.yaml`](../config.example.yaml).

## How plugins run

A plugin attaches to one or more **stages** of the request lifecycle:

| Stage | When it runs |
|---|---|
| `before_request` | after auth, before the provider is called — the place to inspect, rewrite, or reject |
| `after_request` | on success, with the response available — the place to screen or record it |
| `on_error` | when the request failed — the place to record the failure |

Plugins run in the order they are written within a stage. Configure them under
`plugins[]`:

```yaml
plugins:
  - name: word-filter
    type: guardrail
    stage: before_request
    enabled: true
    config:
      blocked_words: ["password", "secret"]
```

Config values support `${VAR}` environment references (braced form only),
resolved when the plugin is constructed.

## Deny vs. break

Two outcomes are deliberately different:

- **Deny** — a plugin decides the request may not proceed (a guardrail trips, a
  budget is spent). This is a normal 4xx/429 answer, not an error.
- **Break** — the plugin itself fails (a bug, a panic, a dependency down). What
  happens then depends on the plugin's **type**: `logging` and `metrics` plugins
  **fail open** (the request continues; the breakage costs a log line), and every
  other type **fails closed** (the request is refused with 500). A guardrail that
  cannot run must not wave traffic through.

## Multi-stage plugins

Some plugins do work at more than one stage — `response-cache` (serve, then
store), `budget` (check, then record), `request-logger` (record at each stage).
Each stage is **one `plugins[]` entry**, and the entries for one plugin **must
carry identical `config`** so they resolve to a single shared instance. The
gateway refuses to start if they disagree.

## Built-in plugins

| Plugin | Type | Stage(s) | What it does |
|---|---|---|---|
| **word-filter** | guardrail | before_request (and after_request to screen the response) | Rejects a request whose text contains a blocked entry as a substring. |
| **max-token** | guardrail | before_request | Rejects a request that declares a completion ceiling above the limit, or exceeds the message-count / input-length limit. It never *imposes* a ceiling. |
| **rate-limit** | ratelimit | before_request | Bounds request rate globally and per API key or user, independently of the per-IP HTTP limiter. |
| **budget** | ratelimit | before_request + after_request | Tracks estimated spend per API key and refuses requests once the budget is exhausted. |
| **response-cache** | transform | before_request + after_request | Serves an identical request from memory instead of calling a provider again, scoped to the API key that primed it. |
| **request-logger** | logging | before_request + after_request + on_error | Records each request for the Request Logs page, and persists it when a request-log store is configured. |

### word-filter

Substring match against a blocklist. At `before_request` it screens the prompt;
listed at `after_request` it screens the response too.

```yaml
config:
  blocked_words: ["password", "secret"]
  case_sensitive: false
```

### max-token

Caps how large a completion a caller may **ask for**, plus message-count and
total input-length limits. A request that declares no ceiling is uncapped —
this plugin rejects over-large requests, it does not add a ceiling to requests
that omit one.

```yaml
config:
  max_tokens: 4096       # reject a request whose completion ceiling exceeds this
  max_messages: 50       # reject more than this many messages
  max_input_length: 0    # 0 = no total-character limit
```

### rate-limit

A token-bucket limiter over gateway traffic. Distinct from the per-IP HTTP
limiter (`RATE_LIMIT_RPS`). Every configured rate must be `> 0`; to turn the
plugin off, set `enabled: false`. The per-key and per-user limits key on the API
credential and `Request.User`, so they apply to authenticated requests.

```yaml
config:
  requests_per_second: 100   # global
  burst: 100                 # defaults to requests_per_second
  key_rpm: 60                # optional, per API key (requests/minute)
  user_rpm: 30               # optional, per user (requests/minute)
```

### budget

A per-API-key spend cap. It estimates each request's cost from the response's
token usage and the configured per-million-token prices, accumulates it against
the key, and rejects once the limit is reached. Keys on the API credential, so
it applies to authenticated requests. List it at both stages with identical
config.

```yaml
config:
  store_id: default          # instances sharing a store_id share counters
  spend_limit_usd: 10.0       # per API key; 0 = unlimited
  input_per_m_tokens: 3.0
  output_per_m_tokens: 15.0
  cache_read_per_m_tokens: 0.0
  cache_write_per_m_tokens: 0.0
  max_keys: 10000             # keys tracked in memory
```

### response-cache

Returns a stored response for an identical repeated request instead of calling
the provider again. The cache key includes the API credential, so one key's
response is never served to another. List it at both stages with identical
config.

```yaml
config:
  max_age: 300        # seconds a cached entry is served
  max_entries: 1000
```

### request-logger

Records each request for the dashboard's Request Logs page. With `persist: true`
and a request-log store configured (`REQUEST_LOG_STORE_BACKEND` /
`REQUEST_LOG_STORE_DSN`), rows survive a restart; otherwise it logs to stdout.
List it at all three stages so a failed request still produces a terminal row.

```yaml
config:
  level: info
  persist: false
```

## Writing your own

1. Create `plugin/<name>/<name>.go` implementing `plugin.Plugin`.
2. Register a factory via `plugin.RegisterFactory("my-plugin", ...)` in `init()`.
3. Add a blank import in `cmd/ferrogw/main.go`.

To deny a request, set the rejection reason and return `nil`; return an error
only when the plugin itself failed. See the `plugin` package docs and
[Adding a New Plugin](../AGENTS.md#adding-a-new-plugin).

## Quick check

`scripts/plugin_smoke.sh` exercises every built-in plugin end-to-end against a
live provider and prints a PASS/FAIL summary.
