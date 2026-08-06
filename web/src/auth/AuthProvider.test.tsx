import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, useAuth } from './AuthProvider'
import { clearSession, configureGateway, request, saveSession } from '../lib/api'
import { resetPluginCatalog, usePluginCatalog } from '../lib/plugins'
import type { Call } from '../test/stubs'
import { memoryStorage, stubGateway } from '../test/stubs'

/*
 * The gateway is stubbed at `fetch` so the real request builder, the real
 * session handling and the real events stay under test. Both claims here are
 * about state that outlives one page: a module-scope cache, and a scope
 * snapshot taken at sign-in.
 */

const CATALOG_PATH = '/admin/plugins/catalog'

function callsTo(calls: Call[], path: string): Call[] {
  return calls.filter((call) => call.url.startsWith(path))
}

/** Renders the catalog hook plus the sign-out control, which is all these need. */
function CatalogConsumer() {
  const catalog = usePluginCatalog()
  const { logout } = useAuth()
  return (
    <div>
      <span data-testid="catalog">{catalog === null ? 'pending' : String(catalog.length)}</span>
      <button onClick={() => void logout()} type="button">Sign out</button>
    </div>
  )
}

function ScopeConsumer() {
  const { isAdmin, status } = useAuth()
  return (
    <div>
      <span data-testid="status">{status}</span>
      <span data-testid="admin">{String(isAdmin)}</span>
      <button onClick={() => void request('/admin/keys', { method: 'POST' }).catch(() => {})} type="button">
        Create key
      </button>
    </div>
  )
}

describe('AuthProvider session state', () => {
  beforeEach(() => {
    vi.stubGlobal('localStorage', memoryStorage())
    vi.stubGlobal('sessionStorage', memoryStorage())
    clearSession()
    resetPluginCatalog()
    configureGateway('')
    vi.restoreAllMocks()
  })

  it('discards the plugin catalog on sign-out, so the next operator does not inherit it', async () => {
    // The catalog is cached at module scope for the lifetime of the tab. On a
    // shared operations workstation the next sign-in is a different operator
    // against a possibly different gateway, and without the reset they read the
    // previous session's copy.
    const calls = stubGateway((call) => {
      if (call.url.startsWith(CATALOG_PATH)) return { body: { data: [{ name: 'word-filter', type: 'guardrail', summary: '', settings: [], fails_open: false }] } }
      if (call.url.startsWith('/admin/session')) return { status: 204 }
      return { body: { status: 'ok', providers: [], scopes: ['admin'] } }
    })

    saveSession({ token: 'fgws_first', scopes: ['admin'] })
    const first = render(<AuthProvider><CatalogConsumer /></AuthProvider>)
    await waitFor(() => expect(screen.getByTestId('catalog')).toHaveTextContent('1'))
    expect(callsTo(calls, CATALOG_PATH)).toHaveLength(1)

    await userEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(callsTo(calls, '/admin/session')).toHaveLength(1))
    first.unmount()

    saveSession({ token: 'fgws_second', scopes: ['admin'] })
    render(<AuthProvider><CatalogConsumer /></AuthProvider>)
    await waitFor(() => expect(callsTo(calls, CATALOG_PATH)).toHaveLength(2))
    expect(callsTo(calls, CATALOG_PATH)[1]?.authorization).toBe('Bearer fgws_second')
  })

  it('re-derives scopes when a call is refused for lacking them', async () => {
    // isAdmin comes from a snapshot taken once at sign-in, so a credential
    // de-scoped mid-session left every admin control on screen — each one
    // answering 403 when used, with nothing on the page explaining why.
    let grantedScopes = ['admin']
    stubGateway((call) => {
      if (call.url.startsWith('/admin/health')) return { body: { status: 'ok', providers: [], scopes: grantedScopes } }
      if (call.url.startsWith('/admin/keys')) {
        return { status: 403, body: { error: { message: 'admin scope required', code: 'forbidden' } } }
      }
      return { body: {} }
    })

    saveSession({ token: 'fgws_abc', scopes: ['admin'] })
    render(<AuthProvider><ScopeConsumer /></AuthProvider>)
    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('authenticated'))
    expect(screen.getByTestId('admin')).toHaveTextContent('true')

    // The credential loses admin somewhere else — another operator's edit, a
    // rotation, an expiry on the underlying key's scopes.
    grantedScopes = ['read_only']
    await userEvent.click(screen.getByRole('button', { name: 'Create key' }))

    await waitFor(() => expect(screen.getByTestId('admin')).toHaveTextContent('false'))
    // Still signed in: a 403 says the credential cannot do this, not that it is
    // invalid. Signing the operator out over one would be the wrong remedy.
    expect(screen.getByTestId('status')).toHaveTextContent('authenticated')
  })
})
