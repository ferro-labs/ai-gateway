import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminHealth, DashboardSummary, LogsStats, Readiness, RequestLog } from '../types'
import OverviewPage from './OverviewPage'

const request = vi.hoisted(() => vi.fn())
const requestProbe = vi.hoisted(() => vi.fn())

vi.mock('../lib/api', () => ({ request, requestProbe }))

const summary: DashboardSummary = {
  providers: { total: 2, available: 2 },
  keys: { total: 1, active: 1, expired: 0, total_usage: 4 },
  request_logs: { enabled: true, total: 0 },
}

/**
 * MCP state is read from here, not from /readyz: that body omits `mcp_servers`
 * on a 503, so it reports nothing in the one case an operator looks — and it
 * withholds the failure reason even on a 200, being unauthenticated.
 */
const health: AdminHealth = {
  status: 'ok',
  providers: [{ name: 'openai', status: 'available', models: 2 }],
  components: [{ name: 'key store', status: 'healthy' }],
  mcp_servers: [{ name: 'filesystem', ready: true, required: false }],
}

/** A 200 body: the gateway is in rotation. */
const readyBody: Readiness = {
  status: 'ready',
  providers: [{ name: 'openai', circuit: 'closed' }],
}

function log(overrides: Partial<RequestLog> = {}): RequestLog {
  return {
    trace_id: 'trace-abc',
    stage: 'after_request',
    model: 'gpt-4o-mini',
    provider: 'openai',
    prompt_tokens: 40,
    completion_tokens: 10,
    total_tokens: 50,
    error_message: '',
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

/** Two terminal stages, so a request count is derivable from the rows. */
function statsBody(overrides: Partial<LogsStats> = {}): LogsStats {
  return {
    summary: { total_entries: 12, error_entries: 1, total_tokens: 900 },
    by_stage: {
      before_request: { count: 6, errors: 0, tokens: 0 },
      after_request: { count: 5, errors: 0, tokens: 900 },
      on_error: { count: 1, errors: 1, tokens: 0 },
    },
    by_provider: { openai: { count: 5, errors: 0, tokens: 900 } },
    by_model: { 'gpt-4o-mini': { count: 5, errors: 0, tokens: 900 } },
    ...overrides,
  }
}

interface Fixtures {
  summary: DashboardSummary
  health: AdminHealth
  stats: LogsStats | null
  logs: RequestLog[]
}

function arm(readiness: Readiness | Error = readyBody, overrides: Partial<Fixtures> = {}) {
  const fixtures: Fixtures = { summary, health, stats: null, logs: [], ...overrides }
  request.mockImplementation((path: string) => {
    if (path === '/admin/dashboard') return Promise.resolve(fixtures.summary)
    if (path === '/admin/health') return Promise.resolve(fixtures.health)
    if (path.startsWith('/admin/logs/stats')) return Promise.resolve(fixtures.stats)
    if (path.startsWith('/admin/logs')) {
      return Promise.resolve({
        data: fixtures.logs,
        summary: { total_entries: fixtures.logs.length, returned_entries: fixtures.logs.length },
        filters: {},
      })
    }
    return Promise.reject(new Error(`unexpected path ${path}`))
  })
  requestProbe.mockImplementation((path: string) => {
    if (path !== '/readyz') return Promise.reject(new Error(`unexpected probe ${path}`))
    return readiness instanceof Error ? Promise.reject(readiness) : Promise.resolve(readiness)
  })
}

/** Surfaces the query string so URL state can be asserted, not inferred. */
function LocationProbe() {
  const { search } = useLocation()
  return <span data-testid="query-string">{search}</span>
}

function renderPage(entry = '/') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <OverviewPage />
      <LocationProbe />
    </MemoryRouter>,
  )
}

/** The tile with the given label, so a '—' is read against the right metric. */
function metric(label: string): HTMLElement {
  const tile = screen.getByText(label).parentElement
  if (!tile) throw new Error(`no metric tile for ${label}`)
  return tile
}

/**
 * The overall ready/not-ready row.
 *
 * Scoped rather than searched page-wide: "Ready" is also a per-MCP-server pill,
 * and asserting on the word alone would pass on a gateway that is out of
 * rotation while one of its tool servers is up.
 */
function overallStatus(): HTMLElement {
  const label = screen.getByText('Serving traffic')
  const row = label.parentElement
  if (!row) throw new Error('readiness status label has no row')
  return row
}

beforeEach(() => {
  request.mockReset()
  requestProbe.mockReset()
})


describe('OverviewPage — readiness', () => {
  it('reports a ready gateway', async () => {
    arm()
    renderPage()

    await screen.findByText('Serving traffic')
    expect(within(overallStatus()).getByText('Ready')).toBeInTheDocument()
    expect(screen.queryByText(/No traffic is being accepted/)).not.toBeInTheDocument()
  })

  it('shows the reason a 503 carries, which is the only account of the outage', async () => {
    // requestProbe resolves a 503 body rather than rejecting: unready is an
    // answer. The body carries status and reason and nothing else.
    arm({ status: 'not_ready', reason: 'required mcp server unavailable' })
    renderPage()

    await screen.findByText('Serving traffic')
    expect(within(overallStatus()).getByText('Not ready')).toBeInTheDocument()
    expect(screen.getByText(/required mcp server unavailable/)).toBeInTheDocument()
  })

  it('keeps the rest of the page when the probe itself cannot be reached', async () => {
    arm(new Error('readyz unreachable'))
    renderPage()

    expect(await screen.findByText(/whether this gateway is in rotation is unknown/)).toBeInTheDocument()
    // One panel lost its answer; the dashboard did not fail.
    expect(screen.getByText('key store')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders no MCP list when the gateway configures no MCP server', async () => {
    // `mcp_servers` is omitted entirely in that case, so an empty table here
    // would invent a subsystem the deployment does not have.
    arm(readyBody, { health: { ...health, mcp_servers: undefined } })
    renderPage()

    await screen.findByText('Serving traffic')
    expect(screen.queryByText('MCP servers')).not.toBeInTheDocument()
  })

  it('lists a configured MCP server with its readiness', async () => {
    arm()
    renderPage()

    const server = (await screen.findByText('filesystem')).closest('li')
    expect(server).not.toBeNull()
    expect(within(server as HTMLElement).getByText('Ready')).toBeInTheDocument()
    expect(within(server as HTMLElement).queryByText('Required')).not.toBeInTheDocument()
  })

  it('shows why an MCP server is unready, which no other surface reports', async () => {
    // Before this reached the dashboard the only account of the failure was in
    // the gateway's own logs: /readyz withholds it and /health omits MCP
    // entirely, so an authenticated operator could see "Unready" and nothing
    // else.
    const reason = 'mcp initialize: Post "https://mcp.example.com/mcp": connection refused'
    arm(readyBody, {
      health: { ...health, mcp_servers: [{ name: 'search', ready: false, required: false, last_error: reason }] },
    })
    renderPage()

    const server = (await screen.findByText('search')).closest('li')
    expect(within(server as HTMLElement).getByText(reason)).toBeInTheDocument()
  })

  it('keeps listing MCP servers while the gateway is out of rotation', async () => {
    // The 503 case is the one this panel exists for, and /readyz drops
    // `mcp_servers` from that body — so a list read from there was empty
    // precisely when a required server had taken the gateway down.
    arm(
      { status: 'not_ready', reason: 'required mcp server unavailable' },
      { health: { ...health, mcp_servers: [{ name: 'search', ready: false, required: true, last_error: 'handshake timed out' }] } },
    )
    renderPage()

    const server = (await screen.findByText('search')).closest('li')
    expect(within(server as HTMLElement).getByText('Unready')).toBeInTheDocument()
    expect(within(server as HTMLElement).getByText('handshake timed out')).toBeInTheDocument()
  })

  it('does not claim liveness the endpoint cannot prove', async () => {
    arm()
    renderPage()

    await screen.findByText('Serving traffic')
    expect(screen.getByText(/an HTTP server that stops answering is not detected/)).toBeInTheDocument()
  })

  it('spells out that a required server being down stops all traffic', async () => {
    arm(readyBody, {
      health: { ...health, mcp_servers: [{ name: 'search', ready: false, required: true }] },
    })
    renderPage()

    const server = (await screen.findByText('search')).closest('li')
    expect(server).not.toBeNull()
    expect(within(server as HTMLElement).getByText('Unready')).toBeInTheDocument()
    expect(within(server as HTMLElement).getByText('Required')).toBeInTheDocument()
    expect(
      within(server as HTMLElement).getByText(/including requests that use no tools/),
    ).toBeInTheDocument()
  })

  it('treats an optional server being down as costing only its own tools', async () => {
    arm(readyBody, {
      health: { ...health, mcp_servers: [{ name: 'search', ready: false, required: false }] },
    })
    renderPage()

    const server = (await screen.findByText('search')).closest('li')
    expect(within(server as HTMLElement).getByText(/Requests that do not call them are unaffected/)).toBeInTheDocument()
    expect(within(server as HTMLElement).queryByText(/including requests that use no tools/)).not.toBeInTheDocument()
  })
})

describe('OverviewPage — request metrics', () => {
  it('separates a gateway told not to log from one whose statistics query failed', async () => {
    // Same missing numbers, two different facts: the first is how the operator
    // configured the gateway, the second is something to go and fix.
    arm(readyBody, { summary: { ...summary, request_logs: { enabled: false, total: 0 } } })
    const { unmount } = renderPage()
    expect(await screen.findAllByText('logging disabled')).toHaveLength(2)
    unmount()

    arm()
    renderPage()
    expect(await screen.findAllByText('statistics unavailable')).toHaveLength(2)
  })

  it('counts completed requests from the terminal stages, not from log rows', async () => {
    arm(readyBody, { stats: statsBody() })
    renderPage()

    // Six requests completed across twelve rows; reading rows as requests would
    // double the traffic and halve the failure rate.
    await waitFor(() => expect(within(metric('Requests')).getByText('6')).toBeInTheDocument())
    expect(within(metric('Error Rate')).getByText('16.7%')).toBeInTheDocument()
    expect(within(metric('Error Rate')).getByText('1 failed')).toBeInTheDocument()
  })

  it('declines to state a failure rate no terminal stage was recorded for', async () => {
    // A logger writing only `before_request` rows leaves nothing to derive a
    // request count from, and a confident green 0% would be a lie.
    arm(readyBody, {
      stats: statsBody({
        by_stage: { before_request: { count: 4, errors: 0, tokens: 0 } },
      }),
    })
    renderPage()

    await waitFor(() => expect(screen.getAllByText('no completed requests recorded')).toHaveLength(2))
    expect(within(metric('Requests')).getByText('—')).toBeInTheDocument()
    expect(within(metric('Error Rate')).getByText('—')).toBeInTheDocument()
  })
})

describe('OverviewPage — panels', () => {
  it('plots the series the gateway aggregated for the selected range', async () => {
    arm(readyBody, {
      stats: statsBody({
        series: {
          points: [
            { start: '2026-07-23T10:00:00Z', requests: 4, errors: 1, prompt_tokens: 400, completion_tokens: 100 },
            { start: '2026-07-23T11:00:00Z', requests: 2, errors: 0, prompt_tokens: 200, completion_tokens: 50 },
          ],
          truncated: false,
        },
      }),
    })
    renderPage()

    // The accessible name carries the same story the bars do, which is the only
    // part of the chart a non-sighted operator can read.
    const chart = await screen.findByRole('img')
    expect(chart).toHaveAccessibleName(/6 requests over/)
    expect(screen.queryByText('No traffic history')).not.toBeInTheDocument()
  })

  it('says the gateway reported no series rather than drawing an empty chart', async () => {
    arm(readyBody, { stats: statsBody() })
    renderPage()

    expect(await screen.findByText('No traffic history')).toBeInTheDocument()
  })

  it('names the disabled logger in the traffic panel instead of an absent series', async () => {
    arm(readyBody, { summary: { ...summary, request_logs: { enabled: false, total: 0 } } })
    renderPage()

    expect(await screen.findAllByText('Request logging is disabled')).not.toHaveLength(0)
    expect(screen.getByText(/Enable the request logger plugin/)).toBeInTheDocument()
  })

  it('lists the newest events with the outcome each one reached', async () => {
    arm(readyBody, {
      logs: [
        log({ trace_id: 'trace-ok' }),
        log({ trace_id: 'trace-bad', stage: 'on_error', error_message: 'upstream refused', provider: '', model: '' }),
      ],
    })
    renderPage()

    const failed = (await screen.findByText('Error')).closest('tr')
    expect(failed).not.toBeNull()
    // A row that never reached a provider still belongs in the table; it just
    // has nothing to name in those columns.
    expect(within(failed as HTMLElement).getAllByText('—')).toHaveLength(2)
    expect(screen.getByText('OK')).toBeInTheDocument()
    expect(screen.getByText('openai')).toBeInTheDocument()
  })

  it('offers the Playground when the range is quiet, and does not when nothing is being recorded', async () => {
    arm()
    const { unmount } = renderPage()
    expect(await screen.findByText('No requests in this range')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Open Playground' })).toHaveAttribute('href', '/playground')
    unmount()

    arm(readyBody, { summary: { ...summary, request_logs: { enabled: false, total: 0 } } })
    renderPage()
    expect(await screen.findByText('Enable request logging to populate this table.')).toBeInTheDocument()
    // Sending a request would change nothing while nothing is being written.
    expect(screen.queryByRole('button', { name: 'Open Playground' })).toBeNull()
  })

  it('does not colour a deliberately disabled component as a fault', async () => {
    arm(readyBody, {
      health: {
        ...health,
        components: [
          { name: 'key store', status: 'healthy' },
          { name: 'request log store', status: 'disabled' },
          { name: 'config store', status: 'degraded' },
        ],
      },
    })
    renderPage()

    await screen.findByText('key store')
    expect(screen.getByText('healthy').className).toContain('success')
    expect(screen.getByText('disabled').className).toContain('muted')
    expect(screen.getByText('degraded').className).toContain('warning')
  })
})

describe('OverviewPage — failure', () => {
  it('reports a failed load and offers a retry that actually re-reads', async () => {
    const user = userEvent.setup()
    arm()
    request.mockImplementation((path: string) =>
      path === '/admin/dashboard'
        ? Promise.reject(new Error('the control plane is unreachable'))
        : Promise.resolve(health),
    )
    renderPage()

    expect(await screen.findByRole('alert')).toHaveTextContent('the control plane is unreachable')
    const before = request.mock.calls.length

    await user.click(screen.getByRole('button', { name: 'Try again' }))

    await waitFor(() => expect(request.mock.calls.length).toBeGreaterThan(before))
  })
})

describe('OverviewPage — time range', () => {
  it('reads the range from the URL so a shared link opens the same window', async () => {
    arm()
    renderPage('/?hours=1')

    await waitFor(() => expect(screen.getByLabelText('Time range')).toHaveValue('1'))
    expect(request.mock.calls.map((call) => String(call[0])).some((path) => path.includes('since='))).toBe(true)
  })

  it('falls back to the default rather than asking for a window with no start', async () => {
    arm()
    renderPage('/?hours=nonsense')

    await waitFor(() => expect(screen.getByLabelText('Time range')).toHaveValue('24'))
  })

  it('puts a chosen range in the URL, and leaves the default out of it', async () => {
    const user = userEvent.setup()
    arm()
    renderPage()
    await screen.findByText('key store')

    await user.selectOptions(screen.getByLabelText('Time range'), '168')
    expect(screen.getByTestId('query-string')).toHaveTextContent('hours=168')

    await user.selectOptions(screen.getByLabelText('Time range'), '24')
    // The link an operator pastes is shorter for saying nothing about a range
    // that was never changed.
    expect(screen.getByTestId('query-string').textContent).not.toContain('hours')
  })
})

describe('OverviewPage — polling', () => {
  // The interval is armed on first render, so the clock has to be faked before
  // the page mounts or the timer created is a real one.
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  async function tick(ms: number): Promise<void> {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms)
    })
  }

  it('refreshes on its own without blanking what is already on screen', async () => {
    arm()
    renderPage()
    await tick(0)
    expect(screen.getByText('key store')).toBeInTheDocument()
    const probeCalls = requestProbe.mock.calls.length

    await tick(30_000)

    // The background tick must not raise the spinner or empty the panels: a
    // page that flickers back to "Loading" every half minute is unreadable.
    expect(requestProbe.mock.calls.length).toBeGreaterThan(probeCalls)
    expect(screen.queryByText('Loading gateway overview')).not.toBeInTheDocument()
    expect(screen.getByText('key store')).toBeInTheDocument()
    expect(within(overallStatus()).getByText('Ready')).toBeInTheDocument()
  })
})
