# Ferro AI Gateway — Web Console

The React operations dashboard the gateway serves at `/`. It is compiled into the
gateway binary with `go:embed` (`internal/webui`), so this directory produces an
artifact rather than a deployable of its own — there is no web image and no second
origin. The console is served from the same port as the API and signs in with a
gateway `MASTER_KEY` or any admin / read-only key.

<div align="center">
  <img src="../docs/dashboard.gif" alt="Ferro AI Gateway operations console: Overview, Analytics, Providers, Routing Strategies, Plugins, Playground, Tracing, Request Logs, Audit Trail, Configuration, and API Keys" width="100%" />
</div>

## What it shows

One console over the live gateway — no separate backend, no polling service. Every
page reads what the gateway actually reports.

| Page | Reads |
|------|-------|
| **Overview** | request volume, error rate, provider health, and readiness at a glance |
| **Analytics** | tokens, spend, latency percentiles, and per-model cost over a selectable range |
| **Tracing** | the OTLP export wiring for New Relic, LangSmith, or Jaeger |
| **Request Logs** | one row per request — provider, model, tokens, stage, trace ID — filterable by key, provider, model, and time |
| **Audit Trail** | every credential change, sign-in, and log purge the gateway recorded |
| **Providers** | connected providers first, then the rest of the catalog; the models each serves and, per provider, the chat parameters it accepts |
| **Routing Strategies** | the active strategy and each target's weight, retry, concurrency, and circuit-breaker state |
| **Plugins** | the guardrails and middleware this instance runs, with their configured settings |
| **Playground** | exercise chat, embeddings, and image routes through the real routing path |
| **Configuration** | the runtime config as JSON, with version history and rollback |
| **API Keys** | issue, rotate, and revoke scoped keys; list and revoke active dashboard sessions |

To see it filled with data like the recording above, bring up the self-contained
demo stack from the repository root — a mock upstream plus a load generator that
drives continuous traffic:

```bash
make up-fullstack   # then open http://localhost:8080
```

Development and testing happen entirely here: nothing in this directory needs the
Go module, and `go build` / `go test` need nothing from here.

## Development

Run the gateway on port 8080. From the repository root:

```bash
make run
```

In another terminal, start the web application:

```bash
cd web
make dev
```

Open <http://127.0.0.1:5173> and sign in with the gateway `MASTER_KEY`. Vite
proxies `/admin/*`, `/v1/*`, and `/readyz` to `http://127.0.0.1:8080`, so the
dev server behaves like the same-origin deployment. Use another gateway or
port when needed:

```bash
make dev GATEWAY_URL=http://127.0.0.1:9090 PORT=3000
```

Run `make help` for the complete list. Direct npm commands remain available.

## Validation

```bash
make check   # lint, unit tests, TypeScript, production build
make e2e     # Playwright, real Chromium
```

`make check` is what CI runs. `make e2e` is a local tool only — run it before
sending a change that touches rendering, because no pipeline will run it for
you.

### The real-gateway journey

`make e2e` answers every gateway call from `e2e/fixture.ts`. That is what makes
it fast and deterministic, and it is also why it cannot see the Go API move: the
fixture keeps answering the old way, so a renamed field or a changed status
passes here and breaks the shipped dashboard.

One journey covers that, against a gateway this repository builds and starts:

```bash
FERROGW_E2E_GATEWAY=1 npm run test:e2e
```

It replaces the mock run rather than adding to it — `e2e/gateway.spec.ts` is the
only spec that executes — and it signs in with a sentinel master key, reads the
Overview and Providers pages, and creates an API key, asserting each against
what the gateway actually reports. Memory stores, one target pointed at a
discard port, no remote catalog fetch: it needs no network access and no
provider credential, and nothing it starts outlives the run.

It needs the Go toolchain, which is the reason it is opt-in: everything else
here builds and tests with no Go at all. Neither suite runs in CI.

## Shipping

`make build` here writes `dist/`. `make build` at the **repository root** does
that and then copies the output into `internal/webui/dist` before compiling the
gateway, which is the only step that puts the dashboard in a shipped binary.
The image and the release pipeline do the same copy their own way.

## Constraints worth knowing

**Content-Security-Policy.** The gateway serves the policy for this page from
`internal/middleware/securityheaders.go`. No inline scripts, and no runtime CDN
dependencies. Inline styles are blocked with one exception: `style-src` allows a
single stylesheet by SHA-256 digest, which the component library injects to hide
a scrollbar inside an open dropdown. `src/lib/csp.test.ts` recomputes that digest
from the installed library and compares it against the Go constant, so a
dependency upgrade fails there rather than reintroducing a console violation
nobody reads. Adding a second exception is a deliberate decision:
`'unsafe-inline'` would disable every digest in the same directive and permit any
inline style on a page that manages credentials.

**Routing visibility in the Playground applies to non-streamed answers.** The
"Served by" badge names the target that actually answered, which under fallback
or load-balance routing is the one thing the chosen model cannot tell you. It is
read from `provider` on the JSON response, and a streamed answer has no
equivalent: the SSE frames are the OpenAI wire format every client of this
gateway reads, and a gateway-specific field does not belong in them for the sake
of one dashboard panel. Streaming is on by default, so the panel says so beside
the toggle rather than leaving the badge silently absent. Turn streaming off to
see it, per turn — consecutive turns can be served by different targets, and
each keeps its own.

**Runtime configuration.** The application reads `config.json` before rendering:

```json
{ "gatewayBaseUrl": "" }
```

An empty value means same-origin, which is what the embedded deployment always
is. The field remains for a raw static deployment served from somewhere else; a
non-empty value must be an HTTP or HTTPS origin with no credentials, path, query
string, or fragment.

**Separate-origin deployments need two things configured, and neither of them is
`gatewayBaseUrl`.** That field only tells the application where to send its
requests.

- The gateway's `CORS_ORIGINS` must list **the origin the dashboard is served
  from** — not the gateway's own origin. `CORS_ORIGINS` is matched literally
  against the browser's `Origin` header, which names the page making the
  request. Listing the gateway origin there matches nothing, and every call
  fails with no `Access-Control-Allow-Origin` on the response.
- If the static host serving the dashboard sends its own
  `Content-Security-Policy`, that policy's `connect-src` must include the
  gateway origin. Otherwise the browser blocks the request before it is sent,
  whatever the gateway allows.

**Subpath hosting.** For a static deployment under a subpath, build with
`VITE_BASE_PATH` and mount `dist` at the same path with an `index.html`
fallback:

```bash
VITE_BASE_PATH=/console/ npm run build
```
