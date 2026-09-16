# Plugins

Plugins are middleware that run around each request the gateway routes. They can
inspect or rewrite a request before it reaches a provider, screen or record the
response after, and note failures — enough to build guardrails, caching, rate
limiting, budgets, and logging without touching the routing core. Eleven ship
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

A plugin that can only act at some stages is **refused at the others**, by
`ferrogw validate` and at startup. `pii-redact` and `prompt-shield` screen the
prompt, so they run at `before_request`; `schema-guard` validates the answer, so
it runs at `after_request`. Listing one anywhere else used to load a plugin that
reported itself enabled and did nothing. `regex-guard`, `secret-scan`,
`word-filter` and the multi-stage plugins above are unaffected: they act at
every stage they name.

## Built-in plugins

| Plugin | Type | Stage(s) | What it does |
|---|---|---|---|
| **word-filter** | guardrail | before_request (and after_request to screen the response) | Rejects a request whose text contains a blocked entry as a substring. |
| **regex-guard** | guardrail | before_request (and after_request to screen the response) | Rejects or flags content matching named regular expressions, per rule's `apply_to`. |
| **pii-redact** | guardrail | before_request | Detects personally identifiable information and either denies the request, rewrites it in place with the values replaced by a placeholder, or only records the detection. Rewriting applies to the chat-shaped surfaces; elsewhere a detection under `redact` is denied. |
| **secret-scan** | guardrail | before_request (and after_request to screen the response) | Detects content carrying credentials — cloud keys, tokens, private keys — and applies the configured action; only `block` rejects. The response is screened only when this plugin is **also** listed at `after_request`. |
| **prompt-shield** | guardrail | before_request | Detects prompt-injection and jailbreak attempts, matched by category over common written forms, and applies the configured action; only `block` rejects. |
| **schema-guard** | guardrail | after_request | Validates the model's response against a JSON Schema subset — `type`, `required`, `properties`. |
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

### regex-guard

Screens content against named regular expressions, each independently scoped
to the request, the response, or both via `apply_to`. Patterns compile once at
load — an uncompilable pattern fails the load rather than silently matching
nothing.

A rule's `apply_to` and the plugin entry's `stage` are two separate settings
and both must agree: one `plugins[]` entry registers one stage, so a rule with
`apply_to: output` or `both` only fires when this plugin is **also** listed at
`after_request`. An entry whose rules cannot act at its stage — every rule
`output` under `before_request`, or every rule `input` under `after_request` —
fails the load rather than registering a plugin that screens nothing. An
unrecognised `apply_to` fails the load rather than defaulting to `input` —
screening the prompt is not the rule that was written. `rules` is required and
must name at least one rule: this plugin has no built-in patterns, so without
one it would load and screen nothing.

```yaml
config:
  action: block          # default action for a rule that omits one
  rules:
    - name: ssn
      pattern: '\d{3}-\d{2}-\d{4}'
      apply_to: input     # input (default) | output | both
      action: block        # block | warn | log (only block rejects)
```

### pii-redact

Detects personally identifiable information (email, phone, SSN, credit card, or
custom regex patterns) and either denies the request (`action: block`), or, the
only built-in guardrail that can, rewrites it in place and lets it continue
(`action: redact`), or records the detection by entity type and forwards the
request as written (`action: log`, the observe-only mode for sizing a policy
before enforcing it). Redaction rewrites every screenable field — a message's
`Content`, its reasoning content, each of its content parts and each tool
call's arguments — so none of them can carry the value past the plugin. A
`credit_card` match must also pass the Luhn check, so a sixteen-digit order id
or tracking number is not denied as a card.

**`redact` takes effect on the chat-shaped surfaces** — `/v1/chat/completions`
(streamed or not) and `/v1/completions` — where the gateway reads the rewritten
request back before routing it. Every other surface (`/v1/embeddings`,
`/v1/images/generations`, `/v1/responses`, `/v1/rerank`, `/v1/moderations`,
`/v1/audio/*` and the `/v1/*` pass-through) forwards its own body unchanged,
so a rewrite there would be discarded: on those a detection is **denied**
instead, with a reason saying the content could not be sanitized on that
surface. `action: block` and `action: log` behave identically on every surface.

`piiredact.Detect(text)` is the same detection as a pure function: it runs
every built-in entity and returns the names that matched, sorted, and never the
matched text. It exists so a policy layer embedding the gateway can ask the
same question with the same patterns instead of carrying a copy that drifts.
Custom `patterns` are configuration and are not part of it.

```yaml
config:
  action: redact              # block | redact | log (log records and forwards)
  entities: ["email", "ssn"]  # optional; default is every built-in entity.
                               # An unrecognised name fails the load, and so
                               # does an empty list with no `patterns` to fall
                               # back on
  patterns: ['\bACME-\d{6}\b'] # optional custom regexes, compiled at load. An
                               # empty entry fails the load: it would match
                               # every request.
                               # `entities: []` alongside these screens your
                               # patterns and none of the built-ins
  redact_placeholder: "[REDACTED]" # literal text, inserted exactly as written —
                               # a `$` in it is a dollar sign, not a reference
                               # to the matched value
```

### secret-scan

Screens for credentials — cloud keys, tokens, private keys — using a curated
set of structural patterns (a fixed prefix and length) rather than an
entropy heuristic, because a false positive here costs a developer their
request. It screens the response as well as the request: a model asked to
"show me the config" will read a credential back out of its context, and a
guardrail that only watched the prompt would miss it. The matched kind (e.g.
`aws_access_key`) is logged and reported; the credential itself never is. The
curated kinds are `aws_access_key`, `github_token`, `slack_token`,
`anthropic_key`, `openai_key`, `google_api_key`, `stripe_key`, `private_key`,
`jwt`, `gitlab_token`, `npm_token`, `huggingface_token`, `sendgrid_key`,
`twilio_key`, `azure_storage_key` and `slack_webhook`; an Anthropic key is
reported under its own kind even though it also fits the OpenAI shape, so a
`kinds` selection covers exactly the vendors it names.

**Response screening needs its own `plugins[]` entry.** One entry registers one
stage, so a `before_request`-only entry screens the prompt and nothing else —
list this plugin at `after_request` as well to screen what the model reads back
out, with identical `config` on both entries.

Only `action: block` rejects. Under `warn` or `log` a detected credential is
recorded by kind and the content is **forwarded anyway** — that is the point of
those actions, and it means `warn` is an observation mode, not enforcement.

```yaml
config:
  action: block                # block | warn | log (only block rejects)
  kinds: ["aws_access_key", "private_key"]  # optional; default is every curated
                                             # kind. An unrecognised name fails
                                             # the load, and so does an empty
                                             # list with no `patterns` to fall
                                             # back on
  patterns: ['\bACME-KEY-[0-9]{8}\b']        # optional custom regexes, compiled at load.
                                             # An empty entry fails the load: it would
                                             # match every request.
                                             # `kinds: []` alongside these scans for
                                             # your patterns and no curated kind
```

### prompt-shield

Detects prompt-injection and jailbreak attempts, matched by
category (`system_override`, `role_manipulation`, `instruction_leak`,
`delimiter_attack`) over the common written forms. Screens the request only:
by `after_request` the model has already acted on an injection, and on a
streamed response the tokens have already been delivered, so there is nothing
left to withhold there. These are heuristics over phrasing, not a classifier —
they catch the common written forms and will miss an attacker who paraphrases.
Pair this with an external provider for adversarial traffic; it is one layer,
not the whole defence. The denial reason names the category, never the
matched phrase — quoting the match would let an attacker binary-search the
pattern.

Only `action: block` rejects. Under `warn` or `log` a detected attempt is
recorded by category and the prompt **reaches the model anyway** — those
actions are for measuring a pattern's false-positive rate before enforcing it.

`promptshield.Detect(text)` is the same detection as a pure function: it runs
every category and returns the names that matched, sorted, and never the
matched phrase. It carries the same ceiling as the plugin — pattern matches
over common written forms, not a classifier — and exists so a policy layer
embedding the gateway can ask the same question with the same patterns.

```yaml
config:
  action: block       # block | warn | log (only block rejects)
  categories: ["system_override", "instruction_leak"]  # optional; default is every
                                                        # category. An unrecognised
                                                        # name fails the load, and an
                                                        # empty list is refused
```

### schema-guard

Validates the model's response against a JSON Schema subset — `type`,
`required`, `properties` — rather than a full JSON Schema dependency, which
would be a large addition for the question this answers: did the model return
the object shape the caller is about to unmarshal. An unsupported keyword is
ignored, so a schema copied in from elsewhere still validates the part this
plugin understands. A **supported** keyword written wrong is a load error
instead — `required: "name"` rather than `required: ["name"]`, a `type` that
names no JSON Schema type, a property whose subschema is not an object — because
an ignored `required` is a plugin that starts and approves everything. `type: integer` is a whole number — JSON carries one number
type, so `42` satisfies it and `42.5` does not, while `type: number` accepts
both. Runs at `after_request` only: on a streamed response the
tokens are already delivered, so it can report a violation but not unsend it.
The denial reason names the field and what was expected, since a schema
violation is not adversarial and naming it is the whole diagnostic value.

Each choice is assembled into **one document** and validated once, so an answer
split across content parts is judged whole rather than as several invalid
fragments, and `n > 1` has every candidate validated. A choice carrying
**neither content nor a tool call** is a violation: an empty answer does not
satisfy a schema requiring an object. A choice carrying **only a tool call**
passes without validation — a tool call is a different kind of answer, not a
malformed one, so this plugin is safe to run on a gateway serving structured
output and tool calling at once.

```yaml
config:
  action: block   # block | warn | log
  schema:
    type: object
    required: ["name", "score"]
    properties:
      name:
        type: string
      score:
        type: number
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

`scripts/plugin_smoke.sh` exercises word-filter, max-token, response-cache,
request-logger, budget and rate-limit end-to-end against a live provider and
prints a PASS/FAIL summary. The content guardrails are covered by their unit
suites; the script names the plugins it runs in its own header.
