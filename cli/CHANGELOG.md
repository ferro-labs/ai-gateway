# Changelog

All notable changes to **ferro-cli**, the operator console for the Ferro Labs
AI Gateway. The project is `ferro-cli`; the binary it installs is `ferro`, which
is what the command examples below invoke. This file covers the CLI only: it
lives in the same repository as the gateway but versions and releases
separately, tagged `cli/vX.Y.Z`.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.1.0 — unreleased

First release. ferro-cli ships a single binary, `ferro`, carrying two surfaces
over one HTTP client: scriptable commands and a full-screen operations console.
Requires ai-gateway ≥ v1.4.

### Added

- **Scriptable commands**, all honouring `--format table|json|yaml`: `status`,
  `models`, `providers`, `mcp`, `plugins`, `services`, `sessions`, `audit`,
  `keys list|get|create|rotate|revoke`, `logs list|stats|tail`, `chat`, and
  `version`.
- **Full-screen console** on bare `ferro` at a TTY: a framed layout with a
  connection header, a providers/services left rail, target and traffic
  summaries, a bounded command transcript, and a command composer with
  completion, ghost hints, history, and reverse search (`ctrl+r`). Screens:
  Home, Request logs, Keys, Playground.
- **Request-log tail** — a since-cursor follower with deduplication and
  exponential backoff, in both `logs tail` and the console's logs screen, with
  filters for time, model, provider, stage, and credential.
- **Key management** with derived state (`active` / `expired` / `revoked`) from
  `revoked_at`, `expires_at`, and `active`; a create wizard and
  typed-confirmation rotate and revoke in the console.
- **Playground** — streaming chat with batched rendering, `/model` and
  `/clear`, and a metadata line carrying usage plus route and cost when the
  gateway can supply them.
- **Connection profiles** at `os.UserConfigDir()/ferro/config.yaml`, holding
  connection details only. Resolution precedence — URL: `--gateway-url` >
  `FERRO_URL` > `profile.url` > `http://localhost:8080`; key: `FERRO_API_KEY` >
  `profile.api_key_env` deref > `MASTER_KEY`. Credentials never enter process
  arguments or shell history through a flag.
- **Persistent flags** `--gateway-url`, `--profile`, `--format`, `--ascii`;
  `NO_COLOR` support alongside TTY and `TERM=dumb` detection.

### Fixed

- Request-log tails now drain every bounded page before advancing their cursor,
  preventing bursts larger than one page from being skipped.
- Silent chat connections now end with a structured stream-timeout error, and
  interrupted JSON or YAML chats emit their partial document before exiting.
- Playground action notes are excluded from model history, prompt history is
  retained only for recognized commands, and long sessions remain bounded.
- Key actions entered on a newly opened console wait for the current key list,
  so names and selections resolve against fresh gateway state.
- Stale profile selections now fail with a clear error instead of silently
  falling back to the local gateway URL.

### Contracts

- stdout is the machine channel; narrative, warnings, and errors go to stderr,
  so `--format json` output is always safe to pipe.
- Exit codes are the API: `status` exits 1 only when the gateway is unreachable;
  a reachable-but-degraded gateway exits 0 and prints its degraded state.
- Redirects are surfaced, never followed; a non-2xx response is always a
  failure; an empty 2xx body where a payload was expected is an error; 503 is
  decoded rather than errored only where degraded-state bodies are the contract.
- Secrets from `keys create` and `keys rotate` are shown exactly once, on
  stdout, and never enter the console transcript or command history.
- Absent measurements render `-`, never `0`. Endpoints the gateway does not
  serve (404 / 501) disable a panel with a hint instead of ending the command.
- The console holds no durable state; everything re-derives from the API.

### Notes

- The console's gateway-version slot renders `—`: the gateway does not serve its
  version over HTTP.
- The traffic panel and streaming route attribution are derived from the request
  log, and render as unavailable when no request-log store is configured.
- Not in this release: the config viewer / history / rollback screen, the
  provider capability matrix, the doctor checklist, and the init wizard.
