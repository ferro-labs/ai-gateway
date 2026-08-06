# Fullstack observability stack

The gateway wired to a complete monitoring stack — **Postgres, Jaeger,
Prometheus, and Grafana** — in one command. It runs two ways from the same base:

| Mode | Command | What it does |
|------|---------|--------------|
| **Mock demo** | `make up-fullstack` | adds an in-cluster mock upstream + a load generator, so every dashboard panel fills with no real key and nothing billed |
| **Live** | `make up-fullstack-live` | the same stack against **real providers**, using keys from a repository-root `.env` |

Both run from the repository root. Tear either down with `make down-fullstack`.

Once up: Gateway `:8080` · Grafana `:3000` (`admin`/`admin`) · Prometheus `:9090` · Jaeger `:16686`.
Open Grafana → **Ferro AI Gateway / Overview**.

## Files

| Path | Role |
|------|------|
| [`../compose.fullstack.yaml`](../compose.fullstack.yaml) | **Base** — the production-shaped stack (real providers). Every image tag, port, and credential is env-overridable. |
| [`../compose.fullstack.mock.yaml`](../compose.fullstack.mock.yaml) | **Mock overlay** — adds the mock upstream + load generator and points every OpenAI-compatible provider at the mock. |
| `gateway.yaml` | Gateway config used **in mock mode** — 16 load-balanced providers, guardrails, circuit breakers, request logging, and tracing to Jaeger. |
| `prometheus/prometheus.yml` | Scrape config for the gateway's `/metrics` (bearer-authenticated). |
| `grafana/provisioning/` | Datasources (Prometheus + Jaeger) and the dashboard provider, loaded on start. |
| `grafana/dashboards/ferro-ai-gateway.json` | The dashboard — request rate, latency percentiles, per-provider breakdown, token cost, and circuit-breaker state. |

The **mock upstream** and **load generator** are reusable tools and live in
[`../../scripts`](../../scripts) — [`scripts/mockllm`](../../scripts/mockllm) and
[`scripts/loadgen.sh`](../../scripts/loadgen.sh) — so they can be run against any
gateway, not just this compose. The overlay simply wires them in.

## Configuration

Nothing here needs editing for a deployment — override via a repository-root
`.env`:

```dotenv
# Ports
GATEWAY_PORT=8080
GRAFANA_PORT=3000
PROMETHEUS_PORT=9090
JAEGER_UI_PORT=16686

# Credentials (demo defaults shown)
MASTER_KEY=fgw_fullstack_demo_master_key
GRAFANA_ADMIN_USER=admin
GRAFANA_ADMIN_PASSWORD=admin
POSTGRES_PASSWORD=ferrogw
# DATABASE_URL=postgres://ferrogw:<pw>@postgres:5432/ferrogw?sslmode=disable

# Pinned image tags (override to upgrade)
JAEGER_IMAGE=jaegertracing/all-in-one:1.64.0
PROMETHEUS_IMAGE=prom/prometheus:v3.13.2
GRAFANA_IMAGE=grafana/grafana:13.1.2

# Live mode — the providers you use
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...

# Mock tuning (mock mode only)
MOCK_ERROR_PCT=3
MOCK_RATE_LIMIT_PCT=2
MOCK_LATENCY_MIN_MS=40
MOCK_LATENCY_MAX_MS=200
```

Two coupling notes:

- **Prometheus** scrapes `/metrics` with `MASTER_KEY` as the bearer, and
  `prometheus/prometheus.yml` holds that value literally (Prometheus does not
  interpolate env in its config). If you change `MASTER_KEY`, update that file
  too — or, for production, mint a dedicated `read_only` key with
  `POST /admin/keys` and use it there.
- **Live mode** derives targets from whichever provider keys are set. To run
  your own routing/plugins instead, mount a config and point `GATEWAY_CONFIG` at
  it (the mock overlay does exactly this with `gateway.yaml`).
