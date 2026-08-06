/**
 * The two scopes the gateway issues — `model.ScopeAdmin` and
 * `model.ScopeReadOnly` in internal/admin/model/apikey.go.
 *
 * Narrow rather than `string` so a typo in a comparison is a compile error
 * instead of a control that silently never renders. Anything the server sends
 * outside this pair is dropped on the way in (see `lib/api.ts`), which fails
 * closed: an unrecognised scope grants nothing.
 */
export type Scope = 'admin' | 'read_only'

/**
 * This browser's own login artifact.
 *
 * `subject` and `expires_at` come straight from POST /admin/session and are not
 * secrets — the token is. They are persisted beside it so a reload can still
 * name who is signed in without a round trip. Both are optional because a
 * session stored by an earlier build carries only the token and the scopes.
 */
export interface Session {
  token: string
  scopes: Scope[]
  /** Display name of the credential that was exchanged, e.g. "Ops laptop". */
  subject?: string
  expires_at?: string
}

export interface GatewayErrorEnvelope {
  error?: {
    message?: string
    type?: string
    code?: string
  }
}

export interface DashboardSummary {
  providers: {
    total: number
    available: number
  }
  keys: {
    total: number
    active: number
    expired: number
    total_usage: number
  }
  request_logs: {
    enabled: boolean
    total: number
  }
}

export interface APIKey {
  id: string
  key: string
  name: string
  scopes: Scope[]
  created_at: string
  revoked_at?: string
  expires_at?: string
  rotated_at?: string
  last_used_at?: string
  usage_count: number
  active: boolean
}

/**
 * One authenticated dashboard sign-in, as returned by GET /admin/sessions.
 *
 * Mirrors `admin.Session` in internal/admin/sessions.go field-for-field. Not
 * the same shape as `Session` above — that one is this browser's own login
 * artifact (token + scopes); this is the server's record of every operator
 * currently signed in, including this browser's.
 */
export interface DashboardSession {
  id: string
  credential_id: string
  subject: string
  scopes: Scope[]
  created_at: string
  last_seen_at?: string
  expires_at: string
}

export interface RequestLog {
  trace_id: string
  stage: string
  model: string
  provider: string
  /**
   * The opaque id of the credential the request was served under — never the
   * credential. Resolve it to a key name through GET /admin/keys; the id alone
   * names nobody.
   *
   * Empty when the row names no credential: an unauthenticated request, which
   * is every request on a gateway running without key auth, or a row written
   * before the gateway recorded this at all. Optional because the dashboard
   * deploys separately from the gateway and can be pointed at one that predates
   * the field.
   */
  api_key_id?: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  error_message: string
  created_at: string
}

/**
 * GET /admin/logs.
 *
 * `data` is one row per request by default — the terminal stage each request
 * reached. `?stage=all` returns every logged row instead, and a named stage
 * returns that stage alone. `total_entries` counts whatever the applied filter
 * matched, so it is the request count under the default.
 */
export interface LogsResponse {
  data: RequestLog[]
  summary: {
    total_entries: number
    returned_entries: number
  }
  filters: Record<string, string | number | string[]>
}

/**
 * One group within a stats dimension.
 *
 * `count` is events, not requests — the logger writes a row per plugin stage.
 * `tokens` is the more useful ranking for models: the model called most often
 * is usually not the one consuming the budget.
 */
export interface DimensionStat {
  count: number
  errors: number
  tokens: number
  /** Estimated spend. A floor: rows the catalog could not price add nothing. */
  cost_usd?: number
  /** How many of this group's rows the catalog could not price. */
  unpriced?: number
}

/**
 * A measured distribution, in milliseconds.
 *
 * Null on the wire when nothing was measured — a gateway that has recorded no
 * duration has no median, and a p50 of 0 would read as an instant response
 * rather than an absent measurement.
 */
export interface Percentiles {
  p50: number
  p95: number
  p99: number
  max: number
  mean: number
  count: number
}

export interface SeriesPoint {
  start: string
  /** Completed requests — terminal stages only, so this is not a row count. */
  requests: number
  errors: number
  prompt_tokens: number
  completion_tokens: number
}

export interface LogsSeries {
  points: SeriesPoint[]
  /**
   * The series covers only the most recent rows the server was willing to scan.
   * When set, `start`/`end` describe what was actually covered rather than the
   * requested range — plotting the requested one would render the uncovered
   * remainder as zero traffic, which reads as an outage.
   */
  truncated: boolean
  start?: string
  end?: string
}

export interface ErrorGroup {
  message: string
  count: number
}

/**
 * `top_errors` and `series` are optional because the dashboard ships and
 * deploys separately from the gateway — see web/README.md — so it can be
 * pointed at an older one that does not send them.
 */
export interface LogsStats {
  summary: {
    /**
     * Requests, not logged rows. Each request writes one row per plugin stage,
     * and this used to sum them: 19 requests were reported as 37. A caller
     * reading it as the request count was wrong by roughly 2x, and dividing
     * `error_entries` by it halved every failure rate.
     */
    total_entries: number
    error_entries: number
    total_tokens: number
    prompt_tokens?: number
    completion_tokens?: number
    /** Spend over rows the catalog could price — a floor, not a total. */
    cost_usd?: number
    /** Completed requests the catalog could not price. */
    unpriced_requests?: number
  }
  /** End-to-end request latency. Null when no row carried a duration. */
  latency_ms?: Percentiles | null
  /** Time to first token. Streaming only, so null on a gateway serving none. */
  ttft_ms?: Percentiles | null
  by_stage: Record<string, DimensionStat>
  by_provider: Record<string, DimensionStat>
  by_model: Record<string, DimensionStat>
  top_errors?: ErrorGroup[]
  series?: LogsSeries
}

export interface ModelInfo {
  id: string
  object: string
  created: number
  owned_by: string
}

export interface ProviderInfo {
  name: string
  models: ModelInfo[]
}

/**
 * One provider as reported by the authenticated GET /admin/health.
 *
 * Carries no circuit state: `healthCheck` in internal/admin/handlers/dashboard.go
 * builds this shape from the registry alone. Circuit state is on the two probe
 * endpoints instead — `GatewayHealth` and `Readiness` below.
 */
export interface ProviderHealth {
  name: string
  status: string
  models: number
  message?: string
}

/** Circuit-breaker state as the gateway spells it on the wire. */
export type CircuitState = 'closed' | 'open' | 'half-open'

/**
 * GET /health — unauthenticated, so it is the one provider view a page can read
 * before sign-in. `Health` in internal/handler/chat.go: status is "ok", or
 * "no_providers" with a 503 when nothing is registered.
 */
export interface GatewayHealth {
  status: string
  providers: Array<{
    name: string
    status: string
    circuit: CircuitState
    models: number
  }>
}

export interface ComponentHealth {
  name: string
  status: string
}

/**
 * One MCP server as GET /admin/health reports it — `MCPServerHealth` in
 * internal/admin/handlers/server.go.
 *
 * `last_error` is why the server is unready, and this authenticated route is
 * the only place it is served: /readyz withholds it because that endpoint is
 * unauthenticated and the reason can quote a server URL, an authorization
 * header, or a subprocess command line. The gateway redacts known credential
 * shapes out of it before sending, but that is best effort — treat the string
 * as sensitive and never route it anywhere but the signed-in dashboard.
 */
export interface MCPServerHealth {
  name: string
  ready: boolean
  required: boolean
  /** Absent when the server is ready. */
  last_error?: string
}

export interface AdminHealth {
  status: string
  providers: ProviderHealth[]
  scopes?: Scope[]
  components?: ComponentHealth[]
  /** Omitted entirely when no MCP server is configured. */
  mcp_servers?: MCPServerHealth[]
}

/**
 * One built-in provider as GET /admin/providers/catalog reports it —
 * `listProviderCatalog` in internal/admin/handlers/dashboard.go. Unlike
 * /admin/health, this lists EVERY built-in provider, registered or not, so the
 * Providers gallery can show the whole catalog. `catalog_models` is the count of
 * the provider's non-deprecated catalog models — available even with no
 * credential, which is what lets an unconfigured card still name a model count.
 */
export interface ProviderCatalogEntry {
  id: string
  registered: boolean
  catalog_models: number
}

export interface ProviderCatalogResponse {
  providers: ProviderCatalogEntry[]
}

/**
 * GET /readyz — `Readyz` in internal/handler/health.go.
 *
 * The two responses are different bodies, not one body with optional fields:
 * a 200 carries `providers` and, when any are configured, `mcp_servers`; a 503
 * carries `status` and `reason` and nothing else. So a page must not read
 * `providers` without checking `status` first — an unready gateway reports no
 * providers, which is not the same as having none.
 *
 * `reason` is one of a fixed set of generic strings ("store unreachable",
 * "no ready providers", "required mcp server unavailable", …). The underlying
 * error is deliberately withheld: the endpoint is unauthenticated and the real
 * message can quote a DSN, a server URL, or an authorization header.
 */
export interface Readiness {
  status: 'ready' | 'not_ready'
  /** Present only on a 503. */
  reason?: string
  /** Present only on a 200. */
  providers?: Array<{ name: string; circuit: CircuitState }>
  /**
   * Present only on a 200, and omitted entirely when no MCP server is
   * configured — so this list vanishes exactly when a required server is down
   * and the gateway answers 503. Read MCP state from `AdminHealth.mcp_servers`
   * instead, which is authenticated, always answers 200, and carries the
   * failure reason this endpoint has to withhold.
   */
  mcp_servers?: Array<{ name: string; ready: boolean; required: boolean }>
}

/**
 * GET /v1/capabilities — `Capabilities` in internal/handler/capabilities.go.
 *
 * Every registered provider appears with a full profile over
 * `capabilities.AllParams`, so an absent parameter means the gateway does not
 * know that parameter, not that the provider forwards it.
 */
export type ParamSupport = 'forward' | 'translate' | 'unsupported'

export interface Capabilities {
  providers: Record<string, Record<string, ParamSupport>>
}

/** GET /admin/plugins — `listPlugins` in internal/admin/handlers/dashboard.go. */
export interface PluginInfo {
  name: string
  type: string
  enabled: boolean
}

/**
 * One built-in plugin, as GET /admin/plugins/catalog describes it —
 * `plugin.Builtins()` in plugin/catalog.go.
 *
 * The dashboard used to carry this list itself, and four of its six entries
 * named a config key no plugin reads, in the panel that tells an operator to
 * add one of them to the configuration. Nothing here is derived on this side:
 * `settings` are the keys the plugin's Init reads, `type` is what it reports
 * from Type(), and `fails_open` is the policy the plugin manager applies.
 */
export interface PluginCatalogEntry {
  name: string
  type: string
  summary: string
  settings: string[]
  fails_open: boolean
}

export interface PluginCatalogResponse {
  data: PluginCatalogEntry[]
}

export interface ConfigHistoryEntry {
  version: number
  updated_at: string
  config: Record<string, unknown>
  rolled_back_from?: number
  /**
   * Who applied this version, from the durable `config_history` trail. Omitted
   * rather than blank on an entry that predates actor recording, so an empty
   * string is never mistaken for "nobody".
   */
  actor?: string
}

export interface ConfigHistoryResponse {
  data: ConfigHistoryEntry[]
  summary: { total_versions: number }
}

/** What became of an audited action — `model.AuditOutcome`. */
export type AuditOutcome = 'ok' | 'denied' | 'error'

/**
 * One recorded administrative action — `model.AuditEntry` in
 * internal/admin/model/audit.go, field-for-field.
 *
 * Nothing here is a secret, by construction on the server side: no key secret,
 * no key hash, no session token, no config value, no request body. `detail` is
 * passed through the redactor before storage. Render it as text — it is JSON
 * written by the server, not markup.
 */
export interface AuditEntry {
  occurred_at: string
  /** A dotted verb: "key.create", "config.rollback", "session.create". */
  action: string
  /** Display form, frozen at write time: "Ops laptop (key-7f2)". */
  actor: string
  /**
   * The credential id alone. `actor` carries a name that is neither stable nor
   * unique, so filtering one operator's history keys on this instead.
   */
  actor_id?: string
  /** A key id, a config version, or "*" for a fleet-wide action. */
  target_id?: string
  outcome: AuditOutcome
  /** Optional JSON describing the change. */
  detail?: string
  source_ip?: string
  trace_id?: string
}

/** GET /admin/audit — `listAudit` in internal/admin/handlers/audit_read.go. */
export interface AuditResponse {
  data: AuditEntry[]
  summary: {
    /** Rows matching the filters, ignoring limit and offset. */
    total_entries: number
    returned_entries: number
  }
  filters: Record<string, string | number>
}

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant'
  content: string
}

export interface ChatStreamChunk {
  choices?: Array<{ delta?: { content?: string } }>
  usage?: {
    prompt_tokens?: number
    completion_tokens?: number
  }
}

/**
 * Token consumption — `core.Usage`.
 *
 * The three extended counters are `omitempty` on the wire: only providers that
 * report them send them, so absent means "not reported", not zero.
 */
export interface ChatUsage {
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  reasoning_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
}

/**
 * POST /v1/chat/completions with `stream: false` — `core.Response`.
 *
 * `provider` and `provider_metadata` are gateway additions, not OpenAI fields:
 * the first names which target actually served the request, which is the answer
 * a routing strategy makes non-obvious.
 */
export interface ChatCompletionResponse {
  id: string
  object?: string
  created?: number
  model: string
  provider?: string
  choices: Array<{
    index: number
    message: ChatMessage
    finish_reason: string
  }>
  usage: ChatUsage
  provider_metadata?: Record<string, unknown>
}

/** POST /v1/embeddings — `core.EmbeddingResponse`. */
export interface EmbeddingsResponse {
  object: string
  data: Array<{
    object: string
    embedding: number[]
    index: number
  }>
  model: string
  usage: {
    prompt_tokens: number
    total_tokens: number
  }
}

/**
 * POST /v1/images/generations — `core.ImageResponse`.
 *
 * `url` and `b64_json` are alternatives chosen by the request's
 * `response_format`; exactly one of them is populated per image.
 */
export interface ImageGenerationResponse {
  created: number
  data: Array<{
    url?: string
    b64_json?: string
    revised_prompt?: string
  }>
}
