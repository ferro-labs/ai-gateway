import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ROUTES } from '../lib/routes'
import { memoryStorage, stubMatchMedia } from '../test/stubs'
import { AppShell } from './AppShell'

/** The persisted keys are a contract with the previous build, so they are literal here. */
const THEME_KEY = 'ferrogw.dashboard.theme'
const SIDEBAR_KEY = 'ferrogw.dashboard.sidebar'

const auth = vi.hoisted(() => ({
  value: { isAdmin: true, subject: null as string | null, logout: vi.fn() },
}))

vi.mock('../auth/AuthProvider', () => ({ useAuth: () => auth.value }))

/** The shell plus a stand-in page per route, so navigation actually lands somewhere. */
function renderShell(entry = '/overview') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route element={<AppShell />}>
          {ROUTES.map((route) => (
            <Route element={<h1>{route.label} page</h1>} key={route.path} path={route.path} />
          ))}
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

function sidebar(): HTMLElement {
  return screen.getByRole('navigation', { name: 'Dashboard navigation' })
}

function themeButton(): HTMLElement {
  return screen.getByRole('button', { name: /^Theme:/ })
}

beforeEach(() => {
  vi.stubGlobal('localStorage', memoryStorage())
  stubMatchMedia(false)
  delete document.documentElement.dataset.theme
  auth.value = { isAdmin: true, subject: 'ops-laptop', logout: vi.fn() }
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('AppShell navigation', () => {
  it('offers every destination routes.ts declares, in the order it declares them', () => {
    renderShell()

    const labels = within(sidebar())
      .getAllByRole('link')
      .map((link) => link.textContent?.trim())
    expect(labels).toEqual(ROUTES.map((route) => route.label))
  })

  it('marks the destination being viewed as the current page', () => {
    renderShell('/logs')

    expect(within(sidebar()).getByRole('link', { name: 'Request Logs' })).toHaveAttribute(
      'aria-current',
      'page',
    )
    expect(within(sidebar()).getByRole('link', { name: 'Overview' })).not.toHaveAttribute(
      'aria-current',
    )
  })

  it('titles the browser tab from the same list the sidebar renders, so no page reads "Dashboard"', () => {
    for (const route of ROUTES) {
      const view = renderShell(route.path)

      expect(within(sidebar()).getByRole('link', { name: route.label })).toBeInTheDocument()
      expect(document.title).toBe(`${route.label} · Ferro AI Gateway`)

      view.unmount()
    }
  })

  it('puts a skip link ahead of the navigation so a keyboard reaches the page in one stop', async () => {
    const user = userEvent.setup()
    renderShell()

    const skip = screen.getByRole('link', { name: 'Skip to main content' })
    expect(skip).toHaveAttribute('href', '#main-content')
    // The target has to exist, or the link moves focus nowhere.
    expect(screen.getByRole('main')).toHaveAttribute('id', 'main-content')

    await user.tab()
    expect(skip).toHaveFocus()
  })
})

describe('AppShell theme control', () => {
  it('cycles system → light → dark → system, so a chosen theme can be handed back to the OS', async () => {
    const user = userEvent.setup()
    renderShell()

    expect(themeButton()).toHaveAccessibleName('Theme: system. Switch to light.')
    // Nothing is stored until the operator chooses; an empty slot means "system".
    expect(localStorage.getItem(THEME_KEY)).toBeNull()

    await user.click(themeButton())
    expect(themeButton()).toHaveAccessibleName('Theme: light. Switch to dark.')
    expect(localStorage.getItem(THEME_KEY)).toBe('light')

    await user.click(themeButton())
    expect(themeButton()).toHaveAccessibleName('Theme: dark. Switch to system.')
    expect(localStorage.getItem(THEME_KEY)).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')

    await user.click(themeButton())
    expect(themeButton()).toHaveAccessibleName('Theme: system. Switch to light.')
    // Cleared rather than stored as the word "system": the next boot has to read
    // it as no preference and consult the operating system again.
    expect(localStorage.getItem(THEME_KEY)).toBeNull()
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('opens on the theme the operator last chose rather than the machine preference', () => {
    stubMatchMedia(true)
    localStorage.setItem(THEME_KEY, 'light')

    renderShell()

    expect(themeButton()).toHaveAccessibleName('Theme: light. Switch to dark.')
    expect(document.documentElement.dataset.theme).toBe('light')
  })
})

describe('AppShell identity', () => {
  it('names the credential in use alongside its scope, not instead of it', () => {
    renderShell()

    // During an incident several operators share a screen; "Admin" alone does
    // not say which credential the audit trail is about to record.
    expect(screen.getByText('ops-laptop')).toBeInTheDocument()
    expect(screen.getByText('Admin')).toBeInTheDocument()
  })

  it('tells a read-only session it is read-only', () => {
    auth.value = { isAdmin: false, subject: 'viewer-key', logout: vi.fn() }

    renderShell()

    expect(screen.getByText('viewer-key')).toBeInTheDocument()
    expect(screen.getByText('Read only')).toBeInTheDocument()
    expect(screen.queryByText('Admin')).toBeNull()
  })

  it('still reports the scope for a session restored without a subject', () => {
    auth.value = { isAdmin: true, subject: null, logout: vi.fn() }

    renderShell()

    expect(screen.getByText('Admin')).toBeInTheDocument()
  })

  it('signs out through the auth provider', async () => {
    const user = userEvent.setup()
    renderShell()

    await user.click(screen.getByRole('button', { name: 'Logout' }))

    expect(auth.value.logout).toHaveBeenCalled()
  })
})

describe('AppShell sidebar collapse', () => {
  it('shrinks to an icon rail from the header toggle and remembers it across a reload', async () => {
    const user = userEvent.setup()
    const view = renderShell()
    expect(sidebar()).toHaveTextContent('Overview')

    await user.click(screen.getByRole('button', { name: 'Collapse sidebar' }))

    // Collapsed drops the visible labels, but every destination stays on screen
    // as a named, reachable icon — the rail is not hidden.
    expect(sidebar()).not.toHaveTextContent('Overview')
    expect(within(sidebar()).getByRole('link', { name: 'Overview' })).toBeInTheDocument()
    expect(localStorage.getItem(SIDEBAR_KEY)).toBe('collapsed')
    view.unmount()

    renderShell()

    expect(sidebar()).not.toHaveTextContent('Overview')
    expect(within(sidebar()).getByRole('link', { name: 'Overview' })).toBeInTheDocument()
  })

  it('leaves every collapsed control named, including the toggle back out of the rail', async () => {
    const user = userEvent.setup()
    renderShell()
    await user.click(screen.getByRole('button', { name: 'Collapse sidebar' }))

    const rail = screen.getByRole('complementary')
    const controls = [...within(rail).getAllByRole('link'), ...within(rail).getAllByRole('button')]
    const unnamed = controls.filter(
      (control) =>
        !(
          control.getAttribute('aria-label') ??
          control.getAttribute('title') ??
          control.textContent ??
          ''
        ).trim(),
    )

    // Collapsing hides the visible text of every control in the rail. Without a
    // label fallback a screen reader reaches anonymous controls, one of which is
    // the toggle that expands the rail again.
    expect(unnamed).toHaveLength(0)
    expect(within(rail).getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument()
  })
})

describe('AppShell mobile drawer', () => {
  it('opens the drawer, advertises that it is open, and closes it on navigation', async () => {
    const user = userEvent.setup()
    renderShell('/overview')
    const trigger = screen.getByRole('button', { name: 'Open navigation' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')

    await user.click(trigger)

    const drawer = await screen.findByRole('dialog')
    expect(trigger).toHaveAttribute('aria-expanded', 'true')

    await user.click(within(drawer).getByRole('link', { name: 'Request Logs' }))

    // Left open, the drawer would cover the page the operator just asked for.
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByRole('heading', { name: 'Request Logs page' })).toBeInTheDocument()
  })

  it('offers no rail-collapse control in the drawer and closes without touching the desktop state', async () => {
    const user = userEvent.setup()
    renderShell('/overview')

    await user.click(screen.getByRole('button', { name: 'Open navigation' }))
    const drawer = await screen.findByRole('dialog')
    // The dismiss control says what it closes rather than just "Close".
    expect(within(drawer).getByRole('button', { name: 'Close navigation' })).toBeInTheDocument()
    // A rail-collapse toggle here would shrink a desktop rail the phone never
    // shows, so the drawer offers none.
    expect(within(drawer).queryByRole('button', { name: 'Collapse sidebar' })).toBeNull()

    await user.click(within(drawer).getByRole('button', { name: 'Close navigation' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.getByRole('heading', { name: 'Overview page' })).toBeInTheDocument()
    expect(sidebar()).toHaveTextContent('Overview')
    expect(localStorage.getItem(SIDEBAR_KEY)).toBe('expanded')
  })
})
