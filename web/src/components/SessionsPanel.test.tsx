import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../lib/api'
import type * as ApiModule from '../lib/api'
import type { DashboardSession } from '../types'
import { SessionsPanel } from './SessionsPanel'

/*
 * `request` is replaced but `ApiError` is not: the panel decides that a 501 is a
 * deployment choice rather than a fault with an `instanceof` check, which a
 * stubbed class would silently fail. LogsPage's suite mocks the module the same
 * way and for the same reason.
 */
const request = vi.hoisted(() => vi.fn())
const isAdmin = vi.hoisted(() => ({ value: true }))

vi.mock('../lib/api', async (importOriginal) => ({
  ...(await importOriginal<typeof ApiModule>()),
  request,
}))

vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({ isAdmin: isAdmin.value }),
}))

function session(overrides: Partial<DashboardSession> = {}): DashboardSession {
  return {
    id: 'sess-1',
    credential_id: 'key-1',
    subject: 'Ops laptop',
    scopes: ['admin'],
    created_at: '2026-07-20T09:00:00Z',
    last_seen_at: '2026-07-20T10:00:00Z',
    expires_at: '2026-07-21T09:00:00Z',
    ...overrides,
  }
}

/**
 * Answers the panel's one load; every mutation is opt-in per test.
 *
 * `sessions` may be a function so a test can change what the next load returns —
 * the panel re-queries after a sign-out, and what comes back then is what the
 * operator is left looking at.
 */
function respondToLoad(
  sessions: DashboardSession[] | (() => DashboardSession[]),
  mutations: (path: string, init?: RequestInit) => Promise<unknown> = () => Promise.reject(new Error('unexpected call')),
) {
  request.mockImplementation((path: string, init?: RequestInit) => {
    if ((init?.method ?? 'GET') === 'GET' && path === '/admin/sessions') {
      return Promise.resolve({ data: typeof sessions === 'function' ? sessions() : sessions })
    }
    return mutations(path, init)
  })
}

/** The credential names the keys page has loaded, which is what names a session. */
const loadedKeyNames = new Map([['key-1', 'Ops laptop']])

function renderPanel(keyNames: ReadonlyMap<string, string> = loadedKeyNames) {
  const onNotice = vi.fn()
  const onSignOut = vi.fn()
  render(<SessionsPanel keyNames={keyNames} reloadSignal={0} onNotice={onNotice} onSignOut={onSignOut} />)
  return { onNotice, onSignOut }
}

/** Every path a mutation was sent to, with its method. */
function mutations(): string[] {
  const calls = request.mock.calls as Array<[string, RequestInit | undefined]>
  return calls
    .filter(([, init]) => (init?.method ?? 'GET') !== 'GET')
    .map(([path, init]) => `${init?.method ?? 'GET'} ${path}`)
}

beforeEach(() => {
  isAdmin.value = true
  request.mockReset()
  respondToLoad([session()])
})

describe('SessionsPanel credentials', () => {
  it('names the key a session was minted from, and falls back to its id when that key is not loaded', async () => {
    respondToLoad([
      session({ id: 'sess-1', subject: 'This browser', credential_id: 'key-1' }),
      // MASTER_KEY has no key row at all, and a key on another page of the table
      // is equally unnameable. Either way a blank cell would read as "no
      // credential" rather than "not on screen".
      session({ id: 'sess-2', subject: 'Break-glass', credential_id: 'master-key' }),
    ])
    renderPanel()

    const named = await screen.findByRole('row', { name: /This browser/ })
    expect(within(named).getByText('Ops laptop')).toBeInTheDocument()

    const unnamed = screen.getByRole('row', { name: /Break-glass/ })
    expect(within(unnamed).getByText('master-key')).toBeInTheDocument()
  })

  it('reads last seen as elapsed time, so an abandoned token stands out from an operator working now', async () => {
    respondToLoad([
      session({ id: 'sess-1', subject: 'On-call phone', last_seen_at: new Date(Date.now() - 3 * 3600_000).toISOString() }),
      // Minted and never used since: "Never" is the answer, and a dash would
      // read as a missing field rather than an unused session.
      session({ id: 'sess-2', subject: 'Fresh sign-in', last_seen_at: undefined }),
    ])
    renderPanel()

    const active = await screen.findByRole('row', { name: /On-call phone/ })
    expect(within(active).getByText('3h ago')).toBeInTheDocument()

    const unused = screen.getByRole('row', { name: /Fresh sign-in/ })
    expect(within(unused).getByText('Never')).toBeInTheDocument()
  })
})

describe('SessionsPanel per-session revoke', () => {
  it('ends one sign-in and drops the row it ended', async () => {
    const user = userEvent.setup()
    let remaining = [session({ id: 'sess-1', subject: 'This browser' }), session({ id: 'sess-2', subject: 'On-call phone' })]
    respondToLoad(() => remaining, (path, init) => {
      if (init?.method === 'DELETE' && path === '/admin/sessions/sess-2') {
        remaining = remaining.filter((entry) => entry.id !== 'sess-2')
        return Promise.resolve(undefined)
      }
      return Promise.reject(new Error('unexpected call'))
    })
    const { onNotice, onSignOut } = renderPanel()
    await screen.findByRole('row', { name: /On-call phone/ })

    await user.click(screen.getByRole('button', { name: 'Revoke session for On-call phone' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Revoke session' }))

    await waitFor(() => expect(screen.queryByRole('row', { name: /On-call phone/ })).toBeNull())
    // One stolen laptop costs one session: the operator who was not signed out
    // is still listed, and the fleet-wide DELETE was never reached.
    expect(screen.getByRole('row', { name: /This browser/ })).toBeInTheDocument()
    expect(mutations()).toEqual(['DELETE /admin/sessions/sess-2'])
    expect(onNotice).toHaveBeenCalledWith('On-call phone was signed out.')
    // The page re-runs its own queries, which is what surfaces this browser's
    // own session ending — the next request 401s.
    expect(onSignOut).toHaveBeenCalled()
  })

  it('reports a refused sign-out inside the dialog rather than behind it', async () => {
    const user = userEvent.setup()
    respondToLoad([session({ id: 'sess-1', subject: 'On-call phone' })], () =>
      Promise.reject(new ApiError('session store is unavailable', 503)))
    const { onNotice } = renderPanel()
    await screen.findByRole('row', { name: /On-call phone/ })

    await user.click(screen.getByRole('button', { name: 'Revoke session for On-call phone' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Revoke session' }))

    // The dialog's own backdrop covers the page, so a failure rendered underneath
    // it is unreadable and looks exactly like the button doing nothing.
    await waitFor(() => expect(within(dialog).getByRole('alert')).toHaveTextContent('session store is unavailable'))
    expect(onNotice).not.toHaveBeenCalled()

    // Dismissing is still possible after the failure, and the session that was
    // not signed out is still listed underneath.
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.getByRole('row', { name: /On-call phone/ })).toBeInTheDocument())
  })
})

describe('SessionsPanel sign out everyone', () => {
  it('ends every sign-in, including this browser, and says how many', async () => {
    const user = userEvent.setup()
    let remaining = [session({ id: 'sess-1', subject: 'This browser' }), session({ id: 'sess-2', subject: 'On-call phone' })]
    respondToLoad(() => remaining, (path, init) => {
      if (init?.method === 'DELETE' && path === '/admin/sessions') {
        remaining = []
        return Promise.resolve({ revoked: 2 })
      }
      return Promise.reject(new Error('unexpected call'))
    })
    const { onNotice, onSignOut } = renderPanel()
    await screen.findByRole('row', { name: /On-call phone/ })

    await user.click(screen.getByRole('button', { name: 'Sign out all sessions' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Sign out everyone' }))

    await waitFor(() => expect(onNotice).toHaveBeenCalledWith(expect.stringContaining('2 dashboard session(s) were signed out')))
    // Nothing tries to keep this page alive past the sign-out: it reloads with a
    // dead token, and the 401 that follows is what returns to the sign-in screen.
    expect(onSignOut).toHaveBeenCalled()
    expect(mutations()).toEqual(['DELETE /admin/sessions'])
    expect(await screen.findByText('No active sessions')).toBeInTheDocument()
  })

  it('keeps a refused fleet sign-out in the dialog and leaves every session listed', async () => {
    const user = userEvent.setup()
    respondToLoad([session({ subject: 'On-call phone' })], () =>
      Promise.reject(new ApiError('sessions cannot be revoked in bulk', 500)))
    renderPanel()
    await screen.findByRole('row', { name: /On-call phone/ })

    await user.click(screen.getByRole('button', { name: 'Sign out all sessions' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Sign out everyone' }))

    await waitFor(() => expect(within(dialog).getByRole('alert')).toHaveTextContent('sessions cannot be revoked in bulk'))

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    // Nobody was signed out, so nobody has disappeared from the roster either.
    await waitFor(() => expect(screen.getByRole('row', { name: /On-call phone/ })).toBeInTheDocument())
  })
})

describe('SessionsPanel without a session store', () => {
  it('explains a 501 instead of reporting a fault, and withdraws the sign-out control', async () => {
    request.mockRejectedValue(new ApiError('dashboard sessions are not configured', 501))
    renderPanel()

    expect(await screen.findByText('Dashboard sessions are disabled')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    // A control whose only possible outcome is the same 501 is worse than none.
    expect(screen.queryByRole('button', { name: 'Sign out all sessions' })).toBeNull()
  })

  it('reports a session store that failed, rather than showing an empty roster', async () => {
    request.mockRejectedValue(new ApiError('sessions could not be read', 500))
    renderPanel()

    // "No operator is signed in" and "we could not find out" are different
    // answers, and only one of them is a reason to look at the gateway.
    expect(await screen.findByRole('alert')).toHaveTextContent('sessions could not be read')
    expect(screen.queryByText('No active sessions')).toBeNull()
  })
})
