import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, request } from '../lib/api'
import type * as ApiModule from '../lib/api'
import type { AuditEntry, AuditResponse } from '../types'
import AuditPage from './AuditPage'

vi.mock('../lib/api', async (importOriginal) => ({
  // `ApiError` stays real: the page decides a 501 is a deployment choice with
  // an `instanceof` check, which a stubbed class would silently fail.
  ...(await importOriginal<typeof ApiModule>()),
  request: vi.fn(),
}))

const requestMock = vi.mocked(request)

function entry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
    occurred_at: '2026-07-23T10:00:00Z',
    action: 'key.create',
    actor: 'Ops laptop (key-7f2)',
    actor_id: 'key-7f2',
    target_id: 'key-91a',
    outcome: 'ok',
    source_ip: '10.0.0.4',
    trace_id: 'trace-abc',
    ...overrides,
  }
}

function auditResponse(rows: AuditEntry[], total = rows.length): AuditResponse {
  return { data: rows, summary: { total_entries: total, returned_entries: rows.length }, filters: {} }
}

/** Surfaces the location so URL state can be asserted, not inferred. */
function LocationProbe() {
  const { pathname, search } = useLocation()
  return <span data-testid="location">{`${pathname}${search}`}</span>
}

function renderPage(initialEntry = '/audit') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route
          element={
            <>
              <AuditPage />
              <LocationProbe />
            </>
          }
          path="/audit"
        />
        {/* Real route so a trace link can be followed rather than merely read. */}
        <Route element={<LocationProbe />} path="/logs" />
      </Routes>
    </MemoryRouter>,
  )
}

/** The request paths the page issued, in order. */
function paths(): string[] {
  return requestMock.mock.calls.map((call) => String(call[0]))
}

beforeEach(() => {
  requestMock.mockReset()
  requestMock.mockResolvedValue(auditResponse([entry()]))
})


describe('AuditPage', () => {
  it('builds the query from the URL, so a shared investigation survives a reload', async () => {
    renderPage('/audit?action=key.delete&actor_id=key-7f2&outcome=denied&hours=1&offset=50')

    await waitFor(() => expect(requestMock).toHaveBeenCalled())
    const params = new URLSearchParams(paths()[0]?.split('?')[1])
    expect(params.get('action')).toBe('key.delete')
    expect(params.get('actor_id')).toBe('key-7f2')
    expect(params.get('outcome')).toBe('denied')
    expect(params.get('offset')).toBe('50')
    expect(params.get('limit')).toBe('50')
    // `hours=1` is a client-side range, sent as the absolute `since` the
    // gateway reads.
    expect(params.get('since')).toBeTruthy()
  })

  it('drops an outcome the gateway does not accept, which would be a 400 rather than a page', async () => {
    renderPage('/audit?outcome=refused')

    await waitFor(() => expect(requestMock).toHaveBeenCalled())
    expect(paths()[0]).not.toContain('outcome=')
  })

  it('writes applied filters back to the URL and returns to the first page', async () => {
    const user = userEvent.setup()
    renderPage('/audit?offset=50')
    await screen.findByText('key.create')

    await user.type(screen.getByPlaceholderText('Action, e.g. key.create'), 'session.create')
    await user.type(screen.getByPlaceholderText('Actor ID'), 'key-7f2')
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    const location = screen.getByTestId('location').textContent ?? ''
    expect(location).toContain('action=session.create')
    expect(location).toContain('actor_id=key-7f2')
    // Staying on page 2 of the previous filter shows an empty page for a filter
    // that has matches.
    expect(location).not.toContain('offset=')
  })

  it('sends a chosen outcome to the gateway', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('key.create')

    await user.click(screen.getByRole('combobox', { name: 'Outcome' }))
    await user.click(await screen.findByRole('option', { name: 'Denied' }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(paths().some((path) => path.includes('outcome=denied'))).toBe(true))
    expect(screen.getByTestId('location').textContent).toContain('outcome=denied')
  })

  it('marks a denied action for attention without calling it a failure', async () => {
    requestMock.mockResolvedValue(
      auditResponse([
        entry({ action: 'key.delete', outcome: 'denied' }),
        entry({ action: 'config.rollback', outcome: 'error', trace_id: 'trace-err' }),
      ]),
    )
    renderPage()

    const denied = await screen.findByText('Denied')
    const failed = screen.getByText('Error')
    // A refusal is the gateway working: it must not wear the tone reserved for
    // an action that was permitted and then broke.
    expect(denied.className).toContain('warning')
    expect(denied.className).not.toContain('danger')
    expect(failed.className).toContain('danger')
    // Nothing on the page claims the load itself failed.
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('expands a row to show its stored detail, and offers no control when there is none', async () => {
    const user = userEvent.setup()
    requestMock.mockResolvedValue(
      auditResponse([
        entry({ detail: '{"name":"Production application"}' }),
        entry({ action: 'key.revoke', detail: undefined, target_id: 'key-52c' }),
      ]),
    )
    renderPage()
    await screen.findByText('key.revoke')

    const toggles = screen.getAllByRole('button', { name: /detail for/ })
    expect(toggles).toHaveLength(1)
    const toggle = toggles[0]!
    expect(toggle).toHaveAttribute('aria-expanded', 'false')

    await user.click(toggle)

    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText(/"name": "Production application"/)).toBeInTheDocument()
  })

  it('links a trace to the request logs so the same request can be read there', async () => {
    const user = userEvent.setup()
    renderPage()

    const link = await screen.findByRole('link', { name: 'trace-abc' })
    expect(link).toHaveAttribute('href', '/logs?q=trace-abc')

    await user.click(link)
    expect(screen.getByTestId('location').textContent).toBe('/logs?q=trace-abc')
  })

  it('shows the actor id alongside the display name, since only the id is stable', async () => {
    renderPage()

    expect(await screen.findByText('Ops laptop (key-7f2)')).toBeInTheDocument()
    expect(screen.getByText('key-7f2')).toBeInTheDocument()
  })

  it('explains a 501 as an unconfigured store instead of reporting a fault', async () => {
    requestMock.mockRejectedValue(new ApiError('audit trail storage is not enabled', 501))
    renderPage()

    expect(await screen.findByText('The audit trail is not being recorded')).toBeInTheDocument()
    expect(screen.getByText(/API_KEY_STORE_BACKEND/)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    // The filter form promises a query the gateway will not answer.
    expect(screen.queryByRole('button', { name: 'Apply' })).toBeNull()
  })

  it('reports a genuine failure as an error', async () => {
    requestMock.mockRejectedValue(new ApiError('audit store unreachable', 500))
    renderPage()

    expect(await screen.findByRole('alert')).toHaveTextContent('audit store unreachable')
  })

  it('shows a detail that no longer parses verbatim, since the characters are the evidence', async () => {
    const user = userEvent.setup()
    // The server composes this as JSON and then redacts it, and a substitution
    // inside a quoted value can leave text that no longer parses. Swallowing it
    // would discard the row's only description of what changed.
    requestMock.mockResolvedValue(auditResponse([entry({ detail: '{"name":[REDACTED]' })]))
    renderPage()

    await user.click(await screen.findByRole('button', { name: /detail for/ }))

    expect(screen.getByText('{"name":[REDACTED]')).toBeInTheDocument()
  })

  it('tells an exact-match miss apart from a genuinely quiet gateway', async () => {
    requestMock.mockResolvedValue(auditResponse([], 0))
    const { unmount } = renderPage('/audit?action=key.creat')
    // A near miss returns nothing and looks exactly like an idle gateway, so
    // the empty state has to name the matching rule.
    expect(await screen.findByText(/matched exactly/)).toBeInTheDocument()
    unmount()

    renderPage()
    expect(await screen.findByText(/No administrative action was recorded in this window/)).toBeInTheDocument()
    expect(screen.queryByText(/matched exactly/)).toBeNull()
  })

  it('renders an entry that names no actor, target, or trace without inventing them', async () => {
    requestMock.mockResolvedValue(
      auditResponse([entry({ actor: '', actor_id: undefined, target_id: undefined, source_ip: '', trace_id: undefined })]),
    )
    renderPage()

    const row = (await screen.findByText('key.create')).closest('tr')
    expect(row).not.toBeNull()
    expect(within(row as HTMLElement).getAllByText('—')).toHaveLength(4)
    expect(within(row as HTMLElement).queryByRole('link')).toBeNull()
  })

  it('pages through the trail without losing the filters it was narrowed by', async () => {
    const user = userEvent.setup()
    const rows = Array.from({ length: 50 }, (_, index) => entry({ target_id: `key-${index}` }))
    requestMock.mockResolvedValue(auditResponse(rows, 120))
    renderPage('/audit?outcome=denied')
    await screen.findByText('key-0')

    await user.click(screen.getByRole('button', { name: 'Next' }))

    const location = screen.getByTestId('location').textContent ?? ''
    expect(location).toContain('offset=50')
    expect(location).toContain('outcome=denied')
    await waitFor(() => expect(paths().some((path) => path.includes('offset=50'))).toBe(true))
  })

  it('applies a chosen range, and keeps the default out of the URL', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('key.create')

    await user.click(screen.getByRole('combobox', { name: 'Time range' }))
    await user.click(await screen.findByRole('option', { name: 'Last 7 days' }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    expect(screen.getByTestId('location')).toHaveTextContent('hours=168')

    await user.click(screen.getByRole('combobox', { name: 'Time range' }))
    await user.click(await screen.findByRole('option', { name: 'Last 24 hours' }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    // The interesting part of a shared link is what was actually changed.
    expect(screen.getByTestId('location').textContent).not.toContain('hours')
  })
})
