# Observability

The gateway emits an OpenTelemetry trace for every request it routes. This
package is the seam between the gateway and whatever records those traces: it
defines the stable `Provider`, `Span`, `Exporter` and `Event` contracts and the
`gen_ai.*` / `ferro.*` attribute names, and ships a zero-allocation no-op default
so tracing costs nothing until it is turned on.

Point it at any OTLP collector — Jaeger, New Relic, LangSmith, Grafana Tempo,
Honeycomb, Datadog — over gRPC or HTTP. The wiring (OTLP exporter, W3C
`traceparent` propagation, sampler) lives in [`../internal/otel`](../internal/otel);
this package is the public contract that wiring implements. For the full config
reference with inline comments see [`../config.example.yaml`](../config.example.yaml).

<p align="center">
  <img src="../docs/observability/jaeger-trace.gif" alt="Jaeger trace: one gateway.request span expanding to show its gen_ai.* and ferro.* attributes" width="100%" />
  <br/>
  <em>A single <code>gateway.request</code> trace in Jaeger — the plugin stages, the upstream provider call, and the span opened to show its <code>gen_ai.*</code> / <code>ferro.*</code> attributes.</em>
</p>

## Turn it on

Set one environment variable:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
ferrogw serve
```

…or add an `observability.tracing` block to `config.yaml`:

```yaml
observability:
  tracing:
    enabled: true
    endpoint: localhost:4317   # URL or host:port; blank reads OTEL_EXPORTER_OTLP_*
    protocol: grpc             # grpc | http/protobuf
    service_name: ferrogw
    sample_ratio: 1.0          # head sampler (0.0–1.0), wrapped in ParentBased
    privacy_level: metadata    # none | metadata | full
    shutdown_grace: 10s        # per shutdown stage
```

Either the config `endpoint` or an `OTEL_EXPORTER_OTLP_*` variable switches
tracing on; the environment variable wins when both are set.

## Endpoint and transport

The `endpoint` is a **base** URL, treated the way the OTLP specification treats
`OTEL_EXPORTER_OTLP_ENDPOINT`:

- Under `protocol: http/protobuf`, the traces signal path `v1/traces` is appended
  for you — so `https://host` becomes `https://host/v1/traces`, and a base that
  already ends in `v1/traces` is used as written rather than doubled.
- Under `protocol: grpc`, only the scheme and host are used.
- The **scheme selects transport security**: `https://` is TLS, `http://` and a
  bare `host:port` (e.g. `localhost:4317`) are plaintext. Managed backends need
  the `https://` form.

An endpoint the exporter cannot understand is rejected at startup rather than
failing silently per batch. The startup log prints the exact URL spans are posted
to.

## Managed backends

`headers` carries per-backend authentication. Use `${ENV_VAR}` references for
secrets — only the reference is stored in the config and returned by the admin
config API; the value is resolved from the environment when the exporter is built
and is never persisted.

| Backend | Endpoint | Protocol | Header(s) |
|---|---|---|---|
| **New Relic** | `https://otlp.nr-data.net` (EU: `otlp.eu01.nr-data.net`) | `http/protobuf` | `api-key: <ingest license key>` |
| **LangSmith** | `https://api.smith.langchain.com/otel` | `http/protobuf` | `x-api-key`, `Langsmith-Project` |
| **Jaeger** (self-hosted) | `localhost:4317` | `grpc` | none |

**New Relic** — use the account's **ingest license** key, not a User key:

```yaml
observability:
  tracing:
    enabled: true
    endpoint: https://otlp.nr-data.net
    protocol: http/protobuf
    headers:
      api-key: ${NEW_RELIC_LICENSE_KEY}
```

**LangSmith** — the traces path is appended to the `/otel` base automatically:

```yaml
observability:
  tracing:
    enabled: true
    endpoint: https://api.smith.langchain.com/otel
    protocol: http/protobuf
    headers:
      x-api-key: ${LANGSMITH_API_KEY}
      Langsmith-Project: ferrogw
```

The standard `OTEL_EXPORTER_OTLP_HEADERS` environment variable also applies.

## Try it locally with Jaeger

```bash
docker run -d --name jaeger -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one:latest
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 ferrogw serve
# send a request, then open http://localhost:16686 and search for service "ferrogw"
```

Each request shows up as a `gateway.request` span with its plugin stages and the
upstream provider call as children (see the trace above). Open the span and it
carries the full `gen_ai.*` semantic conventions plus the `ferro.*` extensions —
model, provider, token usage, routing decision, and per-request cost.

For the full stack — the gateway wired to Prometheus, Grafana, and Jaeger with
generated traffic in one command — see [`../deploy/compose.fullstack.yaml`](../deploy/compose.fullstack.yaml)
(`make up-fullstack`).

## What gets emitted

Every routed request produces a `gateway.request` root span (`SERVER` kind) and a
`CLIENT` child span per outbound provider call, which carries `traceparent`
upstream.

- **GenAI semantic conventions**: `gen_ai.system`, `gen_ai.operation.name`,
  `gen_ai.request.model`, `gen_ai.response.model`,
  `gen_ai.usage.{input,output}_tokens`.
- **`ferro.*` extensions**: `ferro.cost.*` (per-request USD cost),
  `ferro.routing.{strategy,target_key}`, `ferro.stream.time_to_{first,last}_token_ms`,
  `ferro.plugin.*`, `ferro.mcp.*`, `ferro.gateway.trace_id`.
- **Unified trace ID**: the OTel `trace_id`, the `X-Request-ID` response header,
  and the `trace_id` on every log line are equal for a request served through the
  gateway's HTTP stack.
- **Sampling**: `sample_ratio` governs the spans the gateway starts. The sampler
  is `ParentBased`, so an inbound sampled `traceparent` is always followed — a
  ratio below 1.0 never splits a distributed trace.

## Privacy levels

`privacy_level` controls how error messages are recorded on spans. No prompt or
response content is exported at any level.

| Level | Error recording on spans |
|---|---|
| `none` | Only the static string `redacted` — no message or type |
| `metadata` (default) | Message redacted (email / JWT / AWS keys tokenised) before recording |
| `full` | Raw error text, for trusted self-hosted debugging |

## Exporter plugins

`observability.exporters` wires plugin exporters that receive
`gateway.request.completed` and `gateway.request.failed` events, independently of
whether an OTLP endpoint is set. Implement `observability.Exporter` and register a
factory with `observability.RegisterExporter` in `init()`. No built-in exporters
ship in this repo — they live in the `ai-gateway-plugins` repository. An
unrecognised or failing exporter is warned and skipped; the gateway still starts.

## Reference

- [`../config.example.yaml`](../config.example.yaml) — full config with comments
- [`../AGENTS.md`](../AGENTS.md) — environment variables and endpoint rules
- [`../internal/otel`](../internal/otel) — the OTLP pipeline that implements this contract
