import { beforeEach, describe, expect, it, vi } from "vitest"
import {
  ApiError,
  clearSession,
  configureGateway,
  endSession,
  exchangeCredentialForSession,
  loadSession,
  rawRequest,
  request,
  requestProbe,
  requestWithToken,
  saveSession,
  SCOPE_PROBE_PATH,
  SCOPES_STALE_EVENT,
  SESSION_EXPIRED_EVENT,
} from "./api"
import { memoryStorage } from "../test/stubs"

// Pinned literal, not imported: the purge must delete the exact key earlier
// builds wrote, so renaming the constant must not quietly satisfy this test.
const SESSION_KEY = "ferrogw.dashboard.session.v1"


/**
 * Runs `body` on a browser that has no `AbortSignal.any`, then puts it back.
 *
 * The property descriptor is swapped rather than the value read and reassigned,
 * so the built-in is restored exactly as it was found.
 */
async function withoutAbortSignalAny(body: () => Promise<void>): Promise<void> {
  const descriptor = Object.getOwnPropertyDescriptor(AbortSignal, "any")
  Reflect.deleteProperty(AbortSignal, "any")
  try {
    await body()
  } finally {
    if (descriptor) Object.defineProperty(AbortSignal, "any", descriptor)
  }
}

describe("dashboard API client", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage())
    vi.stubGlobal("sessionStorage", memoryStorage())
    clearSession()
    configureGateway("")
    vi.restoreAllMocks()
  })

  it("keeps session credentials in sessionStorage and never in localStorage", () => {
    saveSession({ token: "fgw_test", scopes: ["read_only"] })
    expect(loadSession()).toEqual({ token: "fgw_test", scopes: ["read_only"] })
    expect(sessionStorage.getItem(SESSION_KEY)).toContain("fgw_test")
    expect(localStorage.length).toBe(0)
  })

  it("purges a credential persisted by an earlier build instead of honouring it", () => {
    localStorage.setItem(SESSION_KEY, JSON.stringify({ token: "fgw_stale", scopes: ["admin"] }))

    expect(loadSession()).toBeNull()
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(localStorage.length).toBe(0)
  })

  it("clears both storages on sign-out", () => {
    localStorage.setItem(SESSION_KEY, JSON.stringify({ token: "fgw_stale", scopes: ["admin"] }))
    saveSession({ token: "fgw_current", scopes: ["admin"] })

    clearSession()

    expect(sessionStorage.length).toBe(0)
    expect(localStorage.length).toBe(0)
  })

  it("rejects non-relative gateway paths before fetch", async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    await expect(requestWithToken("https://example.com/admin/health", "secret")).rejects.toThrow("configured gateway origin")
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("sends the bearer token and parses a successful response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "healthy" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }))
    vi.stubGlobal("fetch", fetchMock)

    await expect(requestWithToken<{ status: string }>("/admin/health", "fgw_secret")).resolves.toEqual({ status: "healthy" })
    const requestInit = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(new Headers(requestInit.headers).get("Authorization")).toBe("Bearer fgw_secret")
    expect(requestInit.credentials).toBe("omit")
  })

  it("sends requests only to the configured gateway origin", async () => {
    configureGateway("https://gateway.example.com")
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "healthy" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }))
    vi.stubGlobal("fetch", fetchMock)

    await requestWithToken("/admin/health", "fgw_secret")
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://gateway.example.com/admin/health")
    expect(() => configureGateway("https://user@example.com/path")).toThrow("without credentials or a path")
  })

  it("rejects paths that could select another origin", async () => {
    await expect(requestWithToken("//example.com/admin/health", "secret")).rejects.toThrow(
      "Gateway API paths must be absolute paths on the configured gateway origin",
    )
    await expect(requestWithToken("/\\n/example.com/admin/health", "secret")).rejects.toThrow(
      "Gateway API paths must stay on the configured gateway origin",
    )
  })

  it("surfaces the gateway error envelope and expires a 401 session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { message: "invalid key", code: "invalid_api_key" },
    }), { status: 401, statusText: "Unauthorized" })))
    const expired = vi.fn()
    window.addEventListener(SESSION_EXPIRED_EVENT, expired, { once: true })

    const error = await requestWithToken("/admin/health", "bad-key").catch((value: unknown) => value)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ message: "invalid key", status: 401, code: "invalid_api_key" })
    expect(expired).toHaveBeenCalledOnce()
  })

  it("exchanges a credential for a session and stores only the minted token", async () => {
    const masterKey = "fgw_supersecretmasterkey"
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      token: "fgws_abc123", subject: "master-key", scopes: ["admin"],
    }), { status: 201, headers: { "Content-Type": "application/json" } }))
    vi.stubGlobal("fetch", fetchMock)

    const session = await exchangeCredentialForSession(masterKey)
    expect(session).toEqual({
      token: "fgws_abc123",
      scopes: ["admin"],
      subject: "master-key",
      expires_at: undefined,
    })

    saveSession(session)
    const raw = sessionStorage.getItem(SESSION_KEY) ?? ""
    expect(raw).toContain("fgws_abc123")
    expect(raw).not.toContain(masterKey)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()

    const requestInit = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(new Headers(requestInit.headers).get("Authorization")).toBe(`Bearer ${masterKey}`)
  })

  it("rejects an invalid credential without storing anything", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { message: "invalid or revoked API key", type: "authentication_error" },
    }), { status: 401, headers: { "Content-Type": "application/json" } })))

    await expect(exchangeCredentialForSession("wrong")).rejects.toThrow()
    expect(loadSession()).toBeNull()
  })

  it("ends the session server-side, then clears storage", async () => {
    saveSession({ token: "fgws_current", scopes: ["admin"] })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)

    await endSession()

    expect(fetchMock).toHaveBeenCalledOnce()
    const requestInit = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(requestInit.method).toBe("DELETE")
    expect(new Headers(requestInit.headers).get("Authorization")).toBe("Bearer fgws_current")
    expect(loadSession()).toBeNull()
  })

  it("clears storage even when the server-side DELETE call fails", async () => {
    saveSession({ token: "fgws_current", scopes: ["admin"] })
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("network down")))

    await expect(endSession()).resolves.toBeUndefined()
    expect(loadSession()).toBeNull()
  })

  it("does nothing server-side when there is no session to end", async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)

    await endSession()

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    '/..//evil.example/steal',
    '/../..//attacker.test/x',
    '/a/../..//evil.test/y',
  ])('refuses a path that climbs above the root: %s', async (path) => {
    // These satisfy a check on the caller's input — they begin with a single
    // slash — and still resolve to a protocol-relative URL, which fetch would
    // send to another host with the session token attached.
    configureGateway('')
    await expect(requestWithToken(path, 'fgws_abc')).rejects.toThrow(/configured gateway origin/)
  })

  it('sends authenticated requests with caching disabled', async () => {
    configureGateway('')
    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    const fetchMock = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      void init
      return Promise.resolve(
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        }),
      )
    })
    vi.stubGlobal('fetch', fetchMock)

    await request('/admin/keys')

    // Several admin responses omit Cache-Control, and a private browser cache
    // may otherwise write them to disk where they outlive the session.
    expect(fetchMock.mock.calls.at(0)?.[1]).toMatchObject({ cache: 'no-store' })
  })

  it('keeps the subject and expiry beside the token', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      token: 'fgws_abc123',
      subject: 'Ops laptop',
      scopes: ['admin', 'not_a_scope'],
      expires_at: '2026-07-27T09:00:00Z',
    }), { status: 201, headers: { 'Content-Type': 'application/json' } })))

    const session = await exchangeCredentialForSession('fgw_master')
    // An unrecognised scope grants nothing rather than being carried around.
    expect(session).toEqual({
      token: 'fgws_abc123',
      scopes: ['admin'],
      subject: 'Ops laptop',
      expires_at: '2026-07-27T09:00:00Z',
    })

    saveSession(session)
    expect(loadSession()).toEqual(session)
  })

  it('reports a hung gateway as a timeout rather than a generic failure', async () => {
    vi.useFakeTimers()
    try {
      saveSession({ token: 'fgws_abc', scopes: ['admin'] })
      // Settles only when aborted, which is what a gateway that has stopped
      // answering looks like from here.
      vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () =>
            reject(new DOMException('aborted', 'AbortError')))
        })))

      const pending = request('/admin/keys').catch((value: unknown) => value)
      await vi.advanceTimersByTimeAsync(30_000)

      const error = await pending
      expect(error).toBeInstanceOf(ApiError)
      expect(error).toMatchObject({ status: 504, code: 'gateway_timeout' })
      expect((error as ApiError).message).toContain('did not respond in time')
    } finally {
      vi.useRealTimers()
    }
  })

  it('reports an unreachable gateway in words an operator can act on', async () => {
    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    // What fetch rejects with when the request never reached a server:
    // connection refused, DNS failure, a preflight nothing answered. The
    // browser's own wording — "Failed to fetch" — reached every page verbatim.
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))

    const error = await request('/admin/keys').catch((value: unknown) => value)

    expect(error).toBeInstanceOf(ApiError)
    // Status 0 because no status was received; claiming a 5xx would report an
    // answer the gateway never gave.
    expect(error).toMatchObject({ status: 0, code: 'gateway_unreachable' })
    expect((error as ApiError).message).toContain('could not be reached')
  })

  it('translates an unreachable gateway on the probe and stream paths too', async () => {
    // The fix belongs in the one function every call routes its failures
    // through, so no page inherits the untranslated message. requestProbe and
    // rawRequest are the two callers that are not requestWithToken.
    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))

    for (const call of [requestProbe('/readyz'), rawRequest('/v1/chat/completions', { method: 'POST' })]) {
      const error = await call.catch((value: unknown) => value)
      expect(error).toMatchObject({ status: 0, code: 'gateway_unreachable' })
    }
  })

  it('re-reads the session scopes when a call is refused for lacking them', async () => {
    // Scopes are read once at sign-in, so a credential de-scoped mid-session
    // keeps showing controls that then 403. The 403 is the signal to go and
    // look again; without the event the tab has to be reloaded to notice.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { message: 'admin scope required', code: 'forbidden' },
    }), { status: 403 })))
    const stale = vi.fn()
    window.addEventListener(SCOPES_STALE_EVENT, stale, { once: true })

    await requestWithToken('/admin/keys', 'read-only-token').catch(() => {})

    expect(stale).toHaveBeenCalledOnce()
  })

  it('does not announce stale scopes for a 403 on the probe the listener reads', async () => {
    // The listener re-reads scopes from SCOPE_PROBE_PATH. Announcing a 403 on
    // that path would re-trigger the listener that issued it, forever.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 403 })))
    const stale = vi.fn()
    window.addEventListener(SCOPES_STALE_EVENT, stale, { once: true })

    await requestWithToken(SCOPE_PROBE_PATH, 'read-only-token').catch(() => {})

    expect(stale).not.toHaveBeenCalled()
  })

  it('still honours a caller abort, and does not call it a timeout', async () => {
    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () =>
          reject(new DOMException('aborted', 'AbortError')))
      })))

    const controller = new AbortController()
    const pending = request('/admin/keys', { signal: controller.signal }).catch(
      (value: unknown) => value,
    )
    controller.abort()

    const error = await pending
    // Pages treat an AbortError as teardown; turning it into an ApiError here
    // would surface every navigation away from a loading page as a failure.
    expect(error).toBeInstanceOf(DOMException)
    expect((error as DOMException).name).toBe('AbortError')
  })

  it('bounds only the connect for a streamed response', async () => {
    vi.useFakeTimers()
    try {
      saveSession({ token: 'fgws_abc', scopes: ['admin'] })
      let streamSignal: AbortSignal | undefined
      vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => {
        streamSignal = init?.signal ?? undefined
        return Promise.resolve(new Response('data: {}\n\n', { status: 200 }))
      }))

      await rawRequest('/v1/chat/completions', { method: 'POST' })
      // A generation that runs for minutes is not a hang: once the headers are
      // in, the deadline is disarmed and the body streams for as long as it
      // needs.
      await vi.advanceTimersByTimeAsync(120_000)

      expect(streamSignal?.aborted).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('refuses a base URL that is not an origin at all', () => {
    // It arrives from the runtime config.json a deployment writes, so it is
    // caller-supplied and has to fail loudly at configuration time rather than
    // by sending credentials somewhere unintended later.
    expect(() => configureGateway('gateway.example.com')).toThrow('absolute HTTP or HTTPS origin')
    expect(() => configureGateway('ftp://gateway.example.com')).toThrow('without credentials or a path')
  })

  it('ignores a session another tab or an older build left corrupted', () => {
    sessionStorage.setItem(SESSION_KEY, '{not json')
    expect(loadSession()).toBeNull()

    // A body that parses but carries no usable token is equally unusable, and
    // honouring it would send `Bearer undefined` to every endpoint.
    sessionStorage.setItem(SESSION_KEY, JSON.stringify({ token: '', scopes: ['admin'] }))
    expect(loadSession()).toBeNull()
  })

  it('refuses to send a request with no session rather than one with no credential', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(request('/admin/keys')).rejects.toMatchObject({ status: 401, code: 'session_expired' })
    await expect(rawRequest('/v1/chat/completions')).rejects.toMatchObject({ status: 401, code: 'session_expired' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('falls back to the status when the gateway sends no error envelope', async () => {
    // A reverse proxy in front of the gateway answers with HTML, not the
    // gateway's own envelope, and "undefined" is not a message.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response('<html>502</html>', { status: 502, statusText: 'Bad Gateway' }),
    ))

    await expect(requestWithToken('/admin/keys', 'fgws_abc')).rejects.toThrow('Bad Gateway')
  })

  it('declares JSON for a body, and leaves a caller-set content type alone', async () => {
    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await request('/admin/keys', { method: 'POST', body: JSON.stringify({ name: 'Ops laptop' }) })
    expect(new Headers((fetchMock.mock.calls[0]?.[1] as RequestInit).headers).get('Content-Type'))
      .toBe('application/json')

    await request('/admin/keys', {
      method: 'POST',
      body: 'name=Ops',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    })
    expect(new Headers((fetchMock.mock.calls[1]?.[1] as RequestInit).headers).get('Content-Type'))
      .toBe('application/x-www-form-urlencoded')
  })

  it('expires the session on a 401 from a streamed call too', async () => {
    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { message: 'session expired', code: 'session_expired' },
    }), { status: 401, headers: { 'Content-Type': 'application/json' } })))
    const expired = vi.fn()
    window.addEventListener(SESSION_EXPIRED_EVENT, expired, { once: true })

    // The Playground streams through this path, so an expired session there has
    // to sign the operator out rather than reading as a failed generation.
    await expect(rawRequest('/v1/chat/completions', { method: 'POST' })).rejects.toThrow('session expired')
    expect(expired).toHaveBeenCalledOnce()
  })

  it('leaves a caller that expects an arbitrarily long wait entirely unbounded', async () => {
    vi.useFakeTimers()
    try {
      saveSession({ token: 'fgws_abc', scopes: ['admin'] })
      let sentSignal: AbortSignal | null | undefined
      vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => {
        sentSignal = init?.signal
        return Promise.resolve(new Response('{}', { status: 200 }))
      }))

      await rawRequest('/v1/chat/completions', { method: 'POST' }, { connectTimeoutMs: 0 })
      await vi.advanceTimersByTimeAsync(600_000)

      // No deadline is armed at all, so nothing can abort but the caller.
      expect(sentSignal ?? null).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('reports a probe that never answers as a timeout, like every other call', async () => {
    vi.useFakeTimers()
    try {
      const controller = new AbortController()
      vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () =>
            reject(new DOMException('aborted', 'AbortError')))
        })))

      const pending = requestProbe('/readyz', { signal: controller.signal }).catch((value: unknown) => value)
      await vi.advanceTimersByTimeAsync(30_000)

      expect(await pending).toMatchObject({ status: 504, code: 'gateway_timeout' })
    } finally {
      vi.useRealTimers()
    }
  })

  it('reads a 503 from a probe as its answer, not as a failed request', async () => {
    // /readyz reports "not ready" as a 503 carrying the one field a readiness
    // panel exists to show. Throwing on !ok would discard the reason and report
    // a working gateway as a broken request.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      status: 'not_ready', reason: 'required mcp server unavailable',
    }), { status: 503, headers: { 'Content-Type': 'application/json' } })))

    await expect(requestProbe('/readyz')).resolves.toEqual({
      status: 'not_ready',
      reason: 'required mcp server unavailable',
    })
  })

  it('still treats every other probe failure as a fault', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { message: 'readiness check panicked' },
    }), { status: 500, headers: { 'Content-Type': 'application/json' } })))

    const error = await requestProbe('/readyz').catch((value: unknown) => value)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 500, message: 'readiness check panicked' })
  })

  it('sends no session token to an endpoint mounted ahead of the auth middleware', async () => {
    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'ready' }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await requestProbe('/health')

    // /health, /livez and /readyz are unauthenticated, so attaching the token
    // would send it further than it needs to go for nothing in return.
    const requestInit = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(new Headers(requestInit.headers).get('Authorization')).toBeNull()
    expect(requestInit.credentials).toBe('omit')
  })

  it('still honours a caller abort on a browser without AbortSignal.any', async () => {
    // The bundle targets ES2022 — roughly Chrome 94 — while `AbortSignal.any`
    // needs Chrome 116. Without the fallback every request carrying a caller
    // signal throws, which is every page load.
    await withoutAbortSignalAny(async () => {
      saveSession({ token: 'fgws_abc', scopes: ['admin'] })
      vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () =>
            reject(new DOMException('aborted', 'AbortError')))
        })))

      const controller = new AbortController()
      const pending = request('/admin/keys', { signal: controller.signal }).catch(
        (value: unknown) => value,
      )
      controller.abort()

      const error = await pending
      expect(error).toBeInstanceOf(DOMException)
      expect((error as DOMException).name).toBe('AbortError')
    })
  })

  it('does not start a request whose caller signal is already aborted', async () => {
    await withoutAbortSignalAny(async () => {
      saveSession({ token: 'fgws_abc', scopes: ['admin'] })
      // A page that unmounts between deciding to load and loading aborts before
      // the call. Forwarding an already-aborted signal has to abort the combined
      // one immediately rather than waiting for an event that has been and gone.
      vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) =>
        init?.signal?.aborted
          ? Promise.reject(new DOMException('aborted', 'AbortError'))
          : Promise.resolve(new Response('{}', { status: 200 }))))

      const controller = new AbortController()
      controller.abort()

      const error = await request('/admin/keys', { signal: controller.signal }).catch(
        (value: unknown) => value,
      )
      expect(error).toBeInstanceOf(DOMException)
      expect((error as DOMException).name).toBe('AbortError')
    })
  })

  it('purges onboarding flags an earlier build left behind', () => {
    // They survived sign-out, so on a shared workstation the next operator
    // inherited the previous one's checklist. Nothing reads them now, so this
    // load is the only remaining opportunity to remove them.
    localStorage.setItem('ferrogw.dashboard.visitedLogs', 'true')
    localStorage.setItem('ferrogw.dashboard.madeRequest', 'true')

    loadSession()

    expect(localStorage.getItem('ferrogw.dashboard.visitedLogs')).toBeNull()
    expect(localStorage.getItem('ferrogw.dashboard.madeRequest')).toBeNull()
  })
})
