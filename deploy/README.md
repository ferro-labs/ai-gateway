# Deployment files

Container and platform manifests. Everything here is run from the **repository
root**, not from this directory.

| File | Role |
|------|------|
| `Dockerfile` | The gateway image. `--target gateway` is the only target; a node stage inside it builds the dashboard bundle the Go stage embeds. |
| `gateway.release.Dockerfile` | Packages a binary GoReleaser has already cross-compiled. Release pipeline only. |
| `compose.yaml` | Base: image, port, and a commented stub for every provider env var. Never used alone. |
| `compose.dev.yaml` | Override: builds from source, debug logging, live config mount, host access for a local Ollama. |
| `compose.prod.yaml` | Override: pinned tag, restart policy, health check, resource limits, log rotation. |
| `compose.fullstack.yaml` | Standalone base: the gateway wired to a full observability stack (Postgres, Jaeger, Prometheus, Grafana) against **real providers**. Fully env-configurable. See below. |
| `compose.fullstack.mock.yaml` | Overlay that turns the stack into a self-contained demo — an in-cluster mock upstream + a load generator, no real keys. |
| `fullstack/` | Configs the stack mounts — gateway config, Prometheus scrape, Grafana provisioning + dashboard. See [`fullstack/README.md`](fullstack/README.md). |

## Mounting a config takes two lines, not one

The gateway reads a config file **only** when `GATEWAY_CONFIG` names one — it
discovers nothing, on purpose, so that a stray file on a host cannot change how
a gateway routes. A bind mount on its own is therefore ignored, and the gateway
falls back to defaults derived from whichever provider keys the environment
holds. An operator who narrowed `targets` for safety gets the opposite of what
they wrote.

Both overrides set `GATEWAY_CONFIG` beside their mount for that reason. In
`compose.yaml` the env line and the volume are both commented out: uncomment
them together or neither. (A config sitting unread at a conventional path is
also logged as a startup warning.)

```bash
make up            # gateway, built from source
make up-prod       # gateway, detached, from a published image
make down          # tears down either
make docker-build  # --target gateway
```

## One image, one process

The dashboard is compiled into the binary with `go:embed` and served from the
same port as the API, so a deployment is one container and one URL — there is
no separate dashboard image, no second origin, and nothing to keep in version
lockstep. Setting `CORS_ORIGINS` is only for *your* browser apps calling the
gateway from elsewhere.

`go:embed` cannot reference a path outside its own package directory, so the
bundle is copied into `internal/webui/dist/` before the Go build rather than
read from `web/dist`. Inside the image that copy is an inter-stage
`COPY --from`; on a workstation it is `make build`; for a release it is a
GoReleaser before-hook. `internal/webui/dist/index.html` is committed as a
placeholder so a fresh clone compiles with no Node installed — which is also
why every path that ships a binary has to overwrite it.

**`gateway` must stay the last stage.** Render's blueprint spec has
`dockerfilePath` and `dockerContext` but no build target, so it builds whatever
stage comes last — and `render.yaml` deploys the gateway. Adding a stage below
it would silently change what production runs.

**`gateway.release.Dockerfile` is deliberately not merged in.** GoReleaser
builds it against a temporary directory holding only the cross-compiled binary,
not against the repository, so as a stage in the shared file it would be one
nobody could build from the root.

## `.dockerignore` lives at the repository root

Docker reads it from the build context root — never from beside the Dockerfile,
and never from a subdirectory. One context therefore means one ignore file, so
the dashboard's exclusions are the `web/`-prefixed block in the root file rather
than a `web/.dockerignore`. Patterns are anchored at the root and a single `*`
does not cross `/`, so `.env` does **not** cover `web/.env` — that one is spelled
out, because Vite inlines every `VITE_`-prefixed value it finds into the
published bundle.

`internal/webui/dist/` is excluded there too: the bundle is produced by the node
stage, not sent from the context, and shipping a locally built copy would
invalidate the `internal/` layer on every frontend build.

`render.yaml` also stays at the root, because Render only reads it there.

## Fullstack observability stack

The gateway wired to a complete monitoring stack — Postgres, Jaeger, Prometheus,
and Grafana — in one command. A production-shaped **base**
(`compose.fullstack.yaml`) runs against real providers; a **mock overlay**
(`compose.fullstack.mock.yaml`) turns it into a self-contained demo. Full
reference: [`fullstack/README.md`](fullstack/README.md).

```bash
make up-fullstack        # demo: base + mock overlay (no real keys, nothing billed)
make up-fullstack-live   # base only: real providers from a repository-root .env
make down-fullstack      # tears it down and removes its volumes
```

| Service | Endpoint | Role |
|---------|----------|------|
| gateway | http://localhost:8080 | the gateway + embedded dashboard, built from source |
| grafana | http://localhost:3000 | dashboards, provisioned on start (`admin` / `admin`, anonymous viewing on) |
| prometheus | http://localhost:9090 | scrapes the gateway's `/metrics` |
| jaeger | http://localhost:16686 | traces, via OTLP from the gateway |
| postgres | internal | config, key, and request-log stores |
| mockllm | internal (demo only) | OpenAI-compatible mock upstream from [`scripts/mockllm`](../scripts/mockllm) — randomized latency, tokens, errors; scriptable scenarios for the strategy end-to-end suite |
| loadgen | internal (demo only) | continuous mixed traffic from [`scripts/loadgen.sh`](../scripts/loadgen.sh) |

Open Grafana → **Ferro AI Gateway / Overview**. Within a minute the panels show
request rate, latency percentiles, token throughput, per-model cost, provider
errors, circuit-breaker state, rate-limit rejections, and connection state.

<p align="center">
  <img src="../docs/observability/grafana-dashboard.gif" alt="Grafana dashboard: per-provider request rate, latency percentiles, token cost, and circuit-breaker state" width="100%" />
  <br/>
  <em>Grafana — filter by provider or model; every panel is built from the gateway's real Prometheus metrics.</em>
</p>

<p align="center">
  <img src="../docs/observability/jaeger-trace.gif" alt="Jaeger trace: one gateway.request span expanding to show its gen_ai.* and ferro.* attributes" width="100%" />
  <br/>
  <em>Jaeger — a single request's span breakdown (plugin stages plus the upstream provider call), opened to show its <code>gen_ai.*</code> and <code>ferro.*</code> attributes.</em>
</p>

**How it fits together.** In the demo overlay, every OpenAI-compatible provider
is pointed at the in-cluster mock via its `<PROVIDER>_BASE_URL`, so requests run
real end to end — plugins, circuit breaker, metrics, and traces all fire —
without calling a provider. Prometheus scrapes `/metrics` with `MASTER_KEY` as a
bearer (the endpoint requires a `read_only` or `admin` scope); for production,
mint a dedicated `read_only` key instead. The gateway exports traces to Jaeger
over OTLP.

The dashboard's PromQL is built against the gateway's real metric names
(`gateway_requests_total`, `gateway_request_duration_seconds`,
`gateway_request_cost_usd_total`, `gateway_circuit_breaker_state`, …). Every
credential in the compose file — `MASTER_KEY`, the Grafana and Postgres logins —
is a throwaway demo sentinel, safe to commit and not for reuse.


## Compose with PostgreSQL

A self-contained pairing that comes up from a clean checkout —
`OPENAI_API_KEY=sk-... docker compose up` — with all three stores on Postgres:

```yaml
services:
  ferrogw:
    image: ghcr.io/ferro-labs/ai-gateway:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      - OPENAI_API_KEY=${OPENAI_API_KEY}
      - CONFIG_STORE_BACKEND=postgres
      - CONFIG_STORE_DSN=postgresql://ferrogw:ferrogw@db:5432/ferrogw?sslmode=disable
      - API_KEY_STORE_BACKEND=postgres
      - API_KEY_STORE_DSN=postgresql://ferrogw:ferrogw@db:5432/ferrogw?sslmode=disable
      - REQUEST_LOG_STORE_BACKEND=postgres
      - REQUEST_LOG_STORE_DSN=postgresql://ferrogw:ferrogw@db:5432/ferrogw?sslmode=disable
    depends_on:
      db:
        # Not the `depends_on: [db]` short form: that waits for the container to
        # START, and postgres:16-alpine then runs initdb for several seconds. The
        # gateway pings its store once while constructing it and exits on
        # failure, so the short form loses the race on a first `up` every time.
        condition: service_healthy

  db:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: ferrogw
      POSTGRES_PASSWORD: ferrogw
      POSTGRES_DB: ferrogw
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ferrogw -d ferrogw"]
      interval: 2s
      timeout: 3s
      retries: 15
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

No config file is mounted, so the gateway derives its targets from the provider
credentials in the environment — which is what makes this run cold. To serve a
config instead, copy the tracked example (`cp config.example.yaml config.yaml`),
mount it, and set `GATEWAY_CONFIG` beside the mount (see
[Mounting a config takes two lines, not one](#mounting-a-config-takes-two-lines-not-one)
— and without the `cp`, Docker creates a *directory* named `config.yaml` and the
gateway exits):

```yaml
    environment:
      - GATEWAY_CONFIG=/etc/ferrogw/config.yaml
    volumes:
      - ./config.yaml:/etc/ferrogw/config.yaml:ro
```
