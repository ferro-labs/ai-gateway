import AxeBuilder from '@axe-core/playwright'
import { expect, type Page, type Route } from '@playwright/test'

export const adminToken = 'test-admin-key'
export const readOnlyToken = 'test-read-key'
// The token /admin/session mints, deliberately distinct from every credential
// above. Echoing the typed credential back would let login() skip the
// exchange and store the raw credential without failing any assertion here.
export const sessionToken = 'fgws_e2e'
export const timestamp = '2026-07-21T12:30:00Z'

/**
 * The instant this fixture calls "now", and the instant `mockGateway` pins the
 * browser's clock to.
 *
 * Everything else here was already anchored here — `timestamp`, the keys'
 * `last_used_at`, and the traffic series, whose twelve buckets end at exactly
 * this moment. The audit trail and the request log were the two exceptions:
 * they walked back from the real `Date.now()`, so the strings the browser
 * rendered from them were different on every run.
 *
 * That is not merely cosmetic, and masking the cell does not cover it. The
 * tables lay out on content width, so a timestamp one character wider — a
 * single-digit day, a longer month, "5h ago" against "12m ago" — moves every
 * other column, and the audit baseline failed on 3% of its pixels with nothing
 * about the page actually changed.
 *
 * Pinning the data alone would not have been enough either: the audit page asks
 * for "the last 24 hours" and the fixture honours it, so fixed instants would
 * have aged out of the window and emptied the table. Pinning the browser's
 * clock to the same instant is what lets both hold at once — the window is
 * computed from this moment, and the rows sit inside it forever.
 */
const NOW = Date.parse(timestamp)

/**
 * A fixed twelve-hour traffic series, so the charts render the same pixels on
 * every run. Bucket four fails, which keeps a failure band in the baseline —
 * an all-successful series would let a regression in the failure band ship
 * invisibly.
 */
const SERIES_START = Date.parse('2026-07-21T00:30:00Z')
const SERIES_BUCKET_MS = 60 * 60 * 1000
const statsSeries = Array.from({ length: 12 }, (_, index) => ({
  start: new Date(SERIES_START + index * SERIES_BUCKET_MS).toISOString(),
  requests: 900 + index * 140,
  errors: index === 4 ? 46 : 0,
  prompt_tokens: 96000 + index * 9000,
  completion_tokens: 41000 + index * 3800,
}))
const seriesEnd = new Date(SERIES_START + 12 * SERIES_BUCKET_MS).toISOString()

/**
 * GET /admin/plugins/catalog, mirroring `plugin.Builtins()` in
 * plugin/catalog.go.
 *
 * Copied rather than invented: `settings` must be the keys each plugin's Init
 * really reads, which plugin/catalog_test.go holds the Go side to. The
 * dashboard used to carry a list of its own here and four of its six entries
 * named a key no plugin reads.
 */
const pluginCatalog = [
  {
    name: 'word-filter',
    type: 'guardrail',
    summary: 'Rejects a request whose prompt contains any blocked word.',
    settings: ['blocked_words', 'case_sensitive'],
    fails_open: false,
  },
  {
    name: 'max-token',
    type: 'guardrail',
    summary: 'Rejects a request over a token, message-count, or input-length ceiling before it reaches a provider.',
    settings: ['max_tokens', 'max_messages', 'max_input_length'],
    fails_open: false,
  },
  {
    name: 'rate-limit',
    type: 'ratelimit',
    summary: 'Bounds request rate globally and per API key or user, independently of the per-IP HTTP limiter.',
    settings: ['requests_per_second', 'burst', 'key_rpm', 'user_rpm'],
    fails_open: false,
  },
  {
    name: 'budget',
    type: 'ratelimit',
    summary: 'Tracks estimated spend per API key and refuses requests once the configured budget is exhausted.',
    settings: ['spend_limit_usd', 'input_per_m_tokens', 'output_per_m_tokens', 'max_keys', 'store_id'],
    fails_open: false,
  },
  {
    name: 'response-cache',
    type: 'transform',
    summary: 'Serves an identical request from memory instead of calling a provider again.',
    settings: ['max_age', 'max_entries'],
    fails_open: false,
  },
  {
    name: 'request-logger',
    type: 'logging',
    summary: 'Records each request for the Request Logs page, and persists it when a request-log store is configured.',
    settings: ['level', 'persist'],
    fails_open: true,
  },
]

const traffic = [
  { model: 'gpt-4.1-mini', provider: 'openai' },
  { model: 'claude-sonnet-4-5', provider: 'anthropic' },
  { model: 'gemini-2.5-flash', provider: 'gemini' },
]

export const logs = Array.from({ length: 24 }, (_, hour) =>
  Array.from({ length: (hour % 6) + 1 }, (_, offset) => {
    const item = traffic[(hour + offset) % traffic.length]
    const failed = (hour + offset) % 13 === 0
    return {
      trace_id: 'trace-' + hour + '-' + offset,
      stage: failed ? 'on_error' : 'after_request',
      model: item.model,
      provider: item.provider,
      prompt_tokens: 90 + hour * 4,
      completion_tokens: failed ? 0 : 36 + offset * 9,
      total_tokens: 90 + hour * 4 + (failed ? 0 : 36 + offset * 9),
      error_message: failed ? 'upstream request timed out' : '',
      created_at: new Date(NOW - hour * 3_600_000 - offset * 60_000).toISOString(),
    }
  }),
).flat()

/**
 * The audit trail, walking back from `NOW` — the fixed instant `mockGateway`
 * also pins the browser's clock to, so the page's "since 24 hours ago" window
 * is computed from it and these rows never age out of it.
 *
 * Long enough to page (63 rows against a page size of 50), and deliberately not
 * uniform: a denied row is a refusal the gateway enforced and must not be shown
 * in the error tone, so the trail carries both kinds and neither is a rounding
 * error.
 */
const AUDIT_ACTIONS = [
  'key.create', 'key.rotate', 'key.revoke', 'key.delete',
  'config.update', 'config.rollback', 'session.create', 'logs.purge',
] as const

const AUDIT_ACTORS = [
  { actor: 'Production application (key-production)', actor_id: 'key-production' },
  { actor: 'Operations observer (key-observer)', actor_id: 'key-observer' },
] as const

export interface FixtureAuditEntry {
  occurred_at: string
  action: string
  actor: string
  actor_id: string
  target_id: string
  outcome: 'ok' | 'denied' | 'error'
  detail?: string
  source_ip: string
  trace_id: string
}

export const auditEntries: FixtureAuditEntry[] = Array.from({ length: 63 }, (_, index) => {
  const action = AUDIT_ACTIONS[index % AUDIT_ACTIONS.length]
  const who = AUDIT_ACTORS[index % AUDIT_ACTORS.length]
  const denied = index % 9 === 4
  return {
    occurred_at: new Date(NOW - index * 5 * 60_000).toISOString(),
    action,
    actor: who.actor,
    actor_id: who.actor_id,
    target_id: action.startsWith('config.') ? `v${(index % 3) + 1}` : 'key-observer',
    outcome: denied ? 'denied' : index % 17 === 3 ? 'error' : 'ok',
    detail: denied
      ? JSON.stringify({ refused: 'cannot remove the last admin key', target: 'key-locked' })
      : undefined,
    source_ip: `10.0.0.${(index % 5) + 1}`,
    trace_id: `trace-audit-${index}`,
  }
})

/**
 * Every OpenAI chat parameter the gateway models — `capabilities.AllParams`.
 *
 * GET /v1/capabilities materialises a *full* profile over this set for every
 * registered provider, so the fixture does too: a profile that listed only the
 * exceptions would let a page that reads an absent parameter as "unsupported"
 * pass here and invert the matrix in production.
 */
const CHAT_PARAMS = [
  'temperature', 'top_p', 'n', 'seed', 'max_tokens', 'max_completion_tokens',
  'presence_penalty', 'frequency_penalty', 'stop', 'tools', 'tool_choice',
  'response_format', 'logprobs', 'top_logprobs', 'stream', 'stream_options',
  'parallel_tool_calls', 'user', 'logit_bias',
] as const

type ParamSupport = 'forward' | 'translate' | 'unsupported'

const CAPABILITY_EXCEPTIONS: Record<string, Record<string, ParamSupport>> = {
  openai: {},
  anthropic: {
    n: 'unsupported',
    seed: 'unsupported',
    logprobs: 'unsupported',
    top_logprobs: 'unsupported',
    logit_bias: 'unsupported',
    response_format: 'translate',
  },
  gemini: {
    logprobs: 'unsupported',
    logit_bias: 'unsupported',
    presence_penalty: 'unsupported',
    frequency_penalty: 'unsupported',
    user: 'unsupported',
    response_format: 'translate',
  },
}

export const capabilities = {
  providers: Object.fromEntries(
    Object.entries(CAPABILITY_EXCEPTIONS).map(([name, profile]) => [
      name,
      Object.fromEntries(CHAT_PARAMS.map((param) => [param, profile[param] ?? 'forward'])),
    ]),
  ),
}

/** How many parameters the matrix has a column for. */
export const capabilityParamCount = CHAT_PARAMS.length

export const providers = [
  { name: 'openai', models: [
    { id: 'gpt-4.1-mini', object: 'model', created: 0, owned_by: 'openai' },
    { id: 'gpt-4.1', object: 'model', created: 0, owned_by: 'openai' },
  ] },
  { name: 'anthropic', models: [
    { id: 'claude-sonnet-4-5', object: 'model', created: 0, owned_by: 'anthropic' },
  ] },
  { name: 'gemini', models: [
    { id: 'gemini-2.5-flash', object: 'model', created: 0, owned_by: 'google' },
  ] },
]

/**
 * GET /health — `Health` in internal/handler/chat.go. Unauthenticated, and the
 * only endpoint reporting per-provider circuit state, which the Providers page
 * reads for its circuit column.
 */
export function gatewayHealth() {
  return {
    status: 'ok',
    providers: providers.map((provider) => ({
      name: provider.name,
      status: 'available',
      circuit: 'closed',
      models: provider.models.length,
    })),
  }
}

export function health(scopes: string[]) {
  return {
    status: 'healthy',
    scopes,
    providers: providers.map((provider) => ({
      name: provider.name,
      status: 'available',
      models: provider.models.length,
    })),
    components: [
      { name: 'API', status: 'healthy' },
      { name: 'Key store', status: 'healthy' },
      { name: 'Config store', status: 'healthy' },
      { name: 'Request logs', status: 'healthy' },
    ],
  }
}

export async function respond(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:4173' },
    json: body,
    status,
  })
}

/** A 204, which the gateway answers every successful destructive call with. */
async function noContent(route: Route) {
  await route.fulfill({ headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:4173' }, status: 204 })
}

/**
 * The id every mutation below refuses, standing in for the real handler's two
 * 409s — revoking or deleting the credential this request authenticated with,
 * and removing the last admin key — without reproducing that guard logic here.
 *
 * Rotation is deliberately not among them: the gateway leaves rotate unguarded
 * because a rotated key keeps its id and its scopes, so nobody is locked out.
 */
/**
 * Every path prefix the preview server proxies to the gateway — `gatewayProxy`
 * in vite.config.ts.
 *
 * This is the set the fixture must answer, and it is wider than the set it used
 * to check. A gateway path not listed here is not intercepted, so it leaves the
 * browser for 127.0.0.1:8080, which nothing is listening on: the proxy answers
 * 500 and the test fails on a console error that names neither the path nor
 * this list. `/health` was exactly that — the Providers page reads it for the
 * circuit column, and it escaped a check that looked only for `/admin/`, `/v1/`
 * and `/readyz`.
 *
 * Prefix rather than exact match, because `/admin` and `/v1` cover whole trees
 * while `/health`, `/livez` and `/readyz` are single endpoints.
 */
const GATEWAY_PATH_PREFIXES = ['/admin', '/health', '/livez', '/readyz', '/v1']

function isGatewayPath(path: string): boolean {
  return GATEWAY_PATH_PREFIXES.some((prefix) => path === prefix || path.startsWith(`${prefix}/`))
}

const LOCKED_KEY_ID = 'key-locked'
const lastAdminKeyError = {
  error: { message: 'cannot remove the last admin key: create another admin key first', code: 'last_admin_key' },
}

interface FixtureKey {
  id: string
  key: string
  name: string
  scopes: string[]
  created_at: string
  expires_at?: string
  last_used_at?: string
  rotated_at?: string
  revoked_at?: string
  usage_count: number
  active: boolean
}

interface FixtureSession {
  id: string
  credential_id: string
  subject: string
  scopes: string[]
  created_at: string
  last_seen_at?: string
  expires_at: string
}

export async function mockGateway(
  page: Page,
  scopes: string[],
  credential: string,
  options: { streamFailure?: boolean; auditDisabled?: boolean } = {},
) {
  // Before the first navigation, so every page this fixture serves reads the
  // same "now" its data was built from. Timers keep running — only the clock is
  // fixed — so the app renders, polls and animates exactly as it otherwise would.
  await page.clock.setFixedTime(NOW)
  const streamFailure = options.streamFailure ?? false
  // A gateway running without an audit store — a deployment choice the trail
  // has to explain rather than report as a failure.
  const auditDisabled = options.auditDisabled ?? false
  let keys: FixtureKey[] = [
    { id: 'key-production', key: 'fgw_live_abcd1234', name: 'Production application', scopes: ['admin'], created_at: '2026-07-01T09:00:00Z', last_used_at: '2026-07-21T12:25:00Z', usage_count: 12843, active: true },
    { id: 'key-observer', key: 'fgw_live_efgh5678', name: 'Operations observer', scopes: ['read_only'], created_at: '2026-07-10T09:00:00Z', usage_count: 892, active: true },
    // A fixed id that `PUT /admin/keys/:id` always refuses below, standing in
    // for the real handler's two 409s (own-credential expiry, last admin key)
    // without needing to reproduce that guard logic in a browser fixture.
    { id: 'key-locked', key: 'fgw_live_ijkl9012', name: 'Locked credential', scopes: ['admin'], created_at: '2026-06-01T09:00:00Z', usage_count: 4, active: true },
  ]
  let sessions: FixtureSession[] = [
    { id: 'session-current', credential_id: 'key-production', subject: 'Production application', scopes: ['admin'], created_at: '2026-07-21T12:30:00Z', expires_at: '2026-07-22T12:30:00Z' },
    { id: 'session-observer', credential_id: 'key-observer', subject: 'Operations observer', scopes: ['read_only'], created_at: '2026-07-20T08:00:00Z', last_seen_at: '2026-07-21T11:00:00Z', expires_at: '2026-07-21T20:00:00Z' },
  ]
  /*
   * The active configuration document and the trail behind it.
   *
   * Mutable, because a save and a rollback are only worth testing if the next
   * read shows what they did: a fixture that answers GET from a constant would
   * let a PUT that never left the browser pass every assertion.
   *
   * Version 1 names a provider this gateway does not have, which is what makes
   * rolling back to it fail the way the real endpoint fails — validation, on
   * apply, after the operator has already confirmed.
   */
  let configDoc: Record<string, unknown> = {
    apiVersion: 'ferro.dev/v1',
    strategy: { mode: 'fallback' },
    targets: [{ virtual_key: 'openai' }, { virtual_key: 'anthropic' }],
  }
  let configHistory: Array<{ version: number; updated_at: string; actor?: string; rolled_back_from?: number; config: Record<string, unknown> }> = [
    { version: 1, updated_at: '2026-07-20T08:00:00Z', actor: 'Production application (key-production)', config: { strategy: { mode: 'single' }, targets: [{ virtual_key: 'openai-legacy' }] } },
    { version: 2, updated_at: timestamp, actor: 'Production application (key-production)', config: { strategy: { mode: 'fallback' } } },
  ]
  const recordConfigVersion = (config: Record<string, unknown>, rolledBackFrom?: number) => {
    configHistory = [
      ...configHistory,
      {
        version: configHistory.length + 1,
        updated_at: timestamp,
        actor: 'Production application (key-production)',
        rolled_back_from: rolledBackFrom,
        config,
      },
    ]
  }

  // Flips once "sign out all" succeeds, so every admin call issued afterward
  // is answered the way the real gateway would: the session row backing it
  // is gone. `/admin/session` (singular) stays exempt — nothing in this
  // fixture's flows calls it after a sign-out-all, and keeping the exemption
  // narrow avoids masking an unrelated bug in some other test.
  let sessionsRevoked = false

  // Gateway paths this fixture was asked for and does not answer. Asserted
  // empty on every one, so a call the application adds fails here, naming the
  // path — rather than leaving the preview server to proxy it at a gateway that
  // is not running and surfacing as an unattributable console 500.
  const unmocked: string[] = []

  await page.route('**/*', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    if (!isGatewayPath(path)) {
      await route.continue()
      return
    }
    if (request.method() === 'OPTIONS') {
      await route.fulfill({
        headers: {
          'Access-Control-Allow-Headers': 'Authorization, Content-Type',
          'Access-Control-Allow-Methods': 'GET, POST, PUT, DELETE, OPTIONS',
          'Access-Control-Allow-Origin': 'http://127.0.0.1:4173',
        },
        status: 204,
      })
      return
    }
    if (path === '/admin/session' && request.method() === 'POST') {
      // The exchange request itself is the one call still authenticated with
      // the raw typed credential — there is no session token yet to send.
      expect.soft(request.headers().authorization).toBe('Bearer ' + credential)
    } else if (path.startsWith('/admin/') || path.startsWith('/v1/')) {
      // Every request after sign-in must carry the minted session token, not
      // the credential the operator typed in. This is the end-to-end lock-in:
      // a login() that reverted to storing the raw credential would send
      // `Bearer ${credential}` here and fail this assertion.
      expect.soft(request.headers().authorization).toBe('Bearer ' + sessionToken)
    }

    if (sessionsRevoked && path !== '/admin/session') {
      return respond(route, { error: { message: 'session not found', code: 'session_expired' } }, 401)
    }

    if (path === '/admin/session') {
      if (request.method() === 'DELETE') {
        await route.fulfill({ headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:4173' }, status: 204 })
        return
      }
      return respond(route, { token: sessionToken, subject: 'fixture', scopes, expires_at: '2026-07-22T12:30:00Z' }, 201)
    }
    if (path === '/admin/health') return respond(route, health(scopes))
    if (path === '/health') return respond(route, gatewayHealth())
    if (path === '/admin/dashboard') {
      return respond(route, {
        providers: { total: providers.length, available: providers.length },
        keys: { total: keys.length, active: 2, expired: 0, total_usage: 13735 },
        request_logs: { enabled: true, total: 18432 },
      })
    }
    if (path === '/readyz') return respond(route, { status: 'ready', providers: [], mcp_servers: [] })
    if (path === '/admin/logs/stats') {
      // The logger writes one row per plugin stage, so total_entries is roughly
      // double the request count: 18,432 requests produce a before_request row
      // each, plus one terminal row. An earlier fixture omitted before_request,
      // which made total_entries coincidentally equal the request count and hid
      // the doubling from every test.
      return respond(route, {
        summary: {
          total_entries: 36864,
          error_entries: 46,
          total_tokens: 2874300,
          prompt_tokens: 2011010,
          completion_tokens: 863290,
          cost_usd: 41.8734,
          unpriced_requests: 118,
        },
        latency_ms: { p50: 412, p95: 1840, p99: 3120, max: 8801, mean: 596.4, count: 18432 },
        ttft_ms: { p50: 188, p95: 640, p99: 1210, max: 2400, mean: 241.7, count: 9120 },
        by_stage: {
          before_request: { count: 18432, errors: 0, tokens: 0, cost_usd: 0, unpriced: 0 },
          after_request: { count: 18386, errors: 0, tokens: 2874300, cost_usd: 41.8734, unpriced: 118 },
          on_error: { count: 46, errors: 46, tokens: 0, cost_usd: 0, unpriced: 46 },
        },
        by_provider: {
          openai: { count: 9210, errors: 30, tokens: 1431200, cost_usd: 1.2936, unpriced: 0 },
          anthropic: { count: 6042, errors: 12, tokens: 981400, cost_usd: 38.4102, unpriced: 0 },
          gemini: { count: 3180, errors: 4, tokens: 461700, cost_usd: 2.1696, unpriced: 118 },
        },
        // Deliberately not ranked the same way by count and by tokens: the
        // busiest model here is not the one consuming the budget, which is the
        // distinction the Analytics page exists to make.
        by_model: {
          'gpt-4.1-mini': { count: 9210, errors: 30, tokens: 431200, cost_usd: 1.2936, unpriced: 0 },
          'claude-sonnet-4-5': { count: 6042, errors: 12, tokens: 1881400, cost_usd: 38.4102, unpriced: 0 },
          'gemini-2.5-flash': { count: 3180, errors: 4, tokens: 561700, cost_usd: 2.1696, unpriced: 118 },
        },
        top_errors: [
          { message: 'upstream provider returned 429', count: 31 },
          { message: 'context length exceeded', count: 15 },
        ],
        series: {
          points: statsSeries,
          truncated: false,
          start: statsSeries[0].start,
          end: seriesEnd,
        },
      })
    }
    if (path === '/admin/logs') {
      return respond(route, request.method() === 'DELETE'
        ? { deleted: 24 }
        : { data: logs, summary: { total_entries: 18432, returned_entries: logs.length }, filters: {} })
    }
    if (path === '/admin/providers') return respond(route, providers)
    if (path === '/admin/audit') {
      if (auditDisabled) {
        return respond(route, { error: { message: 'audit trail storage is not enabled', code: 'not_implemented' } }, 501)
      }
      const action = url.searchParams.get('action') ?? ''
      const actorId = url.searchParams.get('actor_id') ?? ''
      const outcome = url.searchParams.get('outcome') ?? ''
      const since = url.searchParams.get('since')
      const sinceMs = since ? Date.parse(since) : 0
      // Action and actor id are matched exactly, as the gateway matches them:
      // a fixture doing a substring match would hide the empty page a near miss
      // really produces, which is the whole reason the page explains itself.
      const matched = auditEntries.filter((entry) =>
        Date.parse(entry.occurred_at) >= sinceMs
        && (action === '' || entry.action === action)
        && (actorId === '' || entry.actor_id === actorId)
        && (outcome === '' || entry.outcome === outcome))
      const offset = Number(url.searchParams.get('offset') ?? '0')
      const limit = Number(url.searchParams.get('limit') ?? '50')
      const pageRows = matched.slice(offset, offset + limit)
      return respond(route, {
        data: pageRows,
        summary: { total_entries: matched.length, returned_entries: pageRows.length },
        filters: {},
      })
    }
    if (path === '/admin/keys/usage') {
      const active = url.searchParams.get('active') ?? ''
      const filtered = active === '' ? keys : keys.filter((key) => key.active === (active === 'true'))
      const usedAt = (key: FixtureKey) => (key.last_used_at ? Date.parse(key.last_used_at) : 0)
      const ordered = [...filtered].sort((left, right) => url.searchParams.get('sort') === 'last_used'
        ? usedAt(right) - usedAt(left)
        : right.usage_count - left.usage_count)
      const offset = Number(url.searchParams.get('offset') ?? '0')
      const limit = Number(url.searchParams.get('limit') ?? '20')
      const pageRows = ordered.slice(offset, offset + limit)
      // The summary describes the *filtered* set, not the store — with
      // `active=false` applied, total_keys is how many revoked keys exist.
      return respond(route, {
        data: pageRows,
        summary: {
          total_keys: filtered.length,
          active_keys: filtered.filter((key) => key.active).length,
          total_usage: filtered.reduce((sum, key) => sum + key.usage_count, 0),
          returned_keys: pageRows.length,
        },
      })
    }
    if (path === '/admin/keys') {
      if (request.method() === 'POST') {
        const created = { id: 'key-new', key: 'fgw_live_newsecretvalue', name: 'Browser validation', scopes: ['admin'], created_at: timestamp, usage_count: 0, active: true }
        keys = [created, ...keys]
        return respond(route, created, 201)
      }
      return respond(route, keys)
    }
    if (path.startsWith('/admin/keys/') && request.method() === 'POST') {
      const [rawId, operation] = path.slice('/admin/keys/'.length).split('/')
      const id = decodeURIComponent(rawId)
      const index = keys.findIndex((key) => key.id === id)
      if (index === -1) return respond(route, { error: { message: 'key not found' } }, 404)
      if (operation === 'rotate') {
        // The id and the scopes survive; only the secret changes. A rotate that
        // returned a new id would let a page that keys its rows on the wrong
        // field pass here and lose the row in production.
        const rotated: FixtureKey = { ...keys[index], key: 'fgw_live_rotatedsecret', rotated_at: timestamp }
        keys = keys.map((key, position) => (position === index ? rotated : key))
        return respond(route, rotated)
      }
      if (operation === 'revoke') {
        if (id === LOCKED_KEY_ID) return respond(route, lastAdminKeyError, 409)
        keys = keys.map((key, position) => (position === index ? { ...key, active: false, revoked_at: timestamp } : key))
        return respond(route, { status: 'revoked' })
      }
    }
    if (path.startsWith('/admin/keys/') && request.method() === 'DELETE') {
      const id = decodeURIComponent(path.slice('/admin/keys/'.length))
      if (id === LOCKED_KEY_ID) return respond(route, lastAdminKeyError, 409)
      if (!keys.some((key) => key.id === id)) return respond(route, { error: { message: 'key not found' } }, 404)
      keys = keys.filter((key) => key.id !== id)
      return noContent(route)
    }
    if (path.startsWith('/admin/keys/') && request.method() === 'PUT') {
      const id = decodeURIComponent(path.slice('/admin/keys/'.length))
      if (id === LOCKED_KEY_ID) return respond(route, lastAdminKeyError, 409)
      const body = request.postDataJSON() as { name?: string; expires_at?: string; clear_expiration?: boolean }
      const index = keys.findIndex((key) => key.id === id)
      if (index === -1) return respond(route, { error: { message: 'key not found' } }, 404)
      const updated: FixtureKey = {
        ...keys[index],
        name: body.name || keys[index].name,
        expires_at: body.clear_expiration ? undefined : (body.expires_at ?? keys[index].expires_at),
      }
      keys = keys.map((key, i) => (i === index ? updated : key))
      return respond(route, updated)
    }
    if (path === '/admin/sessions') {
      if (request.method() === 'DELETE') {
        sessionsRevoked = true
        const revoked = sessions.length
        sessions = []
        return respond(route, { revoked })
      }
      return respond(route, { data: sessions })
    }
    if (path.startsWith('/admin/sessions/') && request.method() === 'DELETE') {
      // One sign-in ends; every other session — this browser's included — is
      // untouched, which is the entire reason this endpoint exists beside the
      // fleet-wide DELETE above.
      const id = decodeURIComponent(path.slice('/admin/sessions/'.length))
      sessions = sessions.filter((session) => session.id !== id)
      return noContent(route)
    }
    if (path === '/admin/plugins/catalog') {
      return respond(route, { data: pluginCatalog })
    }
    if (path === '/admin/config') {
      if (request.method() === 'PUT') {
        configDoc = request.postDataJSON() as Record<string, unknown>
        recordConfigVersion(configDoc)
        return respond(route, { status: 'updated' })
      }
      if (request.method() === 'DELETE') {
        // This gateway's config manager is not a ConfigResetter. Answering a
        // reset with the config document instead would have the page report a
        // restore that never happened.
        return respond(route, { error: { message: 'config reset is not enabled', code: 'not_implemented' } }, 501)
      }
      return respond(route, configDoc)
    }
    if (path === '/admin/config/history') {
      return respond(route, { data: configHistory, summary: { total_versions: configHistory.length } })
    }
    if (path.startsWith('/admin/config/rollback/') && request.method() === 'POST') {
      const version = Number(path.slice('/admin/config/rollback/'.length))
      const target = configHistory.find((entry) => entry.version === version)
      if (!target) {
        return respond(route, { error: { message: 'config version not found', code: 'resource_not_found' } }, 404)
      }
      // Version 1 routes to a provider this gateway does not have, so applying
      // it fails validation — the refusal arrives after the operator has
      // already confirmed, which is exactly where it is hardest to report.
      const stale = (target.config.targets as Array<{ virtual_key?: string }> | undefined)
        ?.find((entry) => entry.virtual_key === 'openai-legacy')
      if (stale) {
        return respond(route, {
          error: { message: 'invalid config: target virtual_key "openai-legacy" is not a registered provider', code: 'invalid_config' },
        }, 400)
      }
      const rolledBackFrom = configHistory[configHistory.length - 1].version
      configDoc = target.config
      recordConfigVersion(target.config, rolledBackFrom)
      return respond(route, { status: 'rolled_back', rolled_back_to: version, current_history_size: configHistory.length })
    }
    if (path === '/v1/capabilities') return respond(route, capabilities)
    if (path === '/v1/models') {
      return respond(route, { object: 'list', data: providers.flatMap((provider) => provider.models) })
    }
    if (path === '/v1/chat/completions') {
      if (streamFailure) {
        // What the gateway actually sends when an upstream fails mid-stream:
        // an error frame on an already-200 response, and then no [DONE].
        await route.fulfill({
          body: [
            'data: {"choices":[{"delta":{"content":"Partial ans"}}]}',
            '',
            'data: {"error":{"message":"upstream provider returned 429","type":"stream_error","code":"stream_error"}}',
            '',
          ].join('\n'),
          contentType: 'text/event-stream',
          status: 200,
        })
        return
      }
      await route.fulfill({
        body: [
          'data: {"choices":[{"delta":{"content":"Gateway route "}}]}',
          '',
          'data: {"choices":[{"delta":{"content":"is healthy."}}],"usage":{"prompt_tokens":8,"completion_tokens":4}}',
          '',
          'data: [DONE]',
          '',
        ].join('\n'),
        contentType: 'text/event-stream',
        status: 200,
      })
      return
    }
    // Reached only by a gateway path nothing above answers. Recorded and
    // asserted rather than quietly 501'd: a 501 renders as some page's error
    // state, which reads as a product bug and sends the reader into the
    // application instead of into this file.
    unmocked.push(`${request.method()} ${path}`)
    expect.soft(unmocked, 'gateway requests this fixture does not mock — add them to mockGateway').toEqual([])
    await respond(route, { error: { message: 'Fixture route not implemented' } }, 501)
  })
}

export async function signIn(page: Page, token = adminToken) {
  await page.goto('/login')
  await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible()
  await page.getByLabel('Gateway key').fill(token)
  await page.getByRole('button', { name: 'Continue' }).click()
  await expect(page).toHaveURL(/\/overview$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
}

export function recordRuntimeErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text())
  })
  return errors
}

export async function expectAccessible(page: Page) {
  const result = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
    .analyze()
  const serious = result.violations.filter((violation) =>
    violation.impact === 'critical' || violation.impact === 'serious')
  expect(serious).toEqual([])
}

