# Configuration

The gateway is configured by a YAML or JSON file (format auto-detected) named by
the `GATEWAY_CONFIG` environment variable. **A config file is read only when
that variable names it** — nothing is discovered, so a stray file on a host can
never change how a gateway routes. Decoding is strict: an unknown key is
rejected at load, by `ferrogw validate`, at startup, and on `PUT /admin/config`
alike.

This package holds the schema (`config.go`) and the loader/validator
(`load.go`). The full annotated reference — every option, with inline comments —
is [`../config.example.yaml`](../config.example.yaml) /
[`../config.example.json`](../config.example.json).

```yaml
strategy:
  mode: fallback            # how targets are ordered — see the strategies README

targets:                    # the providers this gateway may route to (an allowlist)
  - virtual_key: openai
    retry: { attempts: 3 }  # per target, applies under every routing mode
  - virtual_key: anthropic
  - virtual_key: gemini
    models: [gemini-2.5-flash]   # models declared by the operator — additive only

aliases:
  fast: gpt-4o-mini         # resolved before routing

plugins:                    # guardrails and middleware — see the plugin README
  - name: word-filter
    type: guardrail
    stage: before_request
    enabled: true
    config: { blocked_words: ["password"] }
```

## Section map

| Section | What it does | Reference |
|---|---|---|
| `strategy` | Target *ordering* — 8 modes in two families (pool vs named) | [`../internal/strategies/README.md`](../internal/strategies/README.md) |
| `targets[]` | The provider allowlist; per-target `retry`, `concurrency`, `circuit_breaker`, `weight`, declared `models`, and `model_map` | [`../config.example.yaml`](../config.example.yaml) |
| `aliases` | Model-name aliases, resolved before routing | — |
| `plugins[]` | Guardrails, caching, rate limits, budgets, logging | [`../plugin/README.md`](../plugin/README.md) |
| `mcp_servers[]` | MCP tool servers (stdio + Streamable HTTP) | [`../mcp/README.md`](../mcp/README.md) |
| `observability` | OpenTelemetry tracing + exporter plugins | [`../observability/README.md`](../observability/README.md) |
| `compatibility` | `on_unsupported_param: warn \| drop \| reject` for parameters a provider cannot express | [`../providers/README.md`](../providers/README.md) |
| `request_timeout` | End-to-end bound on one non-streaming request, retries included | — |
| `batch_target` / `responses_target` | The single backend serving `/v1/files*` + `/v1/batches*`, and the Responses id sub-routes | [`../providers/README.md`](../providers/README.md) |

Validate any file without starting the server:

```bash
ferrogw validate            # every virtual_key, plugin name/stage, and strategy
                            # must resolve; credentials are deliberately NOT checked
```

## `${VAR}` environment references

Values in plugin `config`, MCP `headers`/`env`, and observability exporter
`config` may reference environment variables with the **braced form only**:
`${MY_TOKEN}`. References resolve **when the component is constructed**, never
at file load — so the config carries only the reference for its whole life, and
a secret is never written to the config-history store, never served by
`GET /admin/config`, and never restored by a rollback. A bare `$` is data
(`$100`, `pa$$w0rd` survive verbatim); an undefined variable is an error rather
than a silently blank secret.

## Declared models (`targets[].models`)

Names models a target serves that neither the model catalog nor live discovery
can see — an id newer than the catalog, a preview or regional name, a
self-hosted deployment behind `<PROVIDER>_BASE_URL`. Declared models join the
routing index and `/v1/models` alongside the automatic sources. The field is
**additive only**: declaring one model never hides the others a target serves.
Wildcards are rejected at load. The same id on two targets is how a model gets
a fallback.

## Cross-provider model mapping (`targets[].model_map`)

Maps the model key visible after global alias resolution to the exact model ID
sent to one target. The keys join both the routing index and `/v1/models`
inventory; `targets[].models` remains additive and can still declare other
models. Cost and pricing use the mapped upstream model, not the visible key.

For fallback, every target must contain the same visible key. The client below
always requests `smart`, while each provider receives its own model ID:

```yaml
strategy: { mode: fallback }
targets:
  - virtual_key: openai
    model_map: { smart: gpt-5 }
  - virtual_key: anthropic
    model_map: { smart: claude-sonnet-4-6 }
```

## Trusted proxies (`TRUSTED_PROXIES`)

By default the gateway trusts `X-Forwarded-For` / `X-Real-IP` only from loopback,
so per-IP rate limiting and request logs see the real client IP without being
spoofable. Behind a reverse proxy or load balancer, set the proxy's CIDR(s):

```bash
TRUSTED_PROXIES=10.0.0.1/32                 # single upstream proxy
TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12    # VPC / multiple ranges
```

| Deployment | Recommended value |
|---|---|
| Local dev (no proxy) | _(leave unset — loopback default)_ |
| Docker Compose with nginx | `172.16.0.0/12` or the bridge subnet |
| AWS ALB / GCP LB | your VPC CIDR (e.g. `10.0.0.0/8`) |
| Kubernetes cluster-internal | your pod/node CIDR |
| Cloudflare Tunnel | Cloudflare's published IP ranges |

Configure the proxy to **replace** `X-Forwarded-For`, not append: the gateway
resolves the client from the far end of the chain, and an appending proxy in
front of an untrusted hop lets a caller forge its address. A request from an IP
outside the trusted list has its forwarded headers ignored entirely.

## Live changes

`PUT`/`POST /admin/config` applies a config over the API with the same strict
decoding and validation as a file; history is recorded (with the acting
credential) and `POST /admin/config/rollback` restores a prior version. Free-form
secret-bearing values are withheld from `GET /admin/config` — see
[SECURITY.md](../SECURITY.md).
