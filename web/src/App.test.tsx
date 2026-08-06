import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import App from './App'
import { ROUTES } from './lib/routes'
import { memoryStorage, stubMatchMedia } from './test/stubs'

/*
 * The real pages each open their own request to the gateway on mount, and none
 * of that is what this file is about: the question here is only which page a
 * URL reaches. Every page is stubbed with a heading naming the path it is
 * supposed to serve, so a route that silently falls through to the catch-all
 * shows up as the wrong heading rather than as a passing test.
 */
vi.mock('./pages/LoginPage', () => ({ default: () => <h1>/login page</h1> }))
vi.mock('./pages/GettingStartedPage', () => ({ default: () => <h1>/getting-started page</h1> }))
vi.mock('./pages/OverviewPage', () => ({ default: () => <h1>/overview page</h1> }))
vi.mock('./pages/KeysPage', () => ({ default: () => <h1>/keys page</h1> }))
vi.mock('./pages/LogsPage', () => ({ default: () => <h1>/logs page</h1> }))
vi.mock('./pages/AuditPage', () => ({ default: () => <h1>/audit page</h1> }))
vi.mock('./pages/ProvidersPage', () => ({ default: () => <h1>/providers page</h1> }))
vi.mock('./pages/StrategyPage', () => ({ default: () => <h1>/strategy page</h1> }))
vi.mock('./pages/PluginsPage', () => ({ default: () => <h1>/plugins page</h1> }))
vi.mock('./pages/TracingPage', () => ({ default: () => <h1>/tracing page</h1> }))
vi.mock('./pages/ConfigPage', () => ({ default: () => <h1>/config page</h1> }))
vi.mock('./pages/AnalyticsPage', () => ({ default: () => <h1>/analytics page</h1> }))
vi.mock('./pages/PlaygroundPage', () => ({ default: () => <h1>/playground page</h1> }))

// A verified session, so routing is what is under test rather than the auth
// round trip — ProtectedRoute.test.tsx covers the unverified cases.
vi.mock('./auth/AuthProvider', () => ({
  AuthProvider: ({ children }: { children: ReactNode }) => children,
  useAuth: () => ({
    status: 'authenticated',
    session: null,
    isAdmin: true,
    subject: 'ops-laptop',
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

function visit(path: string) {
  window.history.pushState({}, '', path)
  return render(<App />)
}

/** The heading the stub for `path` renders once its lazy chunk has resolved. */
function page(path: string) {
  return screen.findByRole('heading', { name: `${path} page` })
}

beforeEach(() => {
  vi.stubGlobal('localStorage', memoryStorage())
  stubMatchMedia()
  window.history.pushState({}, '', '/')
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('App routing', () => {
  it('lands a new arrival on Getting Started rather than an empty index route', async () => {
    visit('/')

    expect(await page('/getting-started')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/getting-started')
  })

  it('sends an unknown path to the overview instead of leaving a blank screen', async () => {
    visit('/keys/47/does-not-exist')

    expect(await page('/overview')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/overview')
  })

  /*
   * Driven from ROUTES because that list is what the sidebar renders: a link
   * added there but never given a <Route> here still looks right in the nav and
   * bounces the operator to the overview when clicked.
   */
  it.each(ROUTES.map((route) => route.path))('serves %s from its own page, not the catch-all', async (path) => {
    visit(path)

    expect(await page(path)).toBeInTheDocument()
    expect(window.location.pathname).toBe(path)
  })

  it('renders the sign-in page without the dashboard chrome around it', async () => {
    visit('/login')

    expect(await page('/login')).toBeInTheDocument()
    // Navigation an unauthenticated visitor cannot use is worse than no
    // navigation: every link leads back to the form they are already on.
    expect(screen.queryByRole('navigation', { name: 'Dashboard navigation' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Logout' })).toBeNull()
  })

  it('wraps the dashboard pages in the shell, so navigation is reachable from any of them', async () => {
    visit('/keys')

    expect(await page('/keys')).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Dashboard navigation' })).toBeInTheDocument()
  })

  it('says the dashboard is loading while a page chunk is still in flight', async () => {
    // The chunk is held open deliberately: every other page stub here resolves
    // on the first microtask, which is exactly the window the fallback exists to
    // cover on a real network. Without it the operator gets an empty frame.
    let arrive = () => {}
    const chunk = new Promise<void>((resolve) => { arrive = resolve })
    vi.doMock('./pages/LogsPage', async () => {
      await chunk
      return { default: () => <h1>/logs page</h1> }
    })
    vi.resetModules()
    const { default: SlowApp } = await import('./App')

    window.history.pushState({}, '', '/logs')
    render(<SlowApp />)

    expect(screen.getByRole('status')).toHaveTextContent('Loading dashboard')

    arrive()
    expect(await page('/logs')).toBeInTheDocument()
    vi.doUnmock('./pages/LogsPage')
  })

  it('honours a non-root deployment base, so a gateway served under a sub-path routes at all', async () => {
    vi.stubEnv('BASE_URL', '/console/')
    vi.resetModules()
    const { default: BasedApp } = await import('./App')

    window.history.pushState({}, '', '/console/keys')
    render(<BasedApp />)

    expect(await page('/keys')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/console/keys')
  })
})
