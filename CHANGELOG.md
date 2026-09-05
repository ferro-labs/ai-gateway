# Changelog

All notable changes to Ferro Labs AI Gateway are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.5.4] — 2026-09-05

### Added

- Request identity on every observability surface. A request can name the
  end user and session it belongs to — the OpenAI `user` field, the
  `X-User-ID` and `X-Session-ID` request headers, or the W3C `baggage`
  entries `user.id` / `session.id` — and embedders can set request metadata
  through `observability.ContextWithRequestIdentity`. The identity is
  recorded as `enduser.id`, `session.id` and `ferro.request.metadata.<key>`
  on the request span; as `User`, `SessionID` and `Metadata` on
  `observability.RequestAttrs`, `Event` and `RoutingAttempt`; and as
  `user_id` / `session_id` on request-log rows (`GET /admin/logs`, request-log
  schema migration 6). Previously `user` reached the provider but no span,
  event or log row, so nothing the gateway emitted could be grouped by user
  or conversation. A request that states no identity records none; nothing is
  inferred. Unknown JSON body fields are still accepted.
- `observability.tracing.attempt_spans` (default `false`) opens one `CLIENT`
  child span, `gateway.routing.attempt`, per routing-layer attempt — retries
  and failovers included — carrying `ferro.routing.target_key`,
  `ferro.routing.sequence` and `ferro.routing.outcome`. The provider's HTTP
  span nests beneath it on the unary surfaces; on a streamed request the
  attempt span ends when the stream starts, so the HTTP span is its sibling
  rather than its child. A trace still shows which targets were tried and in
  what order without an exporter opting into attempt events. Backends plug in
  through the optional `observability.AttemptSpanProvider` interface; the
  built-in OTLP provider implements it.
- The `/v1/*` pass-through and the fixed-target forwards (`/v1/responses`
  create and its id sub-routes, `/v1/files`, `/v1/batches`) forward the W3C
  `traceparent` (and `tracestate`) upstream, so a provider that records
  traces joins the gateway's trace. `observability.tracing.propagate_passthrough`
  (default `true`) governs both proxy paths; setting it to `false` stops the
  injection on both. The inbound `baggage`, `X-User-ID` and `X-Session-ID`
  headers address the gateway rather than the provider, and an inbound
  `traceparent`/`tracestate` belongs to a trace the provider is not part of;
  all five are stripped before forwarding on every pass-through and
  fixed-target surface, regardless of this setting. A provider therefore
  receives this gateway's trace context or none — never the caller's.

### Changed

- `observability.RoutingAttempt` and `observability.RequestAttrs` each gain a
  `Metadata` map and are therefore no longer comparable with `==`; compare
  with `reflect.DeepEqual`. Code that reads their fields is unaffected.

### Fixed

- Provider HTTP clients no longer propagate inbound baggage upstream. The
  tracing transport injects W3C trace context only, so the `user.id` and
  `session.id` entries a caller may send — and that this release reads as
  end-user identity — stay inside the gateway. Trace linkage is unchanged for
  unary and streaming calls.
- Browser clients on a configured allowed origin can send `X-User-ID`,
  `X-Session-ID` and `baggage`. The identity headers this release introduces
  were absent from the CORS allowlist, so a preflight blocked them.

## [1.5.3] — 2026-09-03

Security fixes. No API or configuration changes for deployments that declare
their MCP servers in the config file.

### Security

- Stdio MCP servers are pinned to the boot-time config file. A config sent
  through the admin API may keep or remove a stdio server but can no longer
  add one, change its `command`, `args` or `env`, or widen or clear its
  `allowed_tools` (narrowing stays allowed); the request fails with
  `stdio mcp servers can only be declared in the gateway config file`. A
  stdio server is a command the gateway process executes, so accepting one
  from the API turned an admin key into arbitrary command execution on the
  host. A persisted config that violates the rule is refused at start-up
  rather than adopted.
- `google.golang.org/grpc` 1.82.1 → 1.83.2, closing GHSA-vp52-pcj8-j9qc
  (heap exhaustion via HTTP/2 DATA-frame fragmentation). Reachable only when
  the OTLP gRPC trace exporter is enabled.
- Dashboard build dependencies: `fast-uri` → 3.1.7 (GHSA-fph4-wmhf-6fwf,
  GHSA-5jgf-p345-68v8) and `browserslist` → 4.28.8 (GHSA-73wf-gq98-2v4g)
  through `overrides` in `web/package.json`. Both are build-time only and
  never ship in the bundle.

## [1.5.2] — 2026-09-03

### Added

- Attribution response headers on every routed surface — chat, streaming chat
  (written before the first chunk), legacy completions, embeddings, images,
  rerank, moderations, transcriptions, translations and speech: `X-Gateway-Provider` (the
  serving target's canonical provider), `X-Gateway-Target` (its
  `targets[].virtual_key`), `X-Gateway-Model` (the upstream model sent to the
  provider, after `model_map`) and `X-Gateway-Attempts` (routing-layer
  attempts for the request, retries and failovers included). A failed request
  carries the last target attempted. Previously only `/v1/completions` and the
  pass-through proxy named a provider, and `/v1/chat/completions` named
  nothing. Embedders read the same values through
  `aigateway.WithRoutingAttribution`.
- The request span carries `ferro.routing.attempt`, the routing-layer attempt
  count when the walk ended (the same number as `X-Gateway-Attempts`), on
  every routed surface. The attribute had been declared as planned since
  `v1.1.0` and never emitted.
- The embedded dashboard's strategy panel shows each target's `model_map`,
  a rule's `target_keys` chain in order, and a `metadata` predicate by its
  field; the mode cards describe the reworked least-latency, cost-optimized
  and conditional behaviour.
- `strategy.failover_on_status_codes: [409]` adds upstream statuses of the
  operator's own to the failover-safe classes, for an upstream whose "try
  elsewhere" answer the built-in classes do not cover. Validated as HTTP
  statuses; `400`, `401`, `403`, `404` and `422` are refused, and the
  request's own cancellation or deadline still stops routing.
- Conditional routing gains four bounded predicates: `key: user` (the
  request's `user` field), `key: stream` and `key: has_tools` (`"true"` /
  `"false"`), and `key: metadata` with `field: <entry>`, which reads one entry
  of the new `X-Gateway-Metadata` request header — a JSON object of at most
  32 scalar values within 4 KiB, accepted on `/v1/chat/completions` and
  `/v1/completions`, never forwarded to a provider. No other request header
  is exposed to a rule; a malformed header is the caller's `400`.
- `target_keys: [a, b]` on `conditions[]` and `content_conditions[]` names an
  ordered target chain for a rule; `target_key` stays as the one-entry form.
  The walk tries the chain in order — a conditional or content-based rule
  advances on the same failover-safe failures a pool does — skips a member whose circuit is open or that is parked after a
  `429`, and never substitutes a target outside the chain. Exactly one of the
  two fields is set; entries must be declared targets and may not repeat.
- `strategy.sticky: { on: user, ttl: "1h" }` under `loadbalance` and `ab-test`
  pins each request to the same target — or A/B variant — for the same `user`
  field — on chat, embeddings and images, the surfaces whose requests carry
  one — so a conversation keeps its provider prompt cache and a multi-turn
  session does not flip variants. It is a stateless hash: no shared state,
  the same answer on every replica with the same config, a random draw for a
  request without `user`, and an optional `ttl` after which a pin may move.
  Refused under any other mode.
- `targets[].timeout` bounds one physical attempt against a target, inside
  `request_timeout`, which stays authoritative for the request as a whole. A
  unary attempt is bounded through its response; a streaming attempt only
  until the provider answers, since a stream that has begun cannot be
  replayed on another target. An attempt that times out is a failover-safe
  failure, so a hung primary no longer consumes the whole request budget
  before a pool mode moves on. Answered `504 gateway_timeout` when every
  target times out.
- A target that answers `429` is parked for its `Retry-After` — bounded to
  one minute, five seconds when the hint is absent or unusable — so the next
  request does not pay another `429` on it. Same-request retry already
  honoured the hint; cross-request memory is new. Only the offending target
  is parked, its circuit breaker is untouched, and when every target that
  serves a model is parked the request is still attempted, as it is when
  every circuit is open. The park is process-local, like breaker and latency
  state.
- `aigateway.WithCatalog(models.Catalog)`, an option to `aigateway.New`, hands
  the gateway the model catalog it prices and ranks with in place of the
  embedded or remote one, which is then neither loaded nor refreshed. A host
  with its own price list — per-tenant pricing, for instance — previously had
  no way to make cost-optimized routing or request cost read it.
  `gateway_catalog_loads_total` reports such a catalog as `source="supplied"`.

### Changed

- Pool modes also fail over on a provider's typed statement that the prompt
  exceeded its context window — the OpenAI-compatible
  (`code: context_length_exceeded`, or the documented message with no code),
  Anthropic (`prompt is too long`) and Gemini (`INVALID_ARGUMENT` token-count)
  envelopes — since a sibling's model may have a larger window. The provider
  error now preserves the envelope's `code` and `type`
  (`core.HTTPStatusError`), and `core.IsContextLengthError` is the one
  classifier; every other 4xx still stops at the current target.
- A `conditional` or `content-based` rule that names one target is now exact:
  when that target's circuit is open the request is answered `503` rather
  than served by a sibling the rule never named. `v1.5.1` borrowed a healthy
  sibling in that one case; a rule that wants a stand-in now says so with
  `target_keys`. On non-chat surfaces, where content rules cannot be
  evaluated, `content-based` routes to the first configured target that can serve
  the surface, alone, instead of walking the declared order.
- Configuration loading now refuses an `ab_variants[]` entry without a
  `label`: attribution keys on it, so an unlabelled variant was one nothing
  could report on. A `v1.5.1` config with an unlabelled variant no longer
  loads until one is added. A `single` strategy with more than one target
  now logs a warning at load naming the unused targets; it still loads, since
  an omitted `strategy` block defaults to `single`.
- Every routed surface ranks its targets through the one strategy
  implementation chat and streaming already used. Embeddings, images, rerank,
  moderation, transcription and speech previously ranked through a second copy
  that differed in six places: it drew load-balance starts from a different
  random source, kept unseen least-latency targets in declared order instead of
  profiling them in random order, priced cost-optimized candidates with a
  different formula (and with no usage at all on rerank, moderation and audio,
  so those surfaces ordered unpriced), placed `unpriced_strategy: allow`
  candidates as a leading block, and returned only the ranked candidates rather
  than the full order. All of that is gone; the same config and the same
  health now produce the same candidate order on every surface, and a target
  that cannot serve a surface is simply not a candidate there.
- `least-latency` keeps learning. Samples are keyed by target **and** upstream
  model, so two models mapped onto one target rank on their own numbers;
  they expire after five minutes, so a target nothing has measured recently
  is treated as unseen and profiled again rather than ranked on a p50 from
  before an incident; and one request in ten leads with a sampled non-leader,
  so the fastest target can no longer lock in while a sibling that recovered
  is never re-measured. A stream's sample is now its time to first chunk
  rather than its whole drain, so a model that answers at length no longer
  reads as a slow provider; a unary call's sample is unchanged. No new config.
- `cost-optimized` scores each candidate on the catalog's price for the model's
  mode — input **plus output** for chat, with the request's `max_tokens` /
  `max_completion_tokens` (or 256) as the output estimate; per token for
  embeddings; per image; per minute or character for audio — rather than on
  chat input price alone. A target that is cheap to read and expensive to
  write no longer wins a request with a large completion budget, and
  embedding, image and audio models that previously tied at zero now order by
  their real catalog rate. Equal-cost targets draw by `targets[].weight`
  (equally when none is set) instead of tying on declaration order.

## [1.5.1] — 2026-09-01

### Added

- `targets[].model_map` lets multiple providers serve one visible model key while
  translating it to each target's upstream model ID. Mapped keys participate in
  routing and `/v1/models`; pricing uses the mapped upstream model.
- Each physical provider call or local circuit-breaker/concurrency refusal can
  emit a separate `gateway.routing.attempt` observation, including retries and
  cross-target failovers. Attempt events are opt-in: an exporter receives them
  only when it implements `observability.RoutingAttemptExporter`, and a custom
  `observability.Provider` only when it implements
  `observability.RoutingAttemptRecordingProvider`. Every existing exporter keeps
  receiving exactly one event per request.
- A/B routing attempts and terminal events carry
  `ferro.routing.ab_variant_label`, preserving the initially drawn variant
  through retries and safe cross-target advancement.
- A keyless end-to-end suite for the routing strategies, `make
  test-e2e-strategies` (`scripts/strategy_e2e.sh`): three scriptable mock
  upstreams stand in for real providers, and every mode is exercised over the
  real binary and HTTP surfaces — failover classes, retries and `Retry-After`,
  breaker open/half-open/closed, weight and variant distributions, `model_map`
  on unary and stream, `/v1/models` and `/metrics` — with no credentials. The
  mock (`scripts/mockllm`) gained scriptable scenarios (`POST /_mock/scenario`)
  and SSE streaming; its demo behaviour is unchanged.
- One-command install and package-manager distribution: `get.ferrolabs.ai`,
  `ferrogw` on npm and PyPI, a Homebrew cask and Scoop manifest, and GoReleaser
  platform archives (#412). Landed on `main` during the v1.4.x line without an
  entry, as did the README quickstart and demo overhaul (#417).

### Changed

- Pool modes (`fallback`, `loadbalance`, `least-latency`, `cost-optimized`, and
  `ab-test`) advance to another target only after a failover-safe failure: a
  transport failure, an attempt that timed out waiting on the target, 408, 429,
  5xx, an open circuit, or target saturation. They previously advanced after
  any failure, so a target answering 400, 401, 404, or 422 was silently covered
  by a sibling; those responses now reach the client. The request's own
  cancellation or deadline still stops routing, as does any provider-call
  failure in `single`, `conditional`, and `content-based` modes.
- Responses name the routed model — the model the client asked for, after alias
  resolution — as their `model` on every surface, streamed chunks included,
  instead of the identifier the provider reported. Provider calls, pricing, and
  the `UpstreamModel` of a `gateway.routing.attempt` event still use the mapped
  upstream model.
- Configuration loading rejects duplicate target keys, an empty
  `targets[].virtual_key`, and duplicate JSON object keys instead of accepting
  an ambiguous routing configuration.
- The embedded model catalog is parsed once per process. Every gateway
  constructed without a reachable remote catalog — one per tenant in an
  embedding platform — previously decoded the 3 MB document again (~90 ms,
  ten times that under the race detector); it now receives its own copy of the
  parsed catalog in about 3 ms.
- Configuration validation rejects duplicate `ab_variants[].target_key` entries.

### Fixed

- Failures in `after_request` plugins are timed and counted as plugin failures
  and emit one failed terminal lifecycle event carrying the selected A/B
  variant, instead of ending without a duration sample or a terminal event.

- A stream whose upstream had already finished when the client hung up was
  recorded as a client cancellation about half the time. The metering loop
  chose between the finished stream and the cancelled request at random when
  both were ready; it now takes the finished stream first, so a completed and
  billed stream is always recorded as completed. Cancellation still decides
  once nothing is ready to forward.

## [1.5.0] — 2026-08-29

### Added — the gateway is importable

A new public `run` package exposes the `ferrogw` program to Go code. `run.Main()`
is what `cmd/ferrogw` now calls; `run.Run(ctx, opts...)` runs the same server
under a caller-owned context and returns startup and listen errors instead of
exiting the process, with context cancellation triggering the same graceful
shutdown as SIGTERM. A custom binary is a `main` that blank-imports its plugins
and calls `run.Main()` — the process lane. `httpgateway` (since 1.4.2) remains
the library lane for mounting gateway surfaces behind your own middleware.
Closes [#206](https://github.com/ferro-labs/ai-gateway/issues/206).

The server now binds its listener before it starts observing shutdown, so a
cancellation that arrives during startup can no longer leave a listener behind.
Existing `ferrogw` behaviour — commands, flags, exit codes — is unchanged.

## [1.4.5] — 2026-08-23

A security patch. The stdio MCP transport now applies the same 10 MiB bound as
the HTTP transport, and three dependency advisories are cleared. No breaking
changes to configuration or the API.

### Security — a stdio MCP server can no longer exhaust gateway memory

The two MCP transports disagreed about whether a server is trusted with gateway
memory. HTTP treats one as an untrusted-content boundary and caps a response at
10 MiB. stdio applied no bound at all: its JSON-RPC framing was read with an
unbounded `ReadString`, and the result was then converted through JSON twice, so
peak memory ran to roughly three times whatever the server sent. A local MCP
server is arbitrary local code — at least as untrusted as a remote one — so a
tool result of any size could drive the gateway out of memory, though the same
protection had always applied to HTTP responses.

stdio now applies the same 10 MiB bound, measured per JSON-RPC message rather
than per session, so an ordinary conversation of any length is unaffected. To
place that bound the gateway spawns the MCP subprocess itself and builds the
transport over its pipes, rather than asking the transport to spawn it; the
teardown ladder that came with the old arrangement — close stdin, grace period,
SIGTERM, SIGKILL, reap — runs unchanged, followed by the same process-group
sweep for descendants an `npx` or `uvx` launcher left behind.

**A previously working oversized tool result will now fail**, which is the
intended behaviour. It is terminal for that server rather than for the one call:
the framing cannot resume mid-message, so the transport closes, in-flight calls
unblock, and the registry withdraws the server and stops advertising its tools.
A server with a legitimate reason to return more than 10 MiB should page its
results.

### Security — three dependency advisories cleared

`govulncheck` now reports zero vulnerabilities in every category, where it
previously reported one imported and one required. None was reachable from
gateway code, and the third is not present in a shipped artifact; all three are
cleared rather than carried.

- `golang.org/x/text` moves to v0.39.0 — an infinite loop on invalid input
  (GO-2026-5970). Imported, never called.
- `golang.org/x/net` moves to v0.56.0 — a panic parsing an invalid SVCB or HTTPS
  DNS record (GO-2026-5942). Required, never imported.
- `github.com/moby/go-archive` moves to v0.3.0 — a crafted tar archive can write
  outside the extraction directory. It arrives through testcontainers and is
  reached only by the integration suite, so it has never been part of the
  gateway binary.

The Go toolchain stays at 1.25.13, which has no outstanding standard-library
advisory, and `npm audit` on the dashboard toolchain reports none either.

### Documentation

- `SECURITY.md` named 1.1.x as the supported series, three minor versions out of
  date. It names 1.4.x.

## [1.4.4] — 2026-08-18

### Fixed — in-flight requests keep the provider price selected for them

An alias repointed to a different provider while a request was in flight could
price that request against the replacement provider, even though the original
provider served it. Routing now carries the canonical pricing identity captured
at provider selection through unary and streaming cost accounting. Attribution
is unchanged: responses, metrics, spans and plugin context still name the
routing alias.

## [1.4.3] — 2026-08-17

A security patch. Every dependency advisory reachable from gateway code is
cleared, and registration aliases are priced correctly on the two paths where
the alias reached the catalog instead of the vendor identity behind it. No
breaking changes.

### Security — the Go toolchain moves to 1.25.13

Six Go standard-library advisories are reachable from gateway code at the
pinned 1.25.12 toolchain, and every one is fixed in 1.25.13: quadratic
complexity in `net/url`'s `resolvePath`, JavaScript regexp context tracking in
`html/template`, unbounded post-handshake messages in `crypto/tls`,
`ReadHeaderTimeout` not applied on the unencrypted HTTP/2 check in `net/http`,
recursion depth during decode in `encoding/xml`, and maximum recursion depth in
`encoding/asn1`. They are reached through provider HTTP clients, the serving
path, the request-log store, the streaming reader, the signing proxy, Bedrock's
XML response stream, and Vertex AI's OAuth token exchange.

A seventh advisory — ASCII-only Punycode labels not rejected in
`golang.org/x/net/idna`, reached through Vertex AI's OAuth token exchange —
is **not** a standard-library issue and is not fixed by the toolchain. It lives
in an external module and is cleared by `golang.org/x/net` v0.55.0, which this
build already pins. It is listed here for completeness, not as a change in this
release.

No gateway code changes; the fix is the toolchain version.

### Security — the dashboard toolchain clears its open advisory

`nanoid` moves to 3.3.18, closing a high-severity advisory where a custom
generator can loop indefinitely when `size` is zero. It is a build-time
dependency and is not present in the dashboard bundle the binary embeds, so no
shipped artifact was affected. `npm audit` reports zero vulnerabilities.

### Fixed — registration aliases are priced correctly

`RegisterProviderAs`, added in 1.4.2, registers a provider under a routing
alias that differs from its canonical vendor identity. 1.4.2 corrected catalog
inventory and surface ranking to resolve the canonical identity; pricing still
keyed on the alias in two places, so an aliased target was treated as unpriced:

- **Cost-optimized routing.** The strategy built its price key from the routing
  alias, so an aliased target ranked as unpriced — dropped from routing entirely
  under `unpriced_strategy: skip`, and ordered wrongly otherwise. This affected
  every provider.
- **Streaming cost.** A streaming response carries no provider identity of its
  own, so its cost was computed against the routing alias — a key the catalog
  cannot price. The span cost, request-log cost and budget spend for a streamed
  request through an aliased target recorded as zero. This also affected every
  provider.

Both now resolve the canonical identity for the catalog lookup. A third path —
non-streaming cost recording — is corrected for completeness: it reads the
provider name off the response, and every built-in provider sets that field to
its own canonical name, so in practice built-in providers were already priced
correctly there. A provider implementation that leaves the field unset was not.

Attribution is unchanged. The routing alias, not the canonical name, still
identifies the target in metrics labels, in the span, and in the `Target` field
plugins read.

Deployments that register providers under their canonical name are unaffected.

## [1.4.2] — 2026-08-10

### Added — registration aliases preserve provider capabilities

`Gateway.RegisterProviderAs` registers one provider under a distinct routing target,
so deployments can bind multiple credentials for the same canonical provider. The
alias now resolves every optional provider capability through the original provider;
streaming, embeddings, images, rerank, moderation, audio, discovery, batch,
Responses, and generic pass-through no longer disappear behind an identity wrapper.

### Added — embedding HTTP surface facade

The public `httpgateway` package exposes the OSS-owned Files/Batches, Responses,
and generic pass-through handlers to embedding applications. Embedders can keep
their own authentication and tenant policy middleware while reusing the same
provider resolution, credential injection, traversal protection, governance, and
usage capture as the standalone server.

### Added — build provenance on the health endpoint

`GET /health` now reports the binary's `version`, `commit`, and `built` build
metadata alongside provider status, so an operator or embedding application can
confirm which build is serving traffic. Values come from `internal/version` and
default to `dev` / `none` / `unknown` for an unstamped local build.

### Changed — the configuration schema lives in the config package

v1.4.0 moved the configuration schema — `Config` and its sub-types — and the
loader and validator out of the root package and into the `config` package
(`github.com/ferro-labs/ai-gateway/config`). Two code comments still referred to
a root-package alias file that was never shipped; they now point at the `config`
package, which is the single home for these types.

For an embedder this remains a breaking change from the pre-v1.4.0 API, and the
migration is a one-line import: reference `config.Config`, `config.ValidateConfig`,
`config.ModeFallback` and the rest instead of the former `aigateway.*` names. The
gateway is still constructed the same way — `aigateway.New` takes a `config.Config`.

## [1.4.1] — 2026-08-07

A dependency-security patch for the web toolchain and the embedded dashboard.
No gateway code changes.

### Changed — the web toolchain clears every open dependency advisory

The dashboard's router moves to `react-router` 8.3.0 — the package that
absorbed `react-router-dom`, whose 7.x line advisory scanners flag on an
uncorrected affected range — together with React 19.2.8 (Router 8's peer
floor is React 19.2.7). The client routing API is unchanged and the rendered
browser suites pass unmodified.

Seven build- and tooling-time packages move onto their patched releases:
`undici`, `ip-address`, `fast-uri`, `postcss`, `@hono/node-server` (reached
through the component tooling's MCP SDK), `brace-expansion`, and `hono`. None
of them ships in the embedded bundle. `npm audit` reports zero
vulnerabilities.

## [1.4.0] — 2026-08-07

This release consolidates routing. Chat, streaming, embeddings and image
generation had each grown their own copy of target ordering, retry, circuit
breaking, error classification, metrics and request logging, and the copies had
drifted: retry applied under one routing mode, a rate limit opened a circuit on
one surface but not another, and a failure was reported as a gateway fault on
two of the four. They now share one path, so a behaviour is either true of every
surface or of none.

It also widens the API surface: rerank, moderations, speech-to-text,
text-to-speech, Files, Batches and the Responses API are now served natively
rather than depending on the generic pass-through — see the Added entries below.

Read the breaking changes below before upgrading. The one most likely to be
noticed is retry: `targets[].retry` was honoured only under `fallback`, and is
now honoured everywhere, so a target that always fails will make `attempts`
upstream calls under modes that previously made one.

### Added — rerank, moderations and audio are routed surfaces

Four surfaces join the natively served API, each carrying the full gateway
lifecycle — targets, routing strategy, plugins, circuit breaker, per-target
concurrency, metrics and request logging — where the pass-through carried none
of it:

- `POST /v1/rerank` (Cohere v2 contract): cohere, together, deepinfra,
  nvidia-nim, bedrock. `top_n` follows one contract on every provider — `0`
  caps to no results, a negative value is refused.
- `POST /v1/moderations` (OpenAI contract): openai, mistral.
- `POST /v1/audio/transcriptions` and `/v1/audio/translations` (multipart
  upload, 25 MiB cap): openai, azure-openai, groq, together, sambanova,
  deepinfra, mistral, fireworks.
- `POST /v1/audio/speech` (JSON in, binary audio out; `input` capped at 4096
  characters): openai, azure-openai, groq, together, deepinfra, mistral —
  mistral through a base64-in-JSON adapter for its native response shape.

### Added — Files and Batches pass through one configured backend

`/v1/files*` and `/v1/batches*` forward transparently to `batch_target`, a
configured target whose provider serves the OpenAI batch contract: openai,
azure-openai, groq, novita, qwen. These routes carry no model — a batch
references an uploaded file id, and the model lives per line inside the JSONL —
so one backend serves the whole flow and ids stay native, meaning a follow-up
call resolves with no gateway state. Uploads stream through outside the shared
body limit; the gateway credential replaces the client's. With no
`batch_target`, every route answers 501.

### Added — `/v1/responses` is governed and priced

The Responses API routes like chat — plugins, guardrails, circuit breaker,
concurrency, request log — and, unlike the generic pass-through, is priced: the
`usage` object is teed out of the response body or the terminal SSE event as it
streams through, unaltered, so catalog pricing, the request-log cost column and
the span cost all light up. The body is otherwise forwarded verbatim. The
stateful id sub-routes (retrieve, delete, cancel, input items) pin to
`responses_target` and answer 501 when it is unset.

### Added — a target declares the models it serves

`targets[].models` names models a target serves that neither the catalog nor
live discovery can see — an id newer than the catalog, a regional or preview
name, a self-hosted deployment. Declared models join the routing index and
`/v1/models` alongside the automatic sources. The field is additive only:
declaring one model never hides the others a target serves, and a wildcard is
rejected at load. The same id on two targets is how a model gets a fallback.

### Added — more providers serve more surfaces

Image generation on gemini (through `generateContent`, covering the
image-capable Gemini models), deepinfra and together; embeddings on
azure-foundry.

### Added — a Tracing page and a fullstack observability demo

The dashboard gains a Tracing page over the gateway's OpenTelemetry output, and
`deploy/` gains a compose stack that runs the gateway, a collector, a tracing
backend and a mock upstream together, so the whole pipeline can be seen working
without a provider key.

### Breaking — an agentic tool loop runs its guardrails on every turn

Only the first call of an MCP tool loop passed the `before_request` plugins. The
turns after it went straight to the provider, carrying tool **results** returned
by an external MCP server — content the caller never wrote and the operator has
least reason to trust — with no configured guardrail having read them.

Every turn now runs the plugins that bound a provider call. Two types are
deliberately excluded: a `transform` would rewrite the model mid-conversation,
and `logging`/`metrics` observe a request and would otherwise emit one row and
one sample per turn.

**What changes for a running deployment:** a guardrail can now reject a request
part-way through a loop, and a rate limiter sees each turn as the provider call
it is. A loop that previously completed may now be refused — which is the point,
but it is a behaviour change rather than a pure fix.

### Fixed — a budget can stop a tool loop that is overspending

The budget store is written once, after a request completes, so a per-turn check
against it read the same figure every time. A key at 99% of its cap got a whole
loop however many turns it ran and however large the context grew.

Each turn is now priced as it completes and the running total is carried into
the next turn's check, so the cap closes mid-request. Pricing per turn is also
more accurate under `loadbalance` and `least-latency`, where consecutive turns
can land on differently-priced providers.

### Fixed — a tool loop that fails part-way is still billed for what it spent

Usage accumulated across turns was discarded on the error path, so a loop that
failed on its third turn recorded nothing for the first two — which had spent
real tokens. A prompt that reliably failed late was free.

### Fixed — MCP subprocesses are reaped within the shutdown budget

`Close` returns immediately when requests still hold the registry and finishes
teardown in the background. That goroutine was untracked, so the gateway's
bounded drain had nothing to wait on and the process could exit with a stdio
subprocess still running. It is now waited on, inside the same budget.

### Fixed — the official SDKs' default embeddings call works again

`encoding_format: "base64"` was refused with a `400`. openai-python sets that
value **by default** — a bandwidth optimisation the caller never asked for and
mostly cannot see — so every default `client.embeddings.create(...)` failed
against every provider.

It is now accepted and served as `float`. That is not a substitution the caller
cannot use: openai-python base64-decodes only a value that arrives as a string,
so a float array passes through its parser untouched and reaches the caller as
the floats it wanted either way. What is lost is the bandwidth saving, which
this gateway could never have delivered — the embedding type is `[]float64` and
the response is JSON-encoded from it directly.

The value is resolved once at the entry point, so no provider receives a format
its response cannot hold. Any other value is still refused.

### Fixed — an admin command no longer reports success on an empty response

A `2xx` carrying no body skipped the decode and returned no error, so
`ferrogw admin keys create` printed `null` and exited `0` — which a script reads
as a key it never received. A response with no payload is now a failure when one
was expected. Commands that ask for nothing back are unaffected:
`keys revoke` answers `204` by design and still succeeds.


### Breaking — an existing Render deployment may refuse to start

`render.yaml` now sets `GATEWAY_ENV=production`, which turns on the production
startup checks. Those checks **refuse to boot** on two settings:
`ALLOW_UNAUTHENTICATED_PROXY=true`, and a `CORS_ORIGINS` list containing `*`.

A Render service carrying either has been starting until now and will stop on
its next deploy. Remove the setting, or drop `GATEWAY_ENV` from the blueprint if
the deployment is not production.

### Breaking — outbound requests surface a redirect instead of following it

The gateway's own HTTP clients followed upstream redirects, which carried the
credential injected for the original host to whatever host the redirect named.
A 3xx is now returned to the caller as the upstream sent it.

This affects a provider or proxy that answers with a redirect on a normal path —
the request fails where it previously succeeded, and the fix is to point the
provider's base URL at the location the redirect names.

### Fixed — an agentic tool loop bills every turn, not just the last

Each turn of an MCP tool loop is a separate provider call with a growing
context, but only the final turn's usage was read. Cost, the Prometheus token
counters, the request-log row, the OTel span and the per-key budget guardrail
all under-reported a multi-turn request — the budget by enough to overshoot a
cap several times over.

All six usage counters are now summed across the turns, and every one of those
five consumers reads the summed total.


### Breaking — withheld config values no longer round-trip through `GET /admin/config`

A free-form map the gateway hands to something else — `mcp_servers[].env` and
`.headers`, `observability.exporters[].config`, `observability.tracing.headers`,
and any plugin setting a plugin does not declare — withheld its values and
served its **keys** verbatim. A credential can be inlined in either position, so
`{"sk-…": 60}` came back intact to any caller holding a `read_only` key while
the string beside it was correctly `[REDACTED]` — which is what made the response
look safe. Env and header *names* were disclosed the same way, though the
documented contract for those maps was to show nothing at all. A value that was
not a string was also copied through untouched.

Withholding now covers the whole entry. Each comes back as
`[REDACTED_KEY_<n>]`, indexed over the sorted original names so the response is
stable across calls; the number of withheld settings survives, none of their
names does.

**What breaks:** a withheld map cannot be edited from a `GET` body and sent
back, because the keys are no longer there. `PUT` refuses such a body rather
than writing the placeholder as a real setting name. Edit those maps in the
config file. A map whose keys are shown — `aliases`, and a plugin's declared
settings — round-trips exactly as before.

`${VAR}` references still appear in a withheld entry's value: they name a value
rather than carrying one, and the stored-literal warning keys off exactly that.


### Fixed — a stalled or trickling upstream can no longer park a pass-through request

A non-2xx pass-through body is buffered so it can be scanned for a credential
the gateway itself injected. That buffer was bounded in size but not in time, so
an upstream that answered and then trickled kept the read going one keepalive at
a time until 256 KiB had arrived — indefinitely, in practice.

The stream idle bound did not cover it. It is armed only after the scan
finishes, and a trickle returns from every read promptly, so an idle timer is
re-armed each time and never fires. A provider answering `429` on a streaming
endpoint while holding the connection open is exactly this shape, so it took no
hostile upstream to reach.

The scan now runs under its own time budget and the upstream is cancelled if it
elapses. Streaming success responses are unaffected — they were never buffered.


### Fixed — configuring an MCP server no longer disables the response cache

The gateway advertises its MCP tools by adding them to the request, and the
plugin context holds a pointer to that request. The response cache keys on
tools, correctly, and computes that key twice: once to look up before the
provider call and once to store after it. The tools arrived between those two
points, so the two keys could never match.

The effect was total and silent. Any `mcp_servers` entry disabled response
caching for the whole deployment — no tool call involved — while the plugin
reported itself configured and simply never hit.

The tools now go onto a copy used for the provider call, so every plugin stage
observes the request the caller actually sent.


### Fixed — a dead target no longer black-holes its share of the traffic

Only `mode: fallback` moved a request past a target that failed. Every other
multi-target mode picked one candidate and stopped there, so an outage on that
target failed its whole selection share while a healthy sibling served the same
model. Under `cost-optimized`, which ranks deterministically, that was every
request.

`fallback`, `loadbalance`, `least-latency`, `cost-optimized` and `ab-test` now
advance to the next candidate. `single`, `conditional` and `content-based` do
not: those name one target on purpose, and answering from another would make the
rule a suggestion.

A circuit breaker was previously the only thing that moved traffic off a bad
target, and breakers are opt-in — the shipped examples configure one across
thirty targets. It is still worth configuring, for a different reason: without
one the walk pays the dead target's connection timeout on every request before
advancing. The breaker makes failover cheap; the routing mode is what makes it
happen.


### Breaking - pass-through paths cannot traverse outside the provider API root

The `/v1/*` pass-through previously forwarded dot segments without resolving
or rejecting them. An upstream that normalised the path could interpret
`/v1/../control` outside its configured AI API root after the gateway had
installed the operator's provider credential.

Traversal-shaped paths are now rejected before provider resolution with
`400 invalid_proxy_path`. This includes encoded, repeatedly encoded,
backslash-separated, and matrix-parameter forms. Ordinary escaped resource IDs,
literal percent characters, semicolon parameters, and query strings are
forwarded unchanged.

### Breaking — content a model cannot express is refused, not dropped

Five providers take text only. Four of them — Bedrock's Titan, Llama and Nova
families, and Replicate — accepted a request carrying an image, dropped it, and
answered `200` describing content the model never received. Three logged a
warning the caller never sees; Replicate logged nothing at all.

They now refuse it with a `400` naming the part they cannot carry, and the
upstream is not called. A caller sending vision content to those providers sees
a failure where it previously saw a plausible answer to a question that was
never asked.

### Breaking — single-prompt models receive the whole conversation, in one shape

Providers whose upstream takes one prompt string rather than a message list had
each invented their own way to flatten a conversation, and two of them lost
information doing it. AI21's Jurassic route overwrote one variable per message,
so only the last one was ever sent. Bedrock's Titan concatenated the contents
with no indication of who said what and no cue for the model to answer rather
than continue.

All of them now use one shape — a line per turn, `role: content`, ending with a
bare `assistant:` — so a conversation reads the same whichever of them serves
it. Every message reaches the model.

This changes the prompt those providers receive, including for a single-message
request: Titan and AI21 previously sent the bare content and now send it with
its role and the closing cue.

Bedrock's Llama family keeps the instruction template it was tuned on, and Nova
keeps its native message list and system channel. Neither is a single-prompt
API; both only needed the guard above.

### Breaking — container images are published as one multi-platform manifest

Images were built per architecture and stitched together afterwards, which is
why `:<version>-amd64` and `:<version>-arm64` existed alongside the plain tag.
They are no longer published. `:<version>` and `:latest` are unchanged in name
and are now multi-platform manifests directly.

The version tag is what carries the signature, and images now also carry an
SPDX SBOM as a build attestation, readable with `cosign download attestation`.
Verification instructions that named an architecture tag need updating to the
plain one.

### Added — a budget can price cached tokens

A spend cap billed cached input at the full input rate, and billed cache writes
— which are not part of the prompt count — at nothing. It now accepts optional
`cache_read_per_m_tokens` and `cache_write_per_m_tokens`.

Without them behaviour is unchanged: cached tokens keep billing at the input
rate, because an unconfigured rate must not silently bill as zero.

### Fixed — streaming Replicate requests report their token usage

They reported none, so they were billed nothing while the non-streaming path
reported correctly. Usage is read once after the stream completes and carried on
its final chunk. The read is best-effort — the content has already been
delivered — so a failure leaves usage absent rather than failing the request.

### Breaking — cached tokens are no longer billed twice

Providers disagree on whether the prompt token count already contains the
cached ones. The gateway priced the whole count at the input rate and then
added the cached portion again at its own rate, so a cached request on an
OpenAI-compatible provider paid for those tokens twice.

Both conventions now normalise where the response is decoded, and input is
priced on the non-cached remainder alone.

**Reported cost falls** for cached workloads on OpenAI-compatible providers — a
request of 10,000 prompt tokens of which 8,000 were cached drops from $0.035 to
$0.015 on `gpt-4o`. Dashboards and totals built on `cost_usd`,
`gateway_request_cost_usd_total` and `/admin/logs/stats` all step down.

**Anthropic-shaped providers now report a prompt token count that includes the
cached tokens**, as OpenAI-compatible providers already did. Their cost does not
change — it was already correct — but a client reading `usage.prompt_tokens`
sees a larger number, and `total_tokens` moves with it.

Where the catalog carries no cache rate for a model, the cached portion stays on
the input rate rather than becoming free.

### Breaking — an exhausted budget returns 402, not 429

A spend cap that had been reached returned `429` with `Retry-After: 1`,
inherited from the rate limiter. Waiting does not restore a budget, so clients
retried through their whole backoff and failed anyway. It now returns `402
Payment Required` with an `insufficient_quota` error, which the OpenAI SDKs do
not retry.

Rate limiting and concurrency backpressure still return `429` with a retry hint.
A client switching on `429` to detect budget denial must add `402`.

### Breaking — `/v1/completions` honours `echo`

A request setting `echo` received the completion alone. Each choice now returns
the prompt followed by the completion. The request sent upstream is unchanged
and usage still counts only generated tokens.

`best_of`, `logprobs` and `suffix` remain accepted and ignored. `suffix`
constrains what the model generates rather than what is returned — the text it
produces is the passage bridging prompt and suffix, and the suffix itself is
never part of it — so a chat call cannot honour it, and appending it would
return text the model never wrote.

### Breaking — a provider missing a required credential is refused

A provider built from a supplied configuration map rather than from the
environment was never checked for the credentials it declares as required, so
one could be constructed carrying none — while the documented contract said
otherwise. Twenty-eight of the thirty-four declared requirements were
unenforced. The check the environment path already applied now runs for both.

Deployments that inject credentials programmatically and were relying on a
provider constructing without one will now see it refused at construction.

### Breaking — Replicate is refused by the pass-through

Replicate's upstream is an asynchronous predictions API that cannot answer an
OpenAI-shaped request, so forwarding one only failed slowly. It is now refused,
which also removes `/v1/predictions` and `/v1/account` from reach under the
gateway's own Replicate token. Chat, streaming and image generation are
unaffected.

Hugging Face embeddings likewise refuse an unsupported `encoding_format` rather
than silently returning float vectors to a caller that asked for base64.

### Added — third-party licence notices ship with every release artifact

Release archives and container images carry the licences and notices of every
module linked into the binary, generated from the module graph at release time
rather than committed — so the inventory cannot drift from `go.mod` — and
placed at `/licenses` in the images.

### Added — image models priced by token report a cost

Image generation reported no token usage, so a model the catalog prices per
token rather than per tile was billed nothing and appeared free in the request
log, the usage statistics and the cost metric. Usage now travels from the
provider through to pricing. Per-tile pricing wins wherever the catalog has it,
and a response reporting no usage stays unpriced rather than being recorded as
costing nothing.

A configured budget begins accounting for these requests.

### Added — MCP failure reasons are readable by an operator

A server that will not start reported its reason only to the server log. It is
now served, redacted, on the authenticated admin health endpoint. The public
readiness endpoint still withholds it, because the reason can quote a server
URL, an authorization header or a subprocess command line.

The dashboard read its MCP list from the public endpoint, which omits that list
entirely when the gateway reports itself unready — so the panel went blank in
exactly the situation it exists for. It now reads the authenticated one.

### Fixed — a configuration reports every unknown key

A JSON configuration reported one unknown key per attempt, so three typos cost
three edit-and-restart cycles, while the same file written as YAML named all
three at once with line numbers. Both formats now report them together, and the
admin configuration endpoint — documented as rejecting exactly what validation
rejects — shares the same decoder.

### Fixed — Gemini enforces a requested JSON schema

A request asking for a response matching a JSON schema was translated to a plain
request-JSON instruction, so the model returned free-form JSON and the caller
was never told their schema had been dropped. The schema now travels. A schema
built from definitions and references still forwards those nodes unconstrained,
which the provider's schema dialect does not accept.

### Fixed — the pass-through no longer inserts a version segment

Pass-through requests inferred whether a configured base URL was an API root
from its last path segment, so a provider whose root carries a different path
had the gateway's version segment inserted mid-path. The inbound segment is now
removed and the configured root used as written.

A root carrying no path is no exception to that, which fixes Perplexity: its API
root is the bare host, so pass-through requests were sent to `/v1/responses` on
a host that mounts `/responses`. A provider configured with a *server* root
rather than an API root — Ollama, whose server mounts the OpenAI-compatible API
at `/v1` and the native one at `/api` — now resolves the difference itself and
reports the API root, so one rule covers both. Nothing reading only the URL
could tell them apart; both are a scheme and a host.

### Fixed — an unreadable pass-through body is refused only when it would have been read

The pass-through refuses a body no guardrail can inspect, but it counted every
guardrail — including ones that reach their verdict without reading request
content. A guardrail can now declare that it ignores request content, and the
answer is per instance: the token guardrail does read content once a length
limit is configured. A guardrail that declares nothing still triggers the
refusal, so one written elsewhere is never quietly reclassified.

### Fixed — credential lookups follow the outbound redirect policy

The AWS credential providers are built during configuration loading, before the
gateway's own HTTP client is installed, so they kept the SDK's client — which
follows redirects and carries the instance-metadata and single-sign-on tokens
across the hop. They now receive the gateway's client.

One deployment shape cannot have both: supplying a client makes the SDK refuse
to apply a custom certificate bundle. Those deployments keep the bundle and the
SDK's client, and the gateway says so at startup rather than choosing silently.

A refused redirect now names the scheme and host it pointed at, and whether that
was the same host — the difference between a path normalisation and an attempt
to move a credential elsewhere. Only the host is reported, so nothing carried in
a redirect's userinfo or query is echoed.

### Breaking — content guardrails apply to embeddings and image generation

A configured content guardrail refused blocked content on chat and forwarded
the same content on `/v1/embeddings` and `/v1/images/generations`, which
answered 200 and called the provider. Those two surfaces handed plugins a
request carrying only the model, so a guardrail had nothing to match on,
approved, and its approval was indistinguishable from one it had actually made.

Both surfaces now project their content for inspection, so a guardrail that
refuses a phrase on chat refuses it everywhere.

An embedding input the guardrail cannot read — a token-id array — is refused
when a content guardrail is configured. Token ids encode the same text the
policy covers, so forwarding them unread leaves a blocklist anyone can step
around by encoding client-side. A deployment with no content guardrail is
unaffected and continues to serve token-id input.

A message-count limit no longer applies to these surfaces: a batch of documents
is not a batch of conversation turns.

### Breaking — the `/v1/*` pass-through runs the gateway lifecycle

Endpoints served by pass-through — `/v1/responses`, `/v1/audio/*`, `/v1/files`,
`/v1/batches` and the rest — applied authentication, the target allowlist and
credential replacement, and nothing else. A guardrail that refused content on a
native route forwarded it here, upstream, under the gateway's own provider
credential; no request-log row was written and no cost was attributed.

They now run the plugin stages, the circuit breaker, per-target concurrency and
the request timeout, and record a request-log row. Where a guardrail is
configured and the body cannot be read as text, the request is refused rather
than forwarded.

Retry and model admission remain off, deliberately: the body is already streamed
and these endpoints are not idempotent, and the target is resolved before the
handler runs. Pass-through cost is recorded as unpriced rather than as zero.

### Breaking — `ADMIN_BOOTSTRAP_KEY` and its companions are removed

`ADMIN_BOOTSTRAP_KEY`, `ADMIN_BOOTSTRAP_READ_ONLY_KEY` and
`ADMIN_BOOTSTRAP_ENABLED` are gone. The path they opened was a function of
current state — it became live again whenever the number of usable admin keys
returned to zero — so a credential an operator believed retired came back when
the last key was deleted or expired. Deprecated since v1.0.3.

Set `MASTER_KEY` instead; `ferrogw init` generates one. There is no direct
replacement for the read-only bootstrap credential: authenticate with
`MASTER_KEY` and create a key with the `read_only` scope.

### Breaking — `OLLAMA_MODELS` is renamed `FERRO_OLLAMA_MODELS`

`OLLAMA_MODELS` is Ollama's own variable for the directory its models are stored
in. On any host running Ollama the gateway read a filesystem path and published
it as a model name.

The old name is still read this release and warns at startup; it is removed in
the next. When both are set the new name wins. A value that is a directory path
is ignored rather than registered as a model.

### Breaking — cached responses are scoped to the credential that fetched them

The response cache keyed entries on request content alone in one shared store,
so a response fetched for one API key could be served to another. Rate limiting
and budgeting already scope on the credential; the cache now does too. Requests
carrying no credential share one bucket, as they do in the request log.

Cache hit rate falls for any deployment using more than one data-plane key.
There is no switch to restore the previous behaviour, because it was the defect.

### Breaking — every routing mode skips a target whose circuit is open

Only `fallback` advanced past a failing target. Every other mode committed to
one target and retried it, so a dead backend failed every request while a
healthy sibling sat idle. Under `least-latency` this was self-sustaining: a
failure records no timing, so a target that died while it was fastest kept its
ranking.

Selection now skips open circuits under every mode. When every candidate is
open the gateway answers `503`, not `404` — the model exists and cannot be
served, which is a different statement from not being served at all. A matched
`conditional` or `content-based` rule becomes preferred rather than exclusive:
it is still honoured when it is the only target left.

`GET /v1/models` is deliberately unchanged by circuit state.

### Breaking — parameter support is reported honestly

`GET /v1/capabilities` reported `parallel_tool_calls` as forwarded by every
provider. Five cannot express it. It is now reported as unsupported on Cohere,
Gemini and Replicate, and translated on Anthropic and Bedrock Claude to the
field those APIs actually define — so a request that set it to `false` and saw
no effect will now see one.

Under `on_unsupported_param: reject` a request setting it against a provider
that cannot express it is refused; under the default it is logged.

`stream` and `stream_options` no longer appear in the listing at all. Usage
reporting is applied to every provider before the response leaves the gateway,
so it was never a provider capability.

### Breaking — an image request for a representation the provider cannot produce is refused

`response_format` was accepted and ignored by providers that emit only one
representation, so a caller asking for `url` received a 200 with a base64 field
populated and the field they asked for empty. Such a request is now refused with
400. Only an explicitly set value is checked; leaving it unset is unchanged.

`GET /v1/capabilities` publishes the representations each provider can produce.

### Breaking — the CLI has one argument, output and exit-code contract

`ferrogw status` exits non-zero when the gateway cannot be reached, and writes
its diagnostics to standard error. A gateway that answers — including one
reporting itself degraded — still exits zero. A script relying on `status`
always succeeding will now fail when the gateway is down, which is the point.

`--format` is refused by the commands that cannot honour it rather than silently
ignored. Every command rejects stray positional arguments instead of discarding
them. Colour is suppressed when output is not a terminal. A failing command no
longer prints its usage block. `ferrogw init` no longer prints a master key when
it leaves an existing configuration in place.

### Breaking — `X-Request-ID` is echoed only when it is a valid trace id

The header was adopted verbatim while the tracing pipeline requires 32
hexadecimal characters, so a client-supplied identifier could split the trace id
from the log id and from the one reported in spans. A value that is not 32 hex
characters is no longer echoed; an uppercase one is echoed lowercased. Requests
originating inside an embedded gateway now carry a trace id where they
previously carried none.

### Fixed — configuration rollback survives a restart

A rollback recorded through a configured config store was indistinguishable from
an ordinary update once the process restarted: the provenance existed only in
memory. It is now stored, added by migration and preserved across upgrade.

### Fixed — a request whose after-stage failed is counted once

Such a request wrote two terminal log rows, so it appeared twice in the log
listing and counted twice in usage statistics. The failure is now recorded on
the row that already exists. Totals on `/admin/logs/stats` fall accordingly for
affected deployments.

### Fixed — a stream whose client disconnects last still records

When a client hung up as the final chunk was delivered, the after-request stage
was abandoned with the connection and its durable log row was lost — and no
error row replaced it, because nothing had failed. That stage now runs on a
detached, time-bounded context, as the error stage already did.

### Fixed — two stores sharing one SQLite file wait for each other

No busy timeout was set, so concurrent writes failed immediately rather than
waiting — including two stores sharing one database file within a single
process, which is a supported arrangement. Connections now carry a default
timeout, and an operator-supplied one is honoured. Multi-instance deployments
belong on PostgreSQL.

### Fixed — admin listings have a stable order

`GET /admin/keys` and `GET /admin/sessions` returned rows in no defined order,
and the count guarding the last usable admin key included revoked keys. Both
listings are now newest-first with a stable tiebreak.

### Fixed — the gateway advertises only what it will serve

`GET /v1/capabilities` described every provider a credential registered rather
than the providers a configured target names, so an instance running
`targets: [deepseek]` reported nine and answered 404 from every routed surface
for eight of them. It now answers from the same set `/v1/models` does.

`/v1/models` published a duplicate `id` when more than one target served one
model — a declared id colliding with another target's catalog entry, or the same
id declared twice. The listing is keyed by `id` in the OpenAI contract, so a
client that indexed by it silently kept whichever arrived last. One entry per id
now, owned by the first configured target that serves it.

Under `mode: cost-optimized` with `unpriced_strategy: skip`, every declared
model was advertised and then refused: a declared model exists because no
catalog knows it, so it can never carry a catalog price. The listing now asks
the strategy that will route the request, so what it advertises is what the next
request can reach.

### Added — `plugin.Context.Target` and `api_key_id` on request-log rows

`plugin.Context.Target` names the routing target a request used: the virtual key
of the target that served it, or on a failure the last one attempted. It is
empty when no target was ever attempted — a request a plugin denied, a model no
configured target serves, or a response served from cache. Set before the
`after_request` and `on_error` stages on every routed surface. Under retry and
fallback it names the last target attempted, matching the per-provider error
counter, the span's target key, the failed lifecycle event, and the target
quoted in the error.

`api_key_id` on a request-log row is the opaque identifier of the credential a
request was served under, never the credential itself. Recorded on every stage
and returned by `GET /admin/logs`. Rows written before the column existed keep a
null, which is distinct from the empty value an unauthenticated request records.
Added by request-log schema migration 5; no operator action is required.

`GET /admin/logs` filters on it: `api_key_id=<id>` returns one credential's
rows, and `api_key_id=none` returns the rows naming no credential — both the
unauthenticated ones and those written before the column existed. The id is
matched exactly and is not checked against the key store, so a revoked or
deleted credential's traffic is still selectable. An empty value is read as
absent, as it is for the other filters.

The dashboard's Request Logs page shows which key served each request, by name
rather than by id, and filters by it. A key revoked or expired since is marked
as such; an id the key store can no longer name is shown as recorded.

`plugin.Context` gains a field. Out-of-tree plugins receive a `*plugin.Context`
and are unaffected; only code constructing one with an unkeyed composite literal
would need updating, which no supported usage does.

### Fixed — a failed request records which provider failed

The `on_error` request-log row carried no provider at all: the only place one
could be read from was the response, which a failure does not have. A stream
that died mid-response left a row saying something had failed and never what.

### Fixed — a request served from cache is recorded as costing nothing

It was priced as though the provider had been called, so its full estimated cost
was added to `gateway_request_cost_usd_total` and one prompt repeated a hundred
times reported a hundred times the spend actually incurred. The row meanwhile
recorded no cost at all, which reads as "could not be priced" rather than "cost
nothing". Both now report a known zero, and the row records how long the request
actually took rather than a duration of zero. Token counts are unchanged — usage
happened and cost did not — and the row is attributed to the credential that
consumed it, which for a shared cache entry is not the credential that primed
it. The budget plugin already declined to bill these requests and still does.

### Breaking — `targets[].retry` applies under every routing mode

Retry was wired only into `fallback`; the other seven modes made a single
attempt and logged nothing about it, while `config.example.yaml` documented
`retry` with no caveat. Retry is how many times **one target** is asked and the
strategy decides whether a **second target** is asked at all — two orthogonal
knobs, which is how the configuration already reads.

If you carry a `retry` block on a target under `single`, `loadbalance`,
`least-latency`, `cost-optimized`, `conditional`, `content-based` or `ab-test`,
that target will now be retried. Set `attempts: 1` to keep the old behaviour.

### Breaking — `Context.Skip` is removed from the plugin API

A plugin that answered a request used to end the whole `before_request` chain,
so a `response-cache` hit disabled every guardrail listed behind it — the order
the example config shipped — and a guardrail denial stopped the request logger
before it recorded anything.

`Skip` is replaced by `SkipProvider`, which declines the **provider call** and
nothing else: every remaining plugin still runs, `after_request` still runs, and
a denial still reaches `on_error`. `Context` also gains `Stage`, set by the
framework, so a plugin no longer has to infer its stage from a nil response.

`Skip` was removed rather than redefined: a plugin written against the old
meaning fails to compile instead of silently changing behaviour. Third-party
plugins that set or read it need updating.

A plugin listed at more than one stage is one plugin, so its entries must now
carry identical configuration; the gateway refuses to start and names the plugin
otherwise, rather than building two instances that share no state.

### Breaking — the routing index decides which provider owns a model

Most providers answered "yes" to every model, so a request for a model no target
owned was offered to several providers in turn — prompt included — before being
refused. Ownership now comes from the routing index: the union of a provider's
configured, catalog and discovered models.

A provider whose model set is named by whoever deploys it opts in explicitly by
implementing `core.AnyModelProvider`, and is consulted only when no target owns
the model. `SupportsModel` remains on the interface but no longer gates routing.

**A model that routed only because a provider claimed everything will stop
routing.** If a provider serves models the catalog does not list, set
`FERRO_MODEL_DISCOVERY_INTERVAL` (for example `6h`) so live discovery adds them,
or name them in that provider's own configuration. Note that `gemini`, `cohere`
and `perplexity` do not implement live discovery and are catalog-only.

### Breaking — strategy configuration is validated at load

An invalid strategy used to start cleanly and fail at request time: a negative
`ab_variants[].weight` returned 500 for every request with the reason in no log
line, and a typo in `conditions[].key` or `content_conditions[].type` silently
routed all of that rule's traffic to the first target.

These are now `ferrogw validate` and startup errors. Unknown condition keys and
content-condition types are rejected, a `target_key` must name a declared
target, and **`weight: 0` means zero traffic** — which is how a target is
drained before its credential is revoked. A negative weight and an all-zero
weight set are both errors; previously an all-zero set was an equal split.

### Breaking — `GET /admin/logs` returns one row per request

The endpoint returned one row per plugin **stage**, so a list of requests showed
each request twice — once complete, once as a started-but-empty row — and
`summary.total_entries` counted roughly double. The default is now the terminal
stages. Pass `stage=all` for the previous behaviour, or a named stage to select
one.

### Breaking — trace sampling follows the parent

The sampler is now `ParentBased`, the OpenTelemetry default. A request arriving
with a sampled `traceparent` is recorded whatever `sample_ratio` says, so a
ratio below 1.0 no longer punches holes in a distributed trace. The converse
also holds: a request whose parent is explicitly **not** sampled is no longer
recorded, even at `sample_ratio: 1.0`.

### Breaking — `metrics.CircuitBreakerState` is removed

Circuit-breaker state is now read from the breakers at scrape time rather than
written when a request resolves, so a breaker that recovered while idle no
longer reports open forever — and an alert on it can clear. Embedders using the
removed gauge should use `metrics.SetCircuitBreakerStateSource` with the
`CircuitClosed` / `CircuitOpen` / `CircuitHalfOpen` constants.

### Breaking — `<PROVIDER>_BASE_URL` is the API root on every provider

**Action required only if you set one of the variables below to a value that
carries a path.** With the variable unset, nothing changes: every provider sends
byte-identical requests to the same URLs as before, on every surface.

`<PROVIDER>_BASE_URL` used to mean one of two things depending on which provider
you were configuring. On most it was the API root, used verbatim, so you wrote
the `/v1` yourself. On eight it was the host root, and the provider appended its
own version segment — so writing the `/v1` produced `/v1/v1/chat/completions`
and a 404 that named no cause. An operator could not learn one rule and apply
it.

There is now one rule, and it is the one every official OpenAI client uses:
**the value is the API root, taken verbatim, and each surface appends only its
operation path.** Write it exactly as the vendor documents it, version segment
included. A base carrying no path at all still resolves to the provider's own
version segment, so a bare host keeps working.

Eight providers changed. If you set one of these to a value **with a path**, add
the version segment the provider used to supply:

| Variable | Before | Now |
|----------|--------|-----|
| `ANTHROPIC_BASE_URL` | `https://proxy.example.com/anthropic` | `https://proxy.example.com/anthropic/v1` |
| `DEEPSEEK_BASE_URL` | `https://proxy.example.com/deepseek` | `https://proxy.example.com/deepseek/v1` |
| `FIREWORKS_BASE_URL` | `https://proxy.example.com/fireworks` | `https://proxy.example.com/fireworks/v1` |
| `GEMINI_BASE_URL` | `https://proxy.example.com/gemini` | `https://proxy.example.com/gemini/v1beta` |
| `GROQ_BASE_URL` | `https://proxy.example.com/groq` | `https://proxy.example.com/groq/v1` |
| `MISTRAL_BASE_URL` | `https://proxy.example.com/mistral` | `https://proxy.example.com/mistral/v1` |
| `OLLAMA_CLOUD_BASE_URL` | `https://proxy.example.com/ollama` | `https://proxy.example.com/ollama/v1` |
| `TOGETHER_BASE_URL` | `https://proxy.example.com/together` | `https://proxy.example.com/together/v1` |

If you were working around the old behaviour by **omitting** a `/v1` these
providers rejected, that suffix is now what you write. `TOGETHER_BASE_URL=https://host/v1`
reached `/v1/v1/models` before and reaches `/v1/models` now.

Ollama Cloud's native embeddings root follows the configured one, so
`https://proxy.example.com/ollama/v1` reaches `/ollama/api/embed`.

Two providers are deliberately unchanged, because their vendor publishes no
single API root: `COHERE_BASE_URL` stays the host (Cohere serves `/v2/chat` and
`/v1/embed` from it), and `OLLAMA_HOST` stays the Ollama server URL (one server
mounts both `/v1` and `/api`). `DATABRICKS_HOST` and the `*_ENDPOINT` variables
are resource hosts and are unchanged too.

The `/v1/*` pass-through proxy reaches the same upstream URLs as before.

### Changed — `targets` is an allowlist on every surface

**Action may be required.** A provider that is registered but not listed under
`targets` no longer serves requests. Until now embeddings, image generation and
streaming each fell back to any registered provider when no configured target
could serve the model, so a provider the operator never listed could answer —
and bill — a request. Non-streaming chat never did this, which meant the same
model could resolve to a different provider depending only on which endpoint it
arrived on.

All four surfaces now agree: a model is served by a configured target or the
request is refused with 404 `model_not_found`.

If you relied on a provider being reachable without listing it, add it to
`targets`. `GET /v1/models` continues to advertise every model the gateway can
route, so a model listed there that now 404s is one whose provider is missing
from your `targets`.

Relatedly, streaming, embeddings and image generation resolve a target's model
support the same way the routing strategies always have — against the routing
index, the union of a provider's configured, catalog and discovered models —
rather than against the provider's own narrower `SupportsModel`. Previously
those three surfaces reached a catalog-only model only via the registry
fallback, so removing it without this would have made them refuse models the
gateway advertises.

### Changed — `/metrics` and `/debug/*` now require a scope, not just a credential

**Action may be required.** Those routes authenticated but never authorized, so
any valid credential reached them — including one whose scope authorizes nothing
else. A read-only key could retrieve a heap dump and the process command line.

They are now split into two tiers, matching where comparable systems draw the
line: `/metrics` accepts `read_only` or `admin`; everything under `/debug`
requires `admin`. A monitoring scraper's bearer sits unattended in a config file
on every node, which is the reason not to make it admin-tier; a profile is a
memory image that can hold request bodies, keys and prompts, and
`/debug/pprof/profile` stops the world.

If a Prometheus scrape breaks, its credential needs `read_only` or `admin`.
`ENABLE_PPROF` still decides whether the pprof routes exist; it never decided
who may call them.

### Changed — `/readyz` reports not ready when no configured target can serve

An instance whose `targets` name no registered provider previously reported 200
ready while failing every request with a routing error, so nothing took it out
of rotation. Readiness now gates on target routability:

| Targets routable | Startup | `/readyz` |
|---|---|---|
| all | silent | `200 ready` |
| some | `WARN` naming the unroutable ones | `200 ready`; `targets[]` marks them `routable: false` |
| none | `ERROR` naming them | `503`, reason `no routable targets` |

Startup warns rather than exiting: a provider registers only when its credential
is present, so a target whose key has not rolled out yet is a legitimate config
that starts serving the moment the secret lands. Readiness gates only on the
total case — one unroutable target among several is degraded, and pulling an
instance that still answers most requests out of rotation makes the outage
worse.

The 503 reason string changed from `no ready providers` to `no routable
targets`; update anything matching the old literal. `/readyz` ready responses
now also carry a `targets` array.

### Changed — `POST`/`PATCH /admin/keys` reject an unrecognized scope

Any scope string was accepted, so a typo minted a credential that
authenticated, looked valid in `GET /admin/keys`, and authorized nothing. The
valid set is now enforced at the write boundary, returning 400 `invalid_scope`
naming the offending value and the accepted scopes. Omitting the field still
applies the least-privilege default. Existing keys are not revalidated.

### Fixed — a config applied over the admin API is decoded as strictly as a file

`PUT`/`POST /admin/config` discarded any key not in the schema and answered
`{"status":"updated"}`, so a config `ferrogw validate` rejected was accepted
here and silently applied without the setting that had just been written. The
two paths now decode identically: an unknown key is a 400 naming it, as is data
trailing the top-level object.

The field this cost most is `targets[].models`. It is hand-written, nothing else
supplies it, and a `model:` typo left a config that reported success and then
routed as though no model had been declared. The same held for `virtual_key`
and every key under `strategy`.

### Fixed — a base URL keeps the credentials written into it

A `<PROVIDER>_BASE_URL` carrying userinfo — `https://user:pass@proxy.example.com`,
how a corporate egress proxy is addressed — lost it when the base carried no
path, and the provider then reached the proxy anonymously. Affected the
providers that set no `Authorization` header of their own, since HTTP Basic is
only injected into a request that has none. A base carrying a path was never
affected.

A base carrying a query string or fragment is now refused at startup rather than
resolved. An operation path is appended to whatever the root resolves to, so
`https://host/v1?a=b` asked for `/v1` with the operation buried in a query value
— a request no configuration could have meant, and one that failed upstream with
nothing pointing at the cause.

### Fixed — an unroutable model returns 404 rather than 500

A model no configured target could serve produced `500 routing_error`, which
instructs an OpenAI SDK to retry a request that can never succeed. The two
conditions are now distinguished: "no configured target serves this model" is
404 `model_not_found`, while a capable target that was actually called and
failed keeps its upstream-derived, retryable status.

Routing errors also no longer echo internal state. `all providers failed:
provider not found: anthropic` disclosed a configured target's name to the
caller; the response now carries a message describing the class of failure and
the detail goes to the log. A plugin's rejection reason and an upstream 400/422's
own message are still passed through, because both are written for the caller.

### Fixed — the gateway's own 429 responses carry `Retry-After`

The per-IP limiter, the per-target concurrency limiter (`provider_saturated`)
and the rate-limit plugin all returned 429 without the header, so SDKs fell back
to their own backoff and the gateway's own limiter was the one producing retry
storms. All three now send `Retry-After: 1` — the honest floor at any rate of
1 rps or more. An upstream's own hint still takes precedence.

### Fixed — `ferrogw validate` catches what `serve` would reject

`validate` accepted configs that made `serve` exit 1: an unknown plugin name, an
unknown plugin stage, and a target naming a provider that does not exist. Since
`validate` is what a deployment pipeline runs as its pre-flight gate, it gave
false confidence exactly where it is relied on. It now resolves every
`virtual_key` and every enabled plugin's name and stage against what the binary
knows, and names the valid values on failure. It deliberately does not check
credentials — a pipeline runs `validate` without secrets, so a target naming a
real provider whose key exists only in production is valid.

### Breaking — `max_completion_tokens` supersedes `max_tokens` on every provider

A request naming a small `max_tokens` alongside a large `max_completion_tokens`
passed a `max-token` cap and then had the large value forwarded upstream:
providers on the OpenAI API surface forward `max_completion_tokens` and clear
`max_tokens`, while the guardrail read `max_tokens`. Either field alone was
correctly rejected. Every entry point — chat, streaming, and the legacy
completions surface that routes through chat — now resolves the pair to one
value carried by both fields before any plugin runs, with
`max_completion_tokens` superseding the deprecated `max_tokens` as the OpenAI
API itself does, so the ceiling a guardrail approves is the ceiling that
travels.

**What changes for you.** A request that sets both fields to *different* values
now gets the `max_completion_tokens` value on **every** provider, including
those that previously read only `max_tokens` and therefore honoured the smaller
legacy field. A caller relying on that — sending a large
`max_completion_tokens` for OpenAI-style models and a small `max_tokens` as the
effective limit elsewhere — will see longer completions and higher spend on
those providers.

This is the precedence the OpenAI API itself applies, and the alternative
considered — clamping to the smaller of the two — was rejected because it
silently truncates a completion relative to what the identical request returns
sent directly upstream. Send one field, or set both to the same value.

### Fixed — a rate-limit plugin rate of zero is refused instead of blackholing traffic

`requests_per_second: 0` started a gateway that reported healthy and answered
429 to every request for the rest of its life; `-1` did the same. Its siblings
`key_rpm` and `user_rpm` already refused to start on the same value. All four
fields are rates and all four must now be positive, rejected at load with the
same message whether it is `ferrogw validate`, `ferrogw doctor`, or startup that
asks. Turn the plugin off with `enabled: false`. The separate per-IP limiter
configured by `RATE_LIMIT_RPS` still reads `0` as "no limiting" — there the
variable is the whole switch — and that difference is now documented on both
sides.

Plugins can publish deployment-independent config rules through the new
`plugin.ConfigValidator` interface; `validate` and `doctor` read them without
constructing the plugin, so no `${VAR}` is resolved and the check still runs on
a machine with no secrets.

### Fixed — production mode guards more than one setting

`GATEWAY_ENV=production` affected exactly one check. It now refuses to start on
`ALLOW_UNAUTHENTICATED_PROXY=true` (as before) and on a `*` entry in
`CORS_ORIGINS`, which is matched literally and therefore permits no
cross-origin request while reading as though it permits all of them; both
refusals are reported together. It warns, without blocking startup, when per-IP
rate limiting is disabled, when `ENABLE_PPROF` has mounted the profiling
routes, and when the API key store is in-memory — each a defensible deployment
choice that was previously an INFO line or nothing at all. Outside production a
`*` CORS entry now warns rather than passing silently.

### Fixed — `ferrogw init` scaffolds only providers this environment can serve

`init` wrote `openai` and `anthropic` targets unconditionally, so an operator
without an Anthropic key got a starter config naming a provider they could not
use. It now scaffolds the providers whose credentials are actually present,
using the same check auto-registration applies at startup. With no credentials
present it writes a single target labelled in the file as a placeholder, since
an empty target list would not pass `ferrogw validate`.

### Fixed — provider credentials no longer reach telemetry

An upstream error quoting the credential it rejected was written verbatim into
spans, structured logs, the request-log store and exported events, at the
default `privacy_level: metadata`. Redaction matched credential *shapes*, so
keys without a distinguishing prefix passed through.

The gateway holds the credentials it was configured with, so it now redacts
them by **value** at every point text leaves the process, naming the variable in
their place (`[REDACTED:MISTRAL_API_KEY]`) so a rejected key is still
identifiable. Shape rules remain as a backstop for credentials the gateway never
saw. Adding a provider requires no redaction change: a test asserts every
credential-carrying environment mapping is enrolled.

An upstream body that is not one of the recognised error envelopes is no longer
echoed to the caller at all — a proxy or WAF returning HTML could otherwise put
an internal hostname, an account identifier and a credential into a client
response. `mcp_servers[].url` and `observability.tracing.endpoint` are now
scrubbed from `GET /admin/config`, and an MCP tool failure returns a fixed
message rather than the transport error.

### Fixed — two instances starting against one fresh database no longer deadlock

Migrations took a global advisory lock and then built an index concurrently on a
pooled connection. A concurrent build waits for every open snapshot, and a
second instance blocked on the lock **is** an open snapshot, so neither could
proceed and neither ever bound its listener. Waiting for the lock no longer
holds a snapshot, the create-time index build is not concurrent on an empty
table, and a blocked migration reports itself and gives up rather than hanging.

Expired sessions are also reclaimed rather than only filtered out on read, and
the idle-activity write is throttled instead of firing on every authenticated
request.

### Fixed — the configuration in force is the one reported

A persisted configuration takes precedence over the file, which is what makes
rollback work — but nothing said so, and the startup log described the file it
had discarded, including plugins that were not running. Startup now names the
source it resolved and warns when a file was superseded, listing what differs.

Values that can change at runtime are read per request rather than captured at
startup, so `max_request_bytes` takes effect when it is changed instead of only
after a restart. `observability` is boot-only and now says so when changed.

### Fixed — the surfaces report failures the same way

An upstream status was recovered by parsing the error text, which worked for
chat and never for the SDK-backed surfaces. The status is now a field on a typed
error, which fixes three consequences at once: a deterministic 400 is no longer
retried (`attempts: 3` had meant nine upstream calls on embeddings), an upstream
429 no longer opens the circuit breaker and takes the instance out of rotation,
and a failure is classified rather than reported as an opaque 500.

Alongside that: the gateway's own request timeout returns 504, an open circuit
returns 503, request duration is observed for failures as well as successes,
provider errors carry the provider's name, and a plugin rejection on embeddings
or images is counted as a rejection rather than an error. Embeddings and image
requests are recorded in the request log, which had only ever recorded chat.

### Fixed — tracing exports where it is pointed

An OTLP endpoint carrying a path — the form collector documentation prints —
folded the path into the host and exported nothing, reporting the failure below
the level most deployments log at. Endpoint handling now follows the OTLP
specification, a malformed endpoint fails at startup rather than silently
disabling tracing, and either standard endpoint variable activates the pipeline.

Configuration changes are audited, the rate-limit plugin's rejections reach the
metric named for them, and MCP startup spans are exported — they were created
before the tracer provider was installed.

### Fixed — the dashboard's plugin catalog matches the gateway

The list of built-in plugins and their settings was maintained separately in the
web application and had drifted: four of six entries named settings that do not
exist, in the panel that invites copying them into a configuration. Unknown
plugin settings are accepted silently, so an operator following it got the
default rate limit rather than the one they wrote. The gateway now serves its
own catalog, and a test asserts it against the plugin packages themselves.

### Fixed — the pass-through resolves a model the same way every other surface does

`/v1/*` picked a provider with each provider's advisory `SupportsModel`, i.e.
whichever registered first and answered yes. A model no configured target served
was forwarded anyway — body included — and a model an owner *did* serve could
still reach a different provider. It now resolves through the routing index: an
unowned model is refused `model_not_found` and nothing is forwarded. The
`X-Provider` header is unchanged and remains the way to reach an endpoint whose
model ids no index can enumerate.

### Fixed — `ferrogw validate` and `doctor` reject a disagreeing multi-stage plugin

A plugin listed at several stages must carry identical configuration or the
gateway refuses to start; both commands reported such a config as valid. The
rule moved into config validation, which `serve` already reaches, so neither
command has its own copy. `doctor` also resolves provider and plugin names now —
it reported a target naming no known provider as healthy.

### Changed — a provider's base URL is the API root, used verbatim

`<PROVIDER>_BASE_URL` was rewritten conditionally on the chat path and passed
through unmodified on the others, so a base without a `/v1` suffix left chat
working and sent embeddings and images to the wrong path. The value is now used
as written, matching what `base_url` means in openai-python, openai-node,
LiteLLM and Portkey. A base carrying no path at all still resolves to `/v1`,
since a bare host is not an API root.

One deployment shape changes: a base with a non-`/v1` path such as
`https://host/openai` previously sent chat to `/openai/v1/chat/completions` and
now sends it to `/openai/chat/completions` — the same path its embeddings
already used.

### Fixed — a wrong HTTP method on a native route is refused, not proxied

`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`,
`/v1/images/generations`, `/v1/models` and `/v1/capabilities` were registered
for a single method each, so any other method fell through to the `/v1/*`
pass-through and was forwarded upstream with the operator's credential — past
every guardrail, budget, circuit breaker, per-target concurrency limit and the
`targets` allowlist, and without a log entry. A wrong method on a route the
gateway serves itself now returns 405 with an `Allow` header. Paths the gateway
does not serve still reach the pass-through unchanged.

---

The dashboard is rebuilt as a React single-page application. The gateway still
serves it, from the same binary and the same port as the API.

**No action required to upgrade.** The dashboard moves from `/dashboard` to the
site root, so `https://your-gateway/` is now the console; the old paths are
gone. Everything else behaves exactly as it did in 1.3.2 — the Admin API under
`/admin`, the OpenAI-compatible API under `/v1`, configuration, and every
environment variable — and the binary is still one file with no external
services.

```text
https://gateway.example.com/         -> dashboard
https://gateway.example.com/admin/*  -> Admin API
https://gateway.example.com/v1/*     -> gateway API
```

Because the page and the API share an origin, the dashboard needs no
`CORS_ORIGINS` entry, no second container, and no separate version to keep in
step. `CORS_ORIGINS` is still there for browser applications of your own that
call the gateway from elsewhere.

### Added

- An embedded React and TypeScript operations application for overview, key management, request logs, provider status, configuration history, analytics, and streaming playground workflows.
- Component health details on `GET /admin/health` for the API, key store, config store, and request-log backend.
- An independent frontend CI job — lint, unit tests, TypeScript, production build, and rendered browser checks — running alongside the Go suite.
- Dashboard sign-in exchanges a gateway key for a short-lived session. The session expires after 24 hours or an hour of inactivity, signing out revokes it server-side, and `DELETE /admin/sessions` signs every operator out at once. The key itself is no longer kept in the browser.
- Configuration history records who applied each version. A dashboard session is attributed to the credential it was minted from, since the session identifies a browser and the credential identifies the operator. Versions written before this release keep no actor rather than being backfilled with a guess.
- Credential changes are recorded. Creating, updating, deleting, revoking or rotating a key, and signing every operator out, each emit a structured log entry naming the actor and the target. Previously only failures were logged, so a successful deletion of the last admin key left no record at all. These are log entries, so log retention is what bounds how far back the question can be answered.
- `GET /admin/logs/stats` reports a time series, an input/output token split, and the most frequent failure messages. `buckets=N` asks for a series over the requested range; each dimension group now carries its error and token totals alongside its event count. The aggregation query already computed those totals per group and discarded all but the count, so the gateway could say which model was called most often but not which one consumed the tokens — usually a different model, and the more useful question.
- Analytics answers what an AI gateway is asked about: traffic and failures over time, token throughput split by direction, which models consume the token budget, and which errors recur. Token direction is reported separately because output tokens bill at a multiple of input tokens, so a single total hides what moves the bill.
- Request logs record per-request latency, time to first token, and estimated cost. The gateway already measured all three and sent them only to Prometheus and OpenTelemetry, where they cannot be queried by time range — so none of it reached the dashboard, and none of it survived a restart. `GET /admin/logs/stats` now reports latency and time-to-first-token percentiles (p50/p95/p99/max/mean) and attributes spend per provider and per model.
- Analytics shows request latency, time to first token, and estimated cost per model. Latency is reported as percentiles rather than an average, because a mean hides the tail an operator is paged about: one request in twenty taking ten seconds barely moves it.
- `plugin.Context` carries a `Measurements` field, so a plugin can read the duration, time to first token and cost of the request it is observing. Additive: an existing plugin compiles and behaves unchanged.

### Fixed

- A provider call is bounded by how long a model can take to answer rather than how long an HTTP service can take to respond. The default header timeout was 30 seconds, but a provider sends no header until it has something to say — the whole generation for a non-streaming call, the first token for a streaming one — so a reasoning model or a long prompt aborted with a transport error. Seven providers already carried their own longer value for exactly this reason; the 17 streaming providers still on the default, among them DeepSeek, OpenRouter, Mistral, Qwen, Moonshot, Together and Fireworks, now get 120 seconds. Any provider with its own preset still overrides it, and the bound stays finite: with no `request_timeout` configured it is the only thing stopping a provider that accepts a connection and never answers.
- `GET /admin/config/history` serves the durable trail and `POST /admin/config/rollback` can reach it. The trail was written and never read, so the endpoint served an in-memory list that reset on restart and a rollback could not target a version recorded before it. Version numbers came from two independent counters, so after a restart the version a client saw and the version stored named different configs. A config reset is now recorded too, and a config is applied to the gateway before being persisted, so one the gateway rejects is no longer left in the store.
- `GET /admin/health` probes the request-log store instead of reporting it healthy unconditionally — it was the only component in that list never asked.
- The last-admin guard counts only keys that can authenticate today. An expired admin key satisfied the guard while failing authentication, so with one expired and one live admin key, deleting the live one was permitted and locked the operator out immediately.
- With `n > 1` and MCP tools configured, a tool is executed once rather than once per alternative completion. Each returned choice's tool calls were executed, so a tool that sends mail or writes a row ran once per alternative and the results were flattened across them.
- A tool name exposed by two MCP servers stays reachable when one dies; the withdrawn server previously kept the name pointed at itself. Reaching the tool-call depth limit is reported rather than handing the model's unexecuted tool calls back to the client, naming gateway-injected tools the caller never declared.
- Streaming tool-call fragments are merged by index rather than appended, so the response handed to after-request plugins, the request log and cost accounting holds whole tool calls instead of fragmented, duplicated ones.
- A stalled client on `/v1/*` is cut off: the proxy cleared its write deadline before the flush that reaches the socket, leaving the write that can actually block unbounded.
- A provider that writes `data:{...}` without the optional space is read correctly. Requiring the space dropped every frame of such a stream, which read as an empty but successful response.
- Gemini reports completion tokens including thinking tokens, so prompt plus completion equals the total it reports and thinking output is costed at the right rate. The OpenAI `developer` role is mapped to the system role on providers that reject it. Replicate no longer drops an explicit `temperature`, `top_p` or `seed` of `0`. Model lists read from the environment are trimmed, so a space after a comma no longer produces a model name that can never match.
- Request-log column migrations are idempotent, and the timing and cost columns store the same precision on both backends — they were declared `REAL`, which Postgres stores with about seven significant digits, enough to visibly round a per-request cost.
- A target is no longer taken out of rotation permanently by a stream start that was abandoned at the request deadline and then succeeded. Its half-open circuit-breaker probe was never resolved, and nothing repairs a half-open circuit stuck at its probe cap, so every later request to that target — streaming and non-streaming — failed with "circuit breaker open" until the process restarted.
- A request shed while queued on a target's concurrency limiter no longer counts against that target's circuit breaker. It returned a bare context error, which reads as the gateway's own deadline firing against the provider, so a burst of load opened the breaker of a target that was answering everything it was given. It now reports `429 provider_saturated`, as the queue-full shed already did. <!-- drift-ok: 429 is an HTTP status code, not a provider count -->
- A configuration reload preserves circuit-breaker and concurrency-limiter state for targets whose settings did not change. Reloading previously rebuilt both, so an open circuit closed and re-admitted traffic to a provider that was still failing, and a second full-capacity limiter briefly joined the in-flight requests. Targets that are new, changed or removed are rebuilt as before.
- A malformed `targets[].circuit_breaker.timeout` is reported instead of silently becoming the 30-second default.
- Per-IP rate limiting no longer stalls once its key store fills. At the shipped 100,000-key cap, every request from an unseen address walked the whole store to evict one entry while holding the lock that fronts the rate-limit check on every route — 15ms per insert with all concurrent traffic queued behind it. Eviction is now amortised over the oldest one percent: 52µs per insert on the same benchmark.
- An observability exporter that panics no longer takes the gateway down. Exporters are third-party code and were dispatched without recovery; the panic is now logged with the exporter's name and the remaining exporters still receive the event.
- `gateway_requests_total` no longer grows a new time series per request on the non-streaming success and after-request-abort paths, where the provider-reported model was used as a label directly while every other path bucketed it first.
- A configured alias routes on `/v1/chat/completions`. Aliases resolved inside routing, but the request was tested against the raw model name first and rejected with `400 model_not_found` before it got there — so an alias worked on `/v1/embeddings` and `/v1/images/generations`, which have no such check, and failed on chat.
- The gateway routes the models it advertises. `GET /v1/models` and the admission check answer from a model index built from a provider's declared models, the catalog and live discovery, but routing selected on the provider's declared models alone. A catalog model returned `500` on non-streaming chat while the same model streamed correctly — affecting Mistral, Vertex AI, Bedrock, Replicate and Gemini models on a default configuration.
- An unknown model answers `404 model_not_found` rather than `500 server_error`, matching what `/v1/embeddings` already returned for the same condition.
- A target that sheds a request under its own concurrency limit is no longer retried against that same target. Saturation carries no status code, so it read as a transport failure and the request backed off and returned to the target that had just declined it.
- The conditional strategy selects the same target on both surfaces. A request matching no rule resolved to the configured fallback when routed and to the first configured target when selecting a streaming target; these differ whenever the two are not the same target.
- `ferrogw init` tells you to point the gateway at the config it wrote. A config file is read only when `GATEWAY_CONFIG` names it, so following the Quick Start produced a gateway running on the auto-derived default with the new file ignored — no targets, plugins, budgets, retry policy or aliases, and no warning. The Quick Start also now exports provider keys before starting the server: providers are registered at startup, so exports that followed it did nothing for the running process.
- The built-in plugins that work across the request lifecycle now do both halves of their job. `response-cache`, `budget` and `request-logger` each check or read before the request and store or record after it, but a plugin was built fresh per configuration entry and registered under a single stage, so each ran only its first half: the cache never stored and so never served a hit, `spend_limit_usd` measured spend that nothing accumulated, and the request logger — enabled by default — never wrote the completion row carrying latency, time to first token and cost. Listing a plugin twice did not help, because the two entries did not share state. One instance is now registered at every stage its configuration names, and the example configurations list all three at both stages.
- The response cache distinguishes requests that differ only in an image or a tool call. Its key covered the plain text of each message, and multipart content collapses only its text, so two vision requests sharing a prompt but referencing different images produced the same key and the second caller received the first caller's answer. Tool calls, tool call ids and reasoning content were likewise absent. Upgrading invalidates existing entries, which for the in-memory store means the first request after a restart.
- `max_messages: 0` and `max_tokens: 0` disable their limits, as `max_input_length: 0` in the same configuration block always did. `max_messages: 0` previously rejected every request. The input length limit also counts multipart payloads, so an image no longer passes a limit measured only against text.
- Request log write failures are reported. A store that was down or full dropped the persisted trail with no log line, and the plugin ships enabled.
- Keys created with `ferrogw admin keys create` carry the scope that was asked for. The command sent the permission under a field name the API does not read, and a key arriving with no scopes was given the admin scope — so `--scope read_only`, and the default, both produced a working admin credential. **Audit keys created through the CLI: any of them may hold admin scope that was never requested.** Existing keys are unchanged.
- `POST /admin/keys` without a `scopes` field creates a read-only key rather than an admin one. Callers that want admin send `{"scopes":["admin"]}`. The bootstrap credential now stays valid until a stored key can authenticate an admin request, rather than being withdrawn as soon as any key exists — on a deployment with no master key, a read-only first key would otherwise have left no way to create the admin key it still needed.
- Rotating or removing `MASTER_KEY` invalidates the dashboard sessions minted from it. Such a session previously kept full admin access for the rest of its lifetime, so an emergency rotation cut off the key without cutting off what had been minted from it. Affects persistent session backends; the default in-memory sessions did not survive a restart in any case.
- Credentials passed to an MCP server as command-line arguments are redacted by `GET /admin/config`, as its headers and environment already were.
- The client address is resolved from the far end of the proxy chain. `X-Forwarded-For` was read leftmost and the trusted-proxy check applied only to the immediate peer, so behind any appending reverse proxy a caller could choose its own address — taking a fresh rate-limit bucket and a fresh sign-in throttle per request, and writing a forged address into the audit trail. Resolved addresses will change for deployments whose clients send the header.
- A caller-chosen model can no longer address a path of its choosing on a provider host. AI21's Jurassic route interpolated the model into the request URL unescaped, and Azure OpenAI left dot segments in its deployment, so a caller holding only an inference key could direct a request carrying the operator's provider credential. Every provider was reviewed.
- Upstream provider failures report what the provider said instead of a blanket `500`. A `429` stays a `429` and carries the provider's `Retry-After`; a `400`, `422` or `404` is reported as the caller's to fix; an upstream `401` or `403` surfaces as `502 upstream_auth_error`, because the rejected credential is the operator's provider key and not the caller's gateway key; other failures surface as `502`, and a timeout as `504`. Alerting keyed on gateway `500`s will see upstream failures move to `502` and `504`.
- A streaming request that fails partway reports the failure instead of ending as a success. A provider that fails after its headers are out sends an error frame, which decoded into an empty chunk: the caller received a truncated answer with nothing marking it as incomplete, and the failure reached no counter, span or log. Covers the shared OpenAI-compatible reader and the OpenAI provider's own path, which carried the same gap.
- Stream chunks carrying no choices serialize `"choices": []` rather than `null`. The terminal usage frame, which the gateway requests on every OpenAI stream, previously broke the documented client loop that indexes the first choice on each frame, and the usage totals were lost with it.
- Gemini reports why the model actually stopped. A response containing tool calls reported `tool_calls` even when Gemini ended it at a token limit or on a safety filter, so truncated and blocked answers were indistinguishable from complete ones; and streaming chunks carrying a tool call reported a finish reason before the stream ended, so a client that stops at the first one lost the later tool calls.
- `ferrogw admin` renders the records the API returns. The key list read a scope string where an array is sent, the provider list read a model count that is not sent, and the log and configuration-history tables read the response envelope rather than the rows inside it — printing a single empty row in place of every record. The log table reports the persisted stage; there is no HTTP status on that row.
- Request totals and failure rates count completed requests rather than log rows. The request logger writes one row per plugin stage, so the dashboard previously reported roughly twice the real traffic and half the real failure rate — a gateway failing every request displayed 50%.
- The playground reports a failed stream instead of presenting it as a finished answer. A mid-stream provider failure or idle timeout arrives as an ordinary frame on an already-successful response, and was discarded, leaving a truncated reply committed as though the model had finished.
- A newly created key can no longer be lost to a stray click. The dialog that shows it once now requires an explicit dismissal.
- Failures from revoking, deleting or rotating a key appear inside the confirmation that triggered them. They were rendered behind its backdrop, so the gateway declining to revoke the credential the request came from looked like the button doing nothing.
- Rotation is refused on an expired key, which previously produced a working-looking secret that could not authenticate.
- The request-log purge states what it deletes. It ignores the filters on screen, so confirming it while filtered to one provider destroyed every provider's history.
- A search that matches nothing on the current page no longer hides pagination, and the count no longer divides a filtered numerator by an unfiltered total.
- Request logging being disabled reads as a disabled feature rather than a failure, on both the logs and analytics pages.
- Editing configuration is protected. Refreshing no longer discards an edit in progress, and saving is blocked while the buffer still holds the redaction placeholder that the read path substitutes for plugin configuration.
- The getting-started checklist derives from gateway state rather than browser storage. Visiting a page that returned an error previously ticked its step and announced the gateway was ready.
- Providers reports registration rather than availability, which is what the status means: a provider with a revoked credential resolved in the registry and showed as healthy.
- The traffic chart no longer implies an outage when the newest rows span minutes inside a window of hours.
- Traffic charts cover the selected range rather than one page of the request log. They were bucketed in the browser from at most two hundred rows, which on a busy gateway is a few seconds of traffic drawn as if it were the whole window; the gateway now aggregates the series and says so when a range holds more events than one query returns.
- A request-log timestamp is stored in UTC regardless of the zone it arrives in. Range filters compare the stored rendering of the timestamp, which is chronological only while every row carries the same offset, so a caller passing a local time wrote rows that sorted into the wrong window.
- Analytics no longer ranks unattributed events as the busiest provider. Only an answered request carries one — a request is logged before a provider is chosen, and a failure without ever reaching one — so those rows aggregated under "unknown" and took first place.
- A failed request records how long it took to fail. A provider timing out after thirty seconds and one refusing immediately are different incidents, and the duration is the only record of which it was.
- A gateway path that climbed above the root resolved to a protocol-relative URL and left the configured origin, carrying the session token with it. No call site could reach it, and the Content-Security-Policy bounded the request, but the guard checked the wrong value.
- Authenticated responses are no longer written to the browser cache, where several admin bodies could outlive signing out.
- `POST /admin/session` is throttled independently of `RATE_LIMIT_RPS`. It is the only unauthenticated write path on the gateway, and its only bound was the general per-IP limiter — which shares a bucket with `/v1` traffic and is removed entirely by `RATE_LIMIT_RPS=0`, a setting reached for while tuning inference throughput with no indication it also unbounds sign-in. The allowance is sized for a whole team behind one shared egress address, and its rejections are counted under their own metrics label so a burst of failed sign-ins is distinguishable from a busy gateway.

### Changed

- **One `deploy/Dockerfile` builds the one image.** The Docker files were spread across the root and `web/`; they are now `deploy/Dockerfile`, `deploy/gateway.release.Dockerfile`, and `deploy/compose{,.dev,.prod}.yaml`. A node stage inside it produces the dashboard bundle that the Go stage embeds, so `make docker-build` yields a single container serving both.

  The Go stage lists its inputs rather than copying the tree, and the bundle arrives from the node stage rather than the build context, so a dashboard edit costs one inter-stage copy instead of a full Go rebuild.

  `gateway` is deliberately the last stage. Render's blueprint spec has no build-target field, so it builds whichever stage comes last, and `render.yaml` deploys the gateway. `gateway.release.Dockerfile` stays separate because GoReleaser builds it against a temporary directory holding only the cross-compiled binary.

  Sharing a context means sharing one `.dockerignore`, which Docker reads only from the context root. `web/.dockerignore` is therefore removed and its rules moved into the root file under `web/` prefixes. Patterns there are anchored at the root and a single `*` does not cross `/`, so `web/.env` is spelled out separately from `.env` — without it, an operator's local file would reach the Vite build, which inlines every `VITE_`-prefixed value into the published bundle.

  `make up` starts one container. The dashboard is at the gateway's own address, so there is no second service, no second port, and no second image to publish.

  `make up`, `make up-prod`, `make down`, and `make docker-build` wrap the base-plus-override file pair, so the paths do not have to be typed. Anyone invoking Compose directly needs the new ones: `docker compose -f deploy/compose.yaml -f deploy/compose.dev.yaml up`. Two paths that resolve against the Compose file's own directory changed with it — the dev config mount and the prod `config.yaml` mount are now `../`, keeping both files at the repository root where they were. The Compose project name is pinned to `ai-gateway`, the value it previously derived from the directory; without that, moving the files would have renamed the project to `deploy` and left an existing stack's containers, volumes, and networks orphaned beside a new one.

  `.dockerignore` and `render.yaml` deliberately stayed at the root: Docker resolves `.dockerignore` from the build context root rather than from beside the Dockerfile, so a copy moved into `deploy/` would have stopped applying without any error, and Render only reads `render.yaml` from the root. Both now point into `deploy/`.

- **Dependabot tracks the dashboard's build base.** The `docker` ecosystem entry scanned `/` only, and that ecosystem does not recurse, so the dashboard image's bases went untracked while it lived in `web/` — with no error and no pull requests to notice the absence of. The `node`, `golang`, and `alpine` bases now all sit in `deploy/Dockerfile`, which the entry points at.

- **Breaking: `POST /v1/completions` is served through the gateway rather than forwarded to the provider.** The route was answered straight from the provider registry while every sibling route was answered by the gateway, so it applied none of it — a prompt a guardrail blocked on `/v1/chat/completions` was served here unfiltered and unbilled, and a provider whose credential was configured but which was deliberately left out of `targets` was reachable through it. Configured targets, aliases, the routing strategy, plugins, the circuit breaker, per-target concurrency limits, metrics and request logging now all apply, and upstream failures are classified as they are on the other routed surfaces.

  The verbatim upstream forward is removed rather than kept alongside, because the two cannot coexist: running the pipeline around a verbatim forward would inspect the translated request and send the raw one, so a transform plugin's rewrite would be silently discarded, and keeping the forward only for request shapes the chat translation cannot express would let a caller opt out of the guardrails by sending one of those shapes.

  What this removes on this route: `stream: true` is refused with `400 streaming_not_supported`; batch prompts (`"prompt": ["a","b"]`) and token-id prompts are refused with `400 unsupported_parameter`; `best_of`, `logprobs` and `suffix` are accepted and ignored (`echo` is honoured — see the entry above); and a model that exists only behind the legacy completions API and rejects chat requests — `gpt-3.5-turbo-instruct` is the real one — will now fail. Use `/v1/chat/completions` with a chat model instead. Anthropic, Gemini, Cohere, Bedrock, Vertex AI and Azure models are better served than before: they were previously sent an OpenAI-shaped legacy body to an upstream with no such route and returned that upstream's raw 404.
- `request_logs` gains nullable `duration_ms`, `ttft_ms` and `cost_usd` columns, applied automatically on start. Existing rows keep NULL rather than being backfilled with zero: a request the catalog cannot price has an unknown cost, not a free one, and a non-streaming request has no time to first token at all. The distinction is preserved through the API and the dashboard, which report "unpriced" separately from "$0".
- The dashboard is built on Tailwind and a component library rather than a hand-written stylesheet, on the palette it already used. The light theme's accent is one step darker to meet WCAG AA contrast, which it previously failed both as link text and as a button fill.
- Every route is reachable by keyboard without traversing the navigation first.
- Active dashboard sessions are visible from the API keys page, and a key's expiry can be extended or cleared instead of requiring the key to be recreated.
- The web application now owns its client routes and consumes the existing Admin and OpenAI-compatible APIs from the same origin the gateway serves it on.
- Dashboard sessions live only in `sessionStorage` and do not survive closing the tab; read-only scopes hide mutation controls.
- The dashboard's page and assets are served under the dashboard Content Security Policy and immutable asset caching, applied independently from gateway API headers.
- `GET /admin/sessions` lists active dashboard sessions; sessions persist across restarts when a SQLite or PostgreSQL key-store backend is configured, and are held in memory otherwise.

### Removed

- **Breaking:** the `/dashboard`, `/dashboard/*`, and `/logo.png` paths. The dashboard is served from the site root instead; update any bookmark or ingress rule that names the old paths.
- The legacy Go template renderer, page-specific browser scripts, vendored chart runtime, and dashboard-owned HTTP helper package.
- The `GATEWAY_BASE_URL` variable, the nginx configuration, and the container entrypoint hook that wrote `config.json` from it. The dashboard is same-origin, so there is no gateway address for it to be told.
- Node from `go build` and `go test`, which compile against a committed placeholder. The paths that ship a binary — `make build`, the image, and GoReleaser — build the bundle first.

## Past releases

Release notes for shipped versions are archived by minor series:

- [1.3.x](docs/changelog/v1.3.md)
- [1.2.x](docs/changelog/v1.2.md)
- [1.1.x](docs/changelog/v1.1.md)
- [1.0.x](docs/changelog/v1.0.md) — including the 1.0.0 release candidates
