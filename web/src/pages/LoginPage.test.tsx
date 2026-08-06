import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider } from '../auth/AuthProvider'
import { clearSession, configureGateway, saveSession } from '../lib/api'
import type { Call, Reply } from '../test/stubs'
import { memoryStorage, stubGateway } from '../test/stubs'
import LoginPage from './LoginPage'

/*
 * The gateway is stubbed at `fetch`, not at the api module. This page is the
 * dashboard's security boundary and the claims worth testing are about what
 * crosses that boundary and what is left behind in the browser afterwards —
 * which credential reaches the wire, how many times, and what lands in storage.
 * A mocked `login` would assert only that the page called a function.
 */

/** The credential an operator types. Distinctive so it can be searched for. */
const CREDENTIAL = 'fgw_master_9f2a7c41e6b0'
const SESSION_TOKEN = 'sess_7d1c4b8e'

const sessionReply: Reply = {
  body: {
    token: SESSION_TOKEN,
    scopes: ['admin'],
    subject: 'Ops laptop',
    expires_at: '2026-07-27T09:00:00Z',
  },
}

/** Answers the session exchange with `reply`; anything else with an empty 200. */
function stubSessionExchange(reply: Reply | (() => Promise<Reply>)): Call[] {
  return stubGateway((call) => {
    if (call.url.startsWith('/admin/session')) return typeof reply === 'function' ? reply() : reply
    return { body: { status: 'ok', providers: [], scopes: ['admin'] } }
  })
}

/** Every key and value a storage area holds, as one searchable string. */
function dump(storage: Storage): string {
  const entries: string[] = []
  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index)
    if (key === null) continue
    entries.push(`${key}=${storage.getItem(key) ?? ''}`)
  }
  return entries.join('\n')
}

/** Surfaces the destination so where the operator landed is asserted, not inferred. */
function LocationProbe() {
  const { pathname } = useLocation()
  return <span data-testid="landed-on">{pathname}</span>
}

function landedOn(): string {
  return screen.getByTestId('landed-on').textContent ?? ''
}

/**
 * `from` is what a ProtectedRoute stashes when it bounces an unauthenticated
 * operator here, and it is attacker-reachable: a crafted link can carry any
 * location state into the app.
 */
function renderLogin(from?: string) {
  return render(
    <MemoryRouter initialEntries={[{ pathname: '/login', state: from === undefined ? null : { from } }]}>
      <AuthProvider>
        <LocationProbe />
        <Routes>
          <Route element={<LoginPage />} path="/login" />
          <Route element={<h1>Overview</h1>} path="/overview" />
          <Route element={<h1>API keys</h1>} path="/keys" />
          <Route element={<h1>Somewhere else</h1>} path="*" />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  )
}

async function signIn(user: ReturnType<typeof userEvent.setup>, credential = CREDENTIAL) {
  await user.type(screen.getByLabelText('Gateway key'), credential)
  await user.click(screen.getByRole('button', { name: /Continue/ }))
}

describe('LoginPage', () => {
  beforeEach(() => {
    configureGateway('')
    vi.stubGlobal('localStorage', memoryStorage())
    vi.stubGlobal('sessionStorage', memoryStorage())
    clearSession()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('exchanges the typed key for a session and sends the operator into the console', async () => {
    const user = userEvent.setup()
    const calls = stubSessionExchange(sessionReply)

    renderLogin()
    await signIn(user)

    await waitFor(() => expect(landedOn()).toBe('/overview'))
    expect(screen.getByRole('heading', { name: 'Overview' })).toBeInTheDocument()
    const exchange = calls.find((call) => call.url.startsWith('/admin/session'))
    expect(exchange?.method).toBe('POST')
    expect(exchange?.authorization).toBe(`Bearer ${CREDENTIAL}`)
  })

  it('sends the typed credential once and leaves only the minted session token in the browser', async () => {
    const user = userEvent.setup()
    const calls = stubSessionExchange(sessionReply)

    renderLogin()
    await signIn(user)
    await waitFor(() => expect(landedOn()).toBe('/overview'))

    // The whole reason POST /admin/session exists: a gateway key in browser
    // storage cannot be revoked or expired, a session token can. If the typed
    // credential appears in either storage area, that exchange bought nothing.
    expect(dump(sessionStorage)).not.toContain(CREDENTIAL)
    expect(dump(localStorage)).not.toContain(CREDENTIAL)
    expect(dump(sessionStorage)).toContain(SESSION_TOKEN)
    expect(localStorage.length).toBe(0)
    // Once. Every extra send is another chance to be logged by a proxy.
    expect(calls.filter((call) => call.authorization === `Bearer ${CREDENTIAL}`)).toHaveLength(1)
  })

  it('reports a refused key in the form and keeps the operator on the sign-in page', async () => {
    const user = userEvent.setup()
    stubSessionExchange({ status: 401, body: { error: { message: 'invalid API key' } } })

    renderLogin()
    await signIn(user, 'fgw_wrong')

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid API key')
    expect(landedOn()).toBe('/login')
    expect(screen.getByLabelText('Gateway key')).toBeInTheDocument()
    expect(dump(sessionStorage)).toBe('')
  })

  it('names the sign-in throttle on a 429 instead of blaming the key, which would send the operator hunting for a new one', async () => {
    const user = userEvent.setup()
    // POST /admin/session is throttled per source address and the throttle
    // cannot be switched off, so an operator behind a shared egress address
    // meets it without having done anything wrong.
    stubSessionExchange({ status: 429, body: { error: { message: 'rate limit exceeded', code: 'rate_limit_exceeded' } } })

    renderLogin()
    await signIn(user)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('rate limit exceeded')
    expect(alert).not.toHaveTextContent('could not verify this key')
    expect(landedOn()).toBe('/login')
  })

  it('explains a 501 as a deployment that has session auth switched off, not as a bad key', async () => {
    const user = userEvent.setup()
    stubSessionExchange({ status: 501, body: { error: { message: 'session authentication is not enabled' } } })

    renderLogin()
    await signIn(user)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('session authentication is not enabled')
    expect(alert).not.toHaveTextContent('could not verify this key')
    expect(landedOn()).toBe('/login')
  })

  /*
   * `login()` flips the auth status before the submit handler resumes, so the
   * page re-renders and its authenticated early return commits the navigation.
   * Any second `navigate()` in the handler would lose that race, which is why
   * the destination has to travel through the early return — and why the three
   * redirect tests below are worth keeping: they fail if a future refactor
   * moves the navigation somewhere that does not carry `safeDestination`.
   */
  it('returns the operator to the in-app page they were bounced off', async () => {
    const user = userEvent.setup()
    stubSessionExchange(sessionReply)

    renderLogin('/keys')
    await signIn(user)

    await waitFor(() => expect(landedOn()).toBe('/keys'), { timeout: 400 })
  })

  it('refuses an absolute destination, so a crafted sign-in link cannot bounce the operator off-origin', async () => {
    const user = userEvent.setup()
    stubSessionExchange(sessionReply)

    renderLogin('https://evil.example/steal')
    await signIn(user)

    // Landing off-origin right after sign-in is how a session token gets typed
    // into a copy of this page.
    await waitFor(() => expect(landedOn()).toBe('/overview'))
  })

  it('refuses a protocol-relative destination, which reads as a path but resolves to another host', async () => {
    const user = userEvent.setup()
    stubSessionExchange(sessionReply)

    renderLogin('//evil.example/steal')
    await signIn(user)

    await waitFor(() => expect(landedOn()).toBe('/overview'))
  })

  it('refuses a destination that is not a string at all', async () => {
    const user = userEvent.setup()
    stubSessionExchange(sessionReply)

    // Location state is whatever the previous navigation put there; nothing
    // guarantees it is the shape this page expects.
    render(
      <MemoryRouter initialEntries={[{ pathname: '/login', state: { from: { pathname: '/keys' } } }]}>
        <AuthProvider>
          <LocationProbe />
          <Routes>
            <Route element={<LoginPage />} path="/login" />
            <Route element={<h1>Overview</h1>} path="/overview" />
            <Route element={<h1>Somewhere else</h1>} path="*" />
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    )
    await signIn(user)

    await waitFor(() => expect(landedOn()).toBe('/overview'))
  })

  it('marks the submit control busy while the exchange is in flight, so a slow gateway cannot be signed into twice', async () => {
    const user = userEvent.setup()
    let release = () => {}
    const held = new Promise<void>((resolve) => { release = resolve })
    const calls = stubSessionExchange(async () => {
      await held
      return sessionReply
    })

    renderLogin()
    await user.type(screen.getByLabelText('Gateway key'), CREDENTIAL)
    await user.click(screen.getByRole('button', { name: /Continue/ }))

    const busy = await screen.findByRole('button', { name: 'Verifying…' })
    expect(busy).toBeDisabled()
    await user.click(busy)
    expect(calls.filter((call) => call.url.startsWith('/admin/session'))).toHaveLength(1)

    release()
    await waitFor(() => expect(landedOn()).toBe('/overview'))
    expect(calls.filter((call) => call.url.startsWith('/admin/session'))).toHaveLength(1)
  })

  it('asks for a key rather than spending a throttled sign-in attempt on an empty field', async () => {
    const user = userEvent.setup()
    const calls = stubSessionExchange(sessionReply)

    renderLogin()
    await user.type(screen.getByLabelText('Gateway key'), '   ')
    await user.click(screen.getByRole('button', { name: /Continue/ }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Enter an administrator or read-only API key.')
    expect(calls).toHaveLength(0)
    expect(landedOn()).toBe('/login')
  })

  it('masks the key by default and reveals it on request, so a mistyped key can be checked', async () => {
    const user = userEvent.setup()
    stubSessionExchange(sessionReply)

    renderLogin()
    const field = screen.getByLabelText('Gateway key')
    await user.type(field, CREDENTIAL)
    expect(field).toHaveAttribute('type', 'password')

    await user.click(screen.getByRole('button', { name: 'Show key' }))
    expect(field).toHaveAttribute('type', 'text')

    await user.click(screen.getByRole('button', { name: 'Hide key' }))
    expect(field).toHaveAttribute('type', 'password')
  })

  it('sends an already signed-in operator on instead of offering a second sign-in form', async () => {
    saveSession({ token: SESSION_TOKEN, scopes: ['admin'], subject: 'Ops laptop' })
    const calls = stubGateway(() => ({ body: { status: 'ok', providers: [], scopes: ['admin'] } }))

    renderLogin()

    await waitFor(() => expect(landedOn()).toBe('/overview'))
    expect(screen.queryByLabelText('Gateway key')).toBeNull()
    // The restored session is verified with the token it already holds; the
    // sign-in exchange is not re-run.
    expect(calls.some((call) => call.method === 'POST')).toBe(false)
  })
})
