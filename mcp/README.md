# MCP tool servers

The gateway can connect to [Model Context Protocol](https://modelcontextprotocol.io)
servers and give the LLM their tools. When any are configured, the gateway
**injects each server's tools into every chat completion**, and when the model
answers with `tool_calls`, it **runs the agentic loop**: call the tool over MCP,
feed the result back, and let the model continue — up to a bounded depth — until
it produces a final answer. To the caller it's a normal `/v1/chat/completions`
request; the tool round-trips happen inside the gateway.

This package holds the MCP client, transports, registry, and the tool-calling
executor. Servers are declared under `mcp_servers:` in the gateway config; see
[`../config.example.yaml`](../config.example.yaml) for the annotated reference.

## Two transports

A server is reached one of two ways, chosen by which field you set:

| Transport | Set | The gateway… |
|---|---|---|
| **stdio** | `command` (+ `args`, `env`) | launches the server as a subprocess and speaks over its stdin/stdout, for the gateway's lifetime |
| **Streamable HTTP** | `url` (+ `headers`) | connects to a running HTTP MCP endpoint |

```yaml
mcp_servers:
  # stdio: launched as a subprocess
  - name: filesystem
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
    timeout_seconds: 30

  # Streamable HTTP: the gateway connects to it
  - name: search
    url: https://mcp.example.com/mcp
    headers:
      Authorization: "Bearer ${SEARCH_TOKEN}"
    allowed_tools: ["web_search"]
```

## Configuration

| Field | Applies to | Meaning |
|---|---|---|
| `name` | both | unique identifier for the server |
| `url` | HTTP | Streamable HTTP endpoint |
| `headers` | HTTP | extra request headers; values support `${VAR}` |
| `command` / `args` | stdio | executable + arguments to launch |
| `env` | stdio | environment for the subprocess (see the trust boundary below); values support `${VAR}` |
| `allowed_tools` | both | restrict which of the server's tools are exposed; empty = all |
| `max_call_depth` | both | bound on the agentic loop's depth (default 5; the minimum positive value across servers applies) |
| `timeout_seconds` | both | per-tool-call timeout (default 30) |
| `required` | both | make this server a condition of readiness (see below; default false) |

`${VAR}` references (in `headers` and `env`) use the braced form only — a bare
`$` is literal, and an undefined variable is an error rather than a blank secret.
They resolve when the MCP client is constructed, so the config never carries a
materialised secret into the config-history store or `GET /admin/config`.

## The subprocess trust boundary (stdio)

A stdio server **does not inherit the gateway's environment**. It gets a minimal
base — `PATH`, `HOME`, `LANG`, `TMPDIR` — plus exactly the keys under `env`. So a
gateway credential like `OPENAI_API_KEY` or `MASTER_KEY` never reaches an MCP
server implicitly. `env` is the trust boundary: anything a server needs — a token,
or `HTTPS_PROXY` / `NODE_PATH` / `SSL_CERT_FILE` — must be listed there
deliberately.

## Readiness (`required`)

Every server's state appears under `mcp_servers` in the `GET /readyz` body, so MCP
health is observable without gating on it. `required: true` makes a server's
availability a condition of readiness: when it isn't ready, `/readyz` answers
`503` and an orchestrator takes the instance out of rotation.

"Unready" means the initialize handshake never completed (including a server whose
transport could not be built). Death *after* a successful handshake is detected
for **stdio** servers only — an HTTP server that goes unreachable keeps reporting
ready — so don't rely on `required` to withdraw an instance when an HTTP server
drops. Turn it on only for a server the deployment genuinely cannot serve without:
a required server that is down stops **all** traffic through the instance,
including requests that use no tools.

## Quick check

`scripts/mcp_smoke.sh` verifies the tool-calling loop end-to-end against real MCP
servers (stdio and Streamable HTTP) and a live model, and prints a PASS/FAIL
summary.
