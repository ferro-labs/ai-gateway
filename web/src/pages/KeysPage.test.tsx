import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider } from '../auth/AuthProvider'
import { clearSession, configureGateway, saveSession } from '../lib/api'
import type { APIKey, DashboardSession, Scope } from '../types'
import type { Call, Reply } from '../test/stubs'
import { memoryStorage, stubGateway } from '../test/stubs'
import KeysPage from './KeysPage'

/*
 * The gateway is stubbed at `fetch` rather than at the api module, so the real
 * request builder, the real session handling and the real query strings are all
 * under test. Several of the behaviours here are *about* the query string —
 * which endpoint is called, with which sort and which offset — and a mocked
 * `request` would assert only that the page called a function.
 */

function apiKey(overrides: Partial<APIKey> = {}): APIKey {
  return {
    id: 'key-1',
    key: 'fgw_abcdefgh1234',
    name: 'Ops laptop',
    scopes: ['admin'],
    created_at: '2026-07-01T09:00:00Z',
    usage_count: 12,
    active: true,
    ...overrides,
  }
}

function dashboardSession(overrides: Partial<DashboardSession> = {}): DashboardSession {
  return {
    id: 'sess-1',
    // Deliberately not the default key's id or name: several tests wait on the
    // key name, and a session echoing it would make those waits ambiguous.
    credential_id: 'key-elsewhere',
    subject: 'Console session',
    scopes: ['admin'],
    created_at: '2026-07-20T09:00:00Z',
    last_seen_at: '2026-07-20T10:00:00Z',
    expires_at: '2026-07-21T09:00:00Z',
    ...overrides,
  }
}

function usageBody(keys: APIKey[], total = keys.length) {
  return {
    data: keys,
    summary: {
      total_keys: total,
      active_keys: keys.filter((key) => key.active).length,
      total_usage: keys.reduce((sum, key) => sum + key.usage_count, 0),
      returned_keys: keys.length,
    },
  }
}

/** The read paths every test needs, so each one only writes its own answers. */
function reads(options: { keys?: APIKey[]; total?: number; sessions?: DashboardSession[]; scopes?: Scope[] } = {}) {
  const keys = options.keys ?? [apiKey()]
  const sessions = options.sessions ?? [dashboardSession()]
  return (call: Call): Reply | null => {
    if (call.url.startsWith('/admin/health')) return { body: { status: 'ok', providers: [], scopes: options.scopes ?? ['admin'] } }
    if (call.url.startsWith('/admin/keys/usage')) return { body: usageBody(keys, options.total ?? keys.length) }
    if (call.url.startsWith('/admin/sessions')) return { body: { data: sessions } }
    return null
  }
}

function renderPage() {
  return render(
    <AuthProvider>
      <KeysPage />
    </AuthProvider>,
  )
}

/** Query strings are built from a URLSearchParams, so order is not guaranteed. */
function lastUsageCall(calls: Call[]): URLSearchParams {
  const usageCalls = calls.filter((call) => call.url.startsWith('/admin/keys/usage'))
  const last = usageCalls[usageCalls.length - 1]
  return new URLSearchParams(last?.url.split('?')[1] ?? '')
}

describe('KeysPage', () => {
  beforeEach(() => {
    configureGateway('')
    vi.stubGlobal('localStorage', memoryStorage())
    vi.stubGlobal('sessionStorage', memoryStorage())
    clearSession()
    saveSession({ token: 'session-token', scopes: ['admin'], subject: 'Ops laptop' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('pages through the sorted usage endpoint rather than the unordered key list', async () => {
    const user = userEvent.setup()
    const keys = Array.from({ length: 20 }, (_, index) => apiKey({ id: `key-${index}`, name: `Key ${index}`, usage_count: 100 - index }))
    const calls = stubGateway((call) => reads({ keys, total: 44 })(call) ?? { body: {} })

    renderPage()
    await screen.findByText('Key 0')

    const first = lastUsageCall(calls)
    expect(first.get('limit')).toBe('20')
    expect(first.get('offset')).toBe('0')
    expect(first.get('sort')).toBe('usage')
    // The unpaginated list is what this page must stop calling: it returns every
    // row, in map order, so two refreshes can disagree about the order.
    expect(calls.some((call) => /^\/admin\/keys(\?|$)/.test(call.url))).toBe(false)

    expect(screen.getByText(/Showing 1–20 of 44/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Next' }))
    await waitFor(() => expect(lastUsageCall(calls).get('offset')).toBe('20'))
  })

  it('returns to the first page when the sort changes', async () => {
    const user = userEvent.setup()
    const keys = Array.from({ length: 20 }, (_, index) => apiKey({ id: `key-${index}`, name: `Key ${index}` }))
    const calls = stubGateway((call) => reads({ keys, total: 44 })(call) ?? { body: {} })

    renderPage()
    await screen.findByText('Key 0')
    await user.click(screen.getByRole('button', { name: 'Next' }))
    await waitFor(() => expect(lastUsageCall(calls).get('offset')).toBe('20'))

    await user.click(screen.getByLabelText('Sort keys'))
    await user.click(await screen.findByRole('option', { name: 'Recently used first' }))

    // Page 3 of "most used" is not page 3 of "recently used", so the offset has
    // to reset or the operator lands somewhere arbitrary in the new order.
    await waitFor(() => {
      const params = lastUsageCall(calls)
      expect(params.get('sort')).toBe('last_used')
      expect(params.get('offset')).toBe('0')
    })
  })

  it('reports a rejected creation inside the dialog, not behind its backdrop', async () => {
    const user = userEvent.setup()
    const calls = stubGateway((call) => {
      const read = reads()(call)
      if (read) return read
      if (call.method === 'POST') {
        return { status: 400, body: { error: { message: 'name is required' } } }
      }
      return { body: {} }
    })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Create key' }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Create key' }))

    // An empty name is refused before the request, and the refusal has to be
    // readable where the operator is looking — inside the open dialog.
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('A key name is required.')
    expect(calls.some((call) => call.method === 'POST')).toBe(false)

    await user.type(within(dialog).getByLabelText('Name'), 'Batch worker')
    await user.click(within(dialog).getByRole('button', { name: 'Create key' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('name is required')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('promotes a read-only key by sending the edited scopes', async () => {
    const user = userEvent.setup()
    const keys = [apiKey({ id: 'key-9', name: 'Reader', scopes: ['read_only'] })]
    const calls = stubGateway((call) => reads({ keys })(call) ?? { body: apiKey() })

    renderPage()
    await screen.findByText('Reader')
    await user.click(screen.getByRole('button', { name: 'Edit Reader' }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByLabelText(/Admin/))
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    await waitFor(() => {
      const put = calls.find((call) => call.method === 'PUT')
      expect(put?.url).toBe('/admin/keys/key-9')
      expect(JSON.parse(put?.body ?? '{}')).toMatchObject({ name: 'Reader', scopes: ['read_only', 'admin'] })
    })
  })

  it('revokes one dashboard session without signing everyone out', async () => {
    const user = userEvent.setup()
    const sessions = [
      dashboardSession({ id: 'sess-1', subject: 'This browser', credential_id: 'key-1' }),
      dashboardSession({ id: 'sess-2', subject: 'On-call phone' }),
    ]
    const calls = stubGateway((call) => {
      const read = reads({ sessions })(call)
      if (read) return read
      if (call.method === 'DELETE') return { status: 204 }
      return { body: {} }
    })

    renderPage()
    const row = await screen.findByRole('row', { name: /This browser/ })
    // The session carries a credential id, not a name; the name comes from the
    // loaded page of keys. A bare id tells an operator nothing about whose
    // session they are about to end.
    expect(within(row).getByText('Ops laptop')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Revoke session for On-call phone' }))
    const confirm = await screen.findByRole('alertdialog')
    expect(confirm).toHaveTextContent('On-call phone')
    await user.click(within(confirm).getByRole('button', { name: 'Revoke session' }))

    await waitFor(() => {
      expect(calls.some((call) => call.method === 'DELETE' && call.url === '/admin/sessions/sess-2')).toBe(true)
    })
    // The fleet-wide DELETE has no id segment. Reaching it from a per-row
    // control would sign every operator out, which is the whole reason this
    // control exists.
    expect(calls.some((call) => call.method === 'DELETE' && call.url === '/admin/sessions')).toBe(false)
  })

  it('asks the gateway for revoked keys rather than filtering the page it already holds', async () => {
    const user = userEvent.setup()
    const keys = Array.from({ length: 20 }, (_, index) => apiKey({ id: `key-${index}`, name: `Key ${index}` }))
    const calls = stubGateway((call) => reads({ keys, total: 44 })(call) ?? { body: {} })

    renderPage()
    await screen.findByText('Key 0')
    await user.click(screen.getByRole('button', { name: 'Next' }))
    await waitFor(() => expect(lastUsageCall(calls).get('offset')).toBe('20'))

    await user.click(screen.getByLabelText('Filter by state'))
    await user.click(await screen.findByRole('option', { name: 'Revoked only' }))

    await waitFor(() => {
      const params = lastUsageCall(calls)
      expect(params.get('active')).toBe('false')
      // A narrowed filter usually has fewer pages than the current offset
      // assumes, so page 2 of every key is nowhere in the revoked ones.
      expect(params.get('offset')).toBe('0')
    })

    await user.click(screen.getByLabelText('Filter by state'))
    await user.click(await screen.findByRole('option', { name: 'All keys' }))

    // Absent, not `active=`: the handler reads an empty value as a filter.
    await waitFor(() => expect(lastUsageCall(calls).has('active')).toBe(false))
  })

  it('shows the rotated secret once, because the gateway keeps only its hash', async () => {
    const user = userEvent.setup()
    const calls = stubGateway((call) => {
      const read = reads()(call)
      if (read) return read
      if (call.method === 'POST' && call.url === '/admin/keys/key-1/rotate') return { body: apiKey({ key: 'fgw_rotated_9f3c' }) }
      return { body: {} }
    })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Rotate Ops laptop' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Rotate key' }))

    const reveal = await screen.findByRole('dialog')
    expect(reveal).toHaveTextContent('API key rotated')
    // The full secret, not the masked form the table shows: this dialog is the
    // only place it is ever readable.
    expect(within(reveal).getByText('fgw_rotated_9f3c')).toBeInTheDocument()
    expect(calls.filter((call) => call.method === 'POST')).toHaveLength(1)
  })

  it('reports a refused revoke inside the dialog, where a 409 on this session\'s own credential can be read', async () => {
    const user = userEvent.setup()
    stubGateway((call) => {
      const read = reads()(call)
      if (read) return read
      if (call.method === 'POST') {
        return { status: 409, body: { error: { message: 'cannot revoke the credential this request authenticated with' } } }
      }
      return { body: {} }
    })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Revoke Ops laptop' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Revoke key' }))

    // A page-level notice sits behind this dialog's backdrop, where a refusal
    // is indistinguishable from the button doing nothing at all.
    await waitFor(() => expect(within(confirm).getByRole('alert')).toHaveTextContent('cannot revoke the credential this request authenticated with'))
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
  })

  it('reports a refused delete of the last admin key inside the dialog', async () => {
    const user = userEvent.setup()
    stubGateway((call) => {
      const read = reads()(call)
      if (read) return read
      if (call.method === 'DELETE') {
        return { status: 409, body: { error: { message: 'cannot delete the last admin key' } } }
      }
      return { body: {} }
    })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Delete Ops laptop' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Delete key' }))

    // Locking every operator out of the admin API is the one mistake this page
    // cannot help anybody recover from, so the refusal has to be readable.
    await waitFor(() => expect(within(confirm).getByRole('alert')).toHaveTextContent('cannot delete the last admin key'))
  })

  it('confirms a completed delete on the page, once the dialog is out of the way', async () => {
    const user = userEvent.setup()
    const calls = stubGateway((call) => {
      const read = reads()(call)
      if (read) return read
      if (call.method === 'DELETE') return { status: 204 }
      return { body: {} }
    })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Delete Ops laptop' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Delete key' }))

    expect(await screen.findByText('Ops laptop was deleted.')).toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(calls.some((call) => call.method === 'DELETE' && call.url === '/admin/keys/key-1')).toBe(true)
  })

  it('refuses to be dismissed by Escape or a stray click while a new secret is on screen', async () => {
    const user = userEvent.setup()
    stubGateway((call) => reads()(call) ?? { body: apiKey({ key: 'fgw_created_4b21' }) })

    renderPage()
    await screen.findByText('Ops laptop')

    // The form dialog is ordinary: Escape closes it, because nothing typed into
    // it is unrecoverable. That is the contrast the rest of this test is about.
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    await screen.findByRole('dialog')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    await user.click(screen.getByRole('button', { name: 'Create key' }))
    const form = await screen.findByRole('dialog')
    await user.type(within(form).getByLabelText('Name'), 'Batch worker')
    await user.click(within(form).getByRole('button', { name: 'Create key' }))

    const reveal = await screen.findByText('fgw_created_4b21')

    await user.keyboard('{Escape}')
    await user.click(document.body)
    // The gateway stores only the hash, so a casual dismissal destroys the only
    // copy of the key and it has to be rotated again. Closing stays possible,
    // but never accidental — which is also why there is no X in the corner.
    expect(reveal).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Close dialog' })).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByText('fgw_created_4b21')).toBeNull())
  })

  it('copies the new secret, and says so where the operator is looking', async () => {
    const user = userEvent.setup()
    stubGateway((call) => reads()(call) ?? { body: apiKey({ key: 'fgw_created_4b21' }) })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    const form = await screen.findByRole('dialog')
    await user.type(within(form).getByLabelText('Name'), 'Batch worker')
    await user.click(within(form).getByRole('button', { name: 'Create key' }))
    await screen.findByText('fgw_created_4b21')

    await user.click(screen.getByRole('button', { name: 'Copy API key' }))

    expect(await navigator.clipboard.readText()).toBe('fgw_created_4b21')
    // Announced, not just drawn: the icon swap says nothing to a reader, and
    // this is the one moment where "did that work?" cannot be re-checked.
    // Matched loosely because the announcement currently reads "Copy API key
    // copied" — the control's label doubles as the noun in the message.
    expect(await screen.findByText(/API key copied/)).toBeInTheDocument()
  })

  it('says the clipboard was refused rather than looking exactly like a successful copy', async () => {
    const user = userEvent.setup()
    stubGateway((call) => reads()(call) ?? { body: apiKey({ key: 'fgw_created_4b21' }) })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    const form = await screen.findByRole('dialog')
    await user.type(within(form).getByLabelText('Name'), 'Batch worker')
    await user.click(within(form).getByRole('button', { name: 'Create key' }))
    await screen.findByText('fgw_created_4b21')

    // Plain HTTP, or a browser that has not granted write permission: the call
    // rejects rather than doing nothing visible, and the one copy of the key is
    // on screen right now.
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('write permission denied'))
    await user.click(screen.getByRole('button', { name: 'Copy API key' }))

    expect(await screen.findByText(/Could not copy/)).toBeInTheDocument()
    expect(screen.getByText('fgw_created_4b21')).toBeInTheDocument()
  })

  it('dates a new key from the expiry chosen, and issues no expiry only when asked for none', async () => {
    const user = userEvent.setup()
    const calls = stubGateway((call) => reads()(call) ?? { body: apiKey({ key: 'fgw_created_4b21' }) })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    const form = await screen.findByRole('dialog')
    await user.type(within(form).getByLabelText('Name'), 'Batch worker')
    await user.click(within(form).getByLabelText('Expires'))
    await user.click(await screen.findByRole('option', { name: '7 days' }))
    await user.click(within(form).getByRole('button', { name: 'Create key' }))
    await screen.findByText('fgw_created_4b21')
    await user.click(screen.getByRole('button', { name: 'Done' }))

    const first = JSON.parse(calls.find((call) => call.method === 'POST')?.body ?? '{}') as { name: string; scopes: string[]; expires_at?: string }
    expect(first).toMatchObject({ name: 'Batch worker', scopes: ['admin'] })
    // Seven days from the moment Create was pressed, not from anything the
    // server guesses: an expiry is the one thing that limits a leaked key.
    const chosen = new Date(first.expires_at ?? '').getTime()
    expect(Math.abs(chosen - (Date.now() + 7 * 86400000))).toBeLessThan(60_000)

    await user.click(screen.getByRole('button', { name: 'Create key' }))
    const second = await screen.findByRole('dialog')
    await user.type(within(second).getByLabelText('Name'), 'Dashboard viewer')
    await user.click(within(second).getByLabelText(/Admin/))
    await user.click(within(second).getByLabelText(/Read only/))
    await user.click(within(second).getByLabelText('Expires'))
    await user.click(await screen.findByRole('option', { name: 'Never' }))
    await user.click(within(second).getByRole('button', { name: 'Create key' }))
    await screen.findByText('fgw_created_4b21')

    const posts = calls.filter((call) => call.method === 'POST')
    const body = JSON.parse(posts[posts.length - 1]?.body ?? '{}') as { scopes: string[]; expires_at?: string }
    // The field is absent rather than empty: a blank `expires_at` is a date the
    // handler cannot parse, not a key that never expires.
    expect(body).toEqual({ name: 'Dashboard viewer', scopes: ['read_only'] })
  })

  it('clears a stored expiry outright, rather than pushing it further out', async () => {
    const user = userEvent.setup()
    const keys = [apiKey({ id: 'key-9', name: 'Reader', expires_at: '2026-08-01T09:00:00Z' })]
    const calls = stubGateway((call) => reads({ keys })(call) ?? { body: apiKey() })

    renderPage()
    await screen.findByText('Reader')
    await user.click(screen.getByRole('button', { name: 'Edit Reader' }))

    const dialog = await screen.findByRole('dialog')
    await user.clear(within(dialog).getByLabelText('Name'))
    await user.type(within(dialog).getByLabelText('Name'), 'Reader (permanent)')
    await user.click(within(dialog).getByLabelText('Expires'))
    await user.click(await screen.findByRole('option', { name: 'Never expires' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    await waitFor(() => {
      const put = calls.find((call) => call.method === 'PUT')
      // `clear_expiration` and an absent `expires_at`: sending a far-future date
      // instead would leave the key expiring, just later.
      expect(JSON.parse(put?.body ?? '{}')).toEqual({ name: 'Reader (permanent)', scopes: ['admin'], clear_expiration: true })
    })
    expect(await screen.findByText('Reader (permanent) was updated.')).toBeInTheDocument()
  })

  it('leaves the credential untouched when the operator backs out of a dialog', async () => {
    const user = userEvent.setup()
    const calls = stubGateway((call) => reads()(call) ?? { body: {} })

    renderPage()
    await screen.findByText('Ops laptop')

    await user.click(screen.getByRole('button', { name: 'Create key' }))
    const form = await screen.findByRole('dialog')
    await user.type(within(form).getByLabelText('Name'), 'Abandoned')
    await user.click(within(form).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    await user.click(screen.getByRole('button', { name: 'Delete Ops laptop' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())

    // Backing out of a confirmation is the operator saying no, and this page's
    // two destructive actions cannot be undone once they land.
    expect(calls.every((call) => call.method === 'GET')).toBe(true)
    expect(screen.getByText('Ops laptop')).toBeInTheDocument()
  })

  it('refuses to save an edit that clears every scope, which the gateway would read as no change', async () => {
    const user = userEvent.setup()
    const calls = stubGateway((call) => reads()(call) ?? { body: apiKey() })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Edit Ops laptop' }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByLabelText(/Admin/))
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    // An empty list means "leave scopes alone" on the wire, so this would have
    // saved the name and quietly kept the scopes the operator just cleared.
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Select at least one scope')
    expect(calls.some((call) => call.method === 'PUT')).toBe(false)
  })

  it('reports a refused de-scope inside the edit dialog rather than behind it', async () => {
    const user = userEvent.setup()
    stubGateway((call) => {
      const read = reads()(call)
      if (read) return read
      if (call.method === 'PUT') {
        return { status: 409, body: { error: { message: 'cannot remove admin from the last admin key' } } }
      }
      return { body: {} }
    })

    renderPage()
    await screen.findByText('Ops laptop')
    await user.click(screen.getByRole('button', { name: 'Edit Ops laptop' }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByLabelText(/Read only/))
    await user.click(within(dialog).getByLabelText(/Admin/))
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('cannot remove admin from the last admin key')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('shows a read-only operator the credentials but none of the controls', async () => {
    const calls = stubGateway((call) => reads({ scopes: ['read_only'] })(call) ?? { body: {} })

    renderPage()
    await screen.findByText('Ops laptop')

    expect(screen.getByText(/Read-only sessions can inspect keys/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Create key' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Edit Ops laptop' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Revoke Ops laptop' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Delete Ops laptop' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Sign out all sessions' })).toBeNull()
    expect(screen.queryByRole('button', { name: /Revoke session for/ })).toBeNull()
    expect(calls.every((call) => call.method === 'GET')).toBe(true)
  })
})
