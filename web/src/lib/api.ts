import type { GatewayErrorEnvelope, Scope, Session } from '../types'

const KNOWN_SCOPES: readonly Scope[] = ['admin', 'read_only']

/**
 * Keeps only scopes this build understands.
 *
 * A scope a newer gateway invents grants nothing here until the dashboard
 * learns it, which is the safe direction: an unrecognised string can only ever
 * be read as "not admin".
 */
function parseScopes(value: unknown): Scope[] {
  if (!Array.isArray(value)) return []
  return value.filter((scope): scope is Scope => KNOWN_SCOPES.includes(scope as Scope))
}

const SESSION_KEY = 'ferrogw.dashboard.session.v1'

/**
 * Keys earlier builds wrote to localStorage and no longer read.
 *
 * Two onboarding flags tracked whether the operator had visited a page. They
 * were never cleared on sign-out, so on a shared workstation the next operator
 * inherited someone else's checklist. Nothing reads them now, which is exactly
 * why they need removing here — no other code path would ever reach them.
 */
const ABANDONED_KEYS = [
  'ferrogw.dashboard.visitedLogs',
  'ferrogw.dashboard.madeRequest',
]
export const SESSION_EXPIRED_EVENT = 'ferrogw:session-expired'

/**
 * Fired when the gateway refuses a call the signed-in scopes said was allowed.
 *
 * Scopes are read once, at sign-in, so a credential de-scoped mid-session leaves
 * every admin control on screen until the tab is reloaded — and each one answers
 * 403 when used. A 403 is the gateway saying the snapshot is stale, so it is
 * also the moment to go and re-read it.
 */
export const SCOPES_STALE_EVENT = 'ferrogw:scopes-stale'

/**
 * The endpoint the session's scopes are read from, at sign-in and again after a
 * 403. Named here because a 403 on this path must not fire SCOPES_STALE_EVENT:
 * that would re-trigger the listener that issued the probe, forever.
 */
export const SCOPE_PROBE_PATH = '/admin/health'

let gatewayBaseURL = ''

export function configureGateway(baseURL: string): void {
  const configured = baseURL.trim()
  if (configured === '') {
    gatewayBaseURL = ''
    return
  }

  let parsed: URL
  try {
    parsed = new URL(configured)
  } catch {
    throw new Error('gatewayBaseUrl must be an absolute HTTP or HTTPS origin')
  }
  if (
    (parsed.protocol !== 'http:' && parsed.protocol !== 'https:')
    || parsed.username !== ''
    || parsed.password !== ''
    || parsed.pathname !== '/'
    || parsed.search !== ''
    || parsed.hash !== ''
  ) {
    throw new Error('gatewayBaseUrl must be an absolute HTTP or HTTPS origin without credentials or a path')
  }
  gatewayBaseURL = parsed.origin
}

/**
 * How long a JSON API call may take end to end.
 *
 * Nothing under /admin is a long-running operation, so a request still open
 * after this is a gateway that has stopped answering rather than one that is
 * working hard. Without a bound the page shows its spinner until the operator
 * reloads, which is the least informative failure the console can produce.
 */
const REQUEST_TIMEOUT_MS = 30_000

const TIMEOUT_MESSAGE =
  'The gateway did not respond in time. It may be overloaded or unreachable.'

const UNREACHABLE_MESSAGE =
  'The gateway could not be reached. Check that it is running and that this dashboard is pointed at the right address.'

/**
 * Arms a deadline that fires until `disarm` is called, combined with whatever
 * signal the caller already passes so a per-page AbortController still works.
 *
 * `disarm` is what separates a hang from a slow answer. A JSON call disarms
 * once the body is parsed, so the deadline covers the whole exchange. A
 * streamed call disarms as soon as the response headers arrive, because from
 * that point the gateway is demonstrably answering and the remaining time
 * belongs to the model — a four-minute generation is not a hang, and aborting
 * it at thirty seconds would truncate the reply mid-sentence.
 */
function armDeadline(
  timeoutMs: number,
  callerSignal: AbortSignal | null | undefined,
): { signal: AbortSignal | undefined; disarm: () => void; expired: () => boolean } {
  if (timeoutMs <= 0) {
    return { signal: callerSignal ?? undefined, disarm: () => {}, expired: () => false }
  }
  const controller = new AbortController()
  let expired = false
  const timer = setTimeout(() => {
    expired = true
    controller.abort()
  }, timeoutMs)
  return {
    // Tracked with a flag rather than read back off the abort reason: a caller
    // abort and a deadline abort are both plain AbortErrors here, and only the
    // second one is a failure worth reporting to the operator.
    signal: combineSignals(callerSignal, controller.signal),
    disarm: () => clearTimeout(timer),
    expired: () => expired,
  }
}

/**
 * Aborts when either signal does.
 *
 * `AbortSignal.any` is the whole of this function and needs Chrome 116 /
 * Safari 17.4 / Firefox 124, while the bundle targets ES2022 — roughly Chrome
 * 94. Nothing else in the app raises the floor that far, and the gap is not
 * hypothetical for a dashboard reached from whatever browser a locked-down
 * operations workstation happens to ship. Without the fallback every request
 * carrying a caller signal throws here, which is every page load.
 */
function combineSignals(
  callerSignal: AbortSignal | null | undefined,
  deadlineSignal: AbortSignal,
): AbortSignal {
  if (!callerSignal) return deadlineSignal
  if (typeof AbortSignal.any === 'function') {
    return AbortSignal.any([callerSignal, deadlineSignal])
  }
  const merged = new AbortController()
  const forward = (source: AbortSignal) => {
    if (source.aborted) {
      merged.abort(source.reason)
      return
    }
    source.addEventListener('abort', () => merged.abort(source.reason), { once: true })
  }
  forward(callerSignal)
  forward(deadlineSignal)
  return merged.signal
}

export class ApiError extends Error {
  readonly status: number
  readonly code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

function parseSession(raw: string | null): Session | null {
  if (!raw) return null
  try {
    const value = JSON.parse(raw) as Partial<Session>
    if (typeof value.token !== 'string' || value.token.length === 0) return null
    return {
      token: value.token,
      scopes: parseScopes(value.scopes),
      subject: typeof value.subject === 'string' ? value.subject : undefined,
      expires_at: typeof value.expires_at === 'string' ? value.expires_at : undefined,
    }
  } catch {
    return null
  }
}

export function loadSession(): Session | null {
  // Earlier builds persisted the gateway key in localStorage indefinitely. Purge
  // it here rather than in clearSession alone: a stale key is never read back, so
  // nothing else would reach it to delete it on an upgraded browser.
  localStorage.removeItem(SESSION_KEY)
  for (const key of ABANDONED_KEYS) localStorage.removeItem(key)
  return parseSession(sessionStorage.getItem(SESSION_KEY))
}

export function saveSession(session: Session): void {
  // Subject and expiry ride along with the token: neither is a secret, and
  // storing them is what lets a reload name who is signed in without a probe.
  sessionStorage.setItem(
    SESSION_KEY,
    JSON.stringify({
      token: session.token,
      scopes: session.scopes,
      subject: session.subject,
      expires_at: session.expires_at,
    }),
  )
}

export function clearSession(): void {
  sessionStorage.removeItem(SESSION_KEY)
  localStorage.removeItem(SESSION_KEY)
}

async function errorFromResponse(response: Response): Promise<ApiError> {
  let body: GatewayErrorEnvelope | null = null
  try {
    body = (await response.json()) as GatewayErrorEnvelope
  } catch {
    body = null
  }
  const fallback = response.statusText || `Request failed with status ${response.status}`
  return new ApiError(body?.error?.message || fallback, response.status, body?.error?.code)
}

function headersFor(token: string, init?: RequestInit): Headers {
  const headers = new Headers(init?.headers)
  headers.set('Authorization', `Bearer ${token}`)
  if (init?.body != null && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  return headers
}

function assertRelativeGatewayPath(path: string): void {
  if (!path.startsWith('/') || path.startsWith('//')) {
    throw new Error('Gateway API paths must be absolute paths on the configured gateway origin')
  }
}

function gatewayURL(path: string): string {
  const trustedOrigin = gatewayBaseURL || 'http://gateway.invalid'
  const resolved = new URL(path, `${trustedOrigin}/`)
  if (resolved.origin !== trustedOrigin) {
    throw new Error('Gateway API paths must stay on the configured gateway origin')
  }
  if (gatewayBaseURL === '') {
    const relative = `${resolved.pathname}${resolved.search}${resolved.hash}`
    // The resolved path is checked, not the caller's input. A path that climbs
    // above the root — "/..//evil.example/steal" — passes an input check and
    // still resolves to a protocol-relative URL, which fetch would send to
    // another host with the session token attached. Resolution is where the
    // escape becomes visible, so it is where the guard belongs.
    if (relative.startsWith('//')) {
      throw new Error('Gateway API paths must stay on the configured gateway origin')
    }
    return relative
  }
  return resolved.toString()
}

/**
 * Exchange a long-lived gateway credential for a short-lived session.
 *
 * The credential the operator types is sent exactly once and never persisted:
 * only the returned session token reaches storage. That is the whole point of
 * this call — a master key in browser storage cannot be revoked or expired,
 * a session can.
 */
export async function exchangeCredentialForSession(credential: string): Promise<Session> {
  const deadline = armDeadline(REQUEST_TIMEOUT_MS, null)
  try {
    const response = await fetch(gatewayURL('/admin/session'), {
      method: 'POST',
      headers: { Authorization: `Bearer ${credential}` },
      credentials: 'omit',
      cache: 'no-store',
      signal: deadline.signal,
    })
    if (!response.ok) {
      throw await errorFromResponse(response)
    }
    const body = (await response.json()) as {
      token: string
      scopes?: unknown
      subject?: unknown
      expires_at?: unknown
    }
    return {
      token: body.token,
      scopes: parseScopes(body.scopes),
      subject: typeof body.subject === 'string' ? body.subject : undefined,
      expires_at: typeof body.expires_at === 'string' ? body.expires_at : undefined,
    }
  } catch (error) {
    throw timeoutOr(error, deadline)
  } finally {
    deadline.disarm()
  }
}

/**
 * Replaces a transport failure with something an operator can act on.
 *
 * Two shapes reach here, and neither carries a status because neither produced
 * a response. Every gateway call routes through this one function, so both are
 * translated once rather than per page — a page that translated one of them
 * would leave every other page reporting the browser's own wording.
 */
function timeoutOr(error: unknown, deadline: { expired: () => boolean }): unknown {
  // Only the abort the deadline itself caused. A gateway that answers 401 a
  // moment after the clock runs out has still answered, and reporting that as
  // a timeout would hide the expired session behind a network story. A caller's
  // own abort — a page unmounting — is left alone for the caller to recognise.
  const aborted = error instanceof DOMException && error.name === 'AbortError'
  if (aborted) {
    return deadline.expired() ? new ApiError(TIMEOUT_MESSAGE, 504, 'gateway_timeout') : error
  }
  // fetch rejects with a TypeError for every failure that never reached a
  // server: connection refused, DNS failure, TLS rejection, a preflight nothing
  // answered. Its message is "Failed to fetch" (or "NetworkError when
  // attempting to fetch resource"), which names neither the gateway nor
  // anything to do about it — and a gateway that is simply not running is the
  // most common first-run failure there is.
  //
  // Status 0 rather than a 5xx: no status was received, and claiming one would
  // have the page report an answer the gateway never gave.
  if (error instanceof TypeError) {
    return new ApiError(UNREACHABLE_MESSAGE, 0, 'gateway_unreachable')
  }
  return error
}

/**
 * GETs an unauthenticated probe endpoint whose failure status is itself the answer.
 *
 * `/readyz` reports "not ready" as a 503 carrying `{status, reason}`, so the
 * ordinary throw-on-!ok path would discard the one field a readiness panel
 * exists to show and report a working gateway as a broken request. Only 2xx and
 * 503 count as answers here; every other status is still a fault.
 *
 * No Authorization header: `/health`, `/livez` and `/readyz` are mounted ahead
 * of the auth middleware, so attaching the session token would send it further
 * than it needs to go for nothing in return.
 */
export async function requestProbe<T>(path: string, init?: RequestInit): Promise<T> {
  assertRelativeGatewayPath(path)
  const deadline = armDeadline(REQUEST_TIMEOUT_MS, init?.signal)
  try {
    const response = await fetch(gatewayURL(path), {
      ...init,
      credentials: 'omit',
      cache: 'no-store',
      signal: deadline.signal,
    })
    if (!response.ok && response.status !== 503) {
      throw await errorFromResponse(response)
    }
    return (await response.json()) as T
  } catch (error) {
    throw timeoutOr(error, deadline)
  } finally {
    deadline.disarm()
  }
}

/** End the current session server-side, then clear local storage. */
export async function endSession(): Promise<void> {
  const session = loadSession()
  if (session) {
    try {
      await requestWithToken('/admin/session', session.token, { method: 'DELETE' })
    } catch {
      // A failed logout must still clear the browser. The token may already be
      // expired or revoked, and leaving it in storage would be worse than a
      // server-side row that expires on its own.
    }
  }
  clearSession()
}

export async function requestWithToken<T>(
  path: string,
  token: string,
  init?: RequestInit,
): Promise<T> {
  assertRelativeGatewayPath(path)
  const deadline = armDeadline(REQUEST_TIMEOUT_MS, init?.signal)
  try {
    const response = await fetch(gatewayURL(path), {
      ...init,
      credentials: 'omit',
      // Several admin responses carry key names, scopes and usage counts without
      // a Cache-Control header of their own. A shared cache is forbidden from
      // storing an Authorization-bearing response, but a private browser cache is
      // not, so those bodies could be written to disk and outlive both logout and
      // the session being cleared. Setting it here covers every endpoint rather
      // than each one separately.
      cache: 'no-store',
      headers: headersFor(token, init),
      signal: deadline.signal,
    })

    if (!response.ok) {
      const error = await errorFromResponse(response)
      announceAuthFailure(path, error.status)
      throw error
    }

    if (response.status === 204) return undefined as T
    return (await response.json()) as T
  } catch (error) {
    throw timeoutOr(error, deadline)
  } finally {
    deadline.disarm()
  }
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const session = loadSession()
  if (!session) throw new ApiError('Your session has expired. Sign in again.', 401, 'session_expired')
  return requestWithToken<T>(path, session.token, init)
}

/**
 * Returns the raw Response, for callers that read the body themselves.
 *
 * The deadline here covers reaching the gateway, not finishing with it: it is
 * disarmed the moment the response headers arrive. This route serves the
 * streamed chat completion, where the body stays open for as long as the model
 * keeps producing tokens — a whole-response deadline would cut a long
 * generation off partway through and report it as a failure.
 *
 * `connectTimeoutMs: 0` removes even that bound, for a caller that expects a
 * gateway to take arbitrarily long to answer at all.
 */
export async function rawRequest(
  path: string,
  init?: RequestInit,
  { connectTimeoutMs = REQUEST_TIMEOUT_MS }: { connectTimeoutMs?: number } = {},
): Promise<Response> {
  assertRelativeGatewayPath(path)
  const session = loadSession()
  if (!session) throw new ApiError('Your session has expired. Sign in again.', 401, 'session_expired')
  const deadline = armDeadline(connectTimeoutMs, init?.signal)
  let response: Response
  try {
    response = await fetch(gatewayURL(path), {
      ...init,
      credentials: 'omit',
      cache: 'no-store',
      headers: headersFor(session.token, init),
      signal: deadline.signal,
    })
  } catch (error) {
    throw timeoutOr(error, deadline)
  } finally {
    deadline.disarm()
  }
  if (!response.ok) {
    const error = await errorFromResponse(response)
    announceAuthFailure(path, error.status)
    throw error
  }
  return response
}

/**
 * Tells the auth layer what the gateway just said about this credential.
 *
 * Both authenticated request paths report here rather than each testing the
 * status themselves, so a new one cannot be added that signs a revoked session
 * out but leaves a de-scoped one showing controls it cannot use.
 */
function announceAuthFailure(path: string, status: number): void {
  if (status === 401) {
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT))
    return
  }
  if (status === 403 && path !== SCOPE_PROBE_PATH) {
    window.dispatchEvent(new Event(SCOPES_STALE_EVENT))
  }
}
