import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../lib/api'
import type * as ApiModule from '../lib/api'
import AnalyticsPage from './AnalyticsPage'
import type { LogsStats } from '../types'

const request = vi.hoisted(() => vi.fn())

// Only `request` is replaced: `ApiError` has to stay the real class, because
// the page decides whether a failure is a missing store by instance and status.
vi.mock('../lib/api', async (importOriginal) => ({
  ...(await importOriginal<typeof ApiModule>()),
  request,
}))

function stats(overrides: Partial<LogsStats> = {}): LogsStats {
  return {
    summary: { total_entries: 4, error_entries: 1, total_tokens: 300, cost_usd: 0.5, unpriced_requests: 3 },
    by_stage: {
      before_request: { count: 2, errors: 0, tokens: 0 },
      after_request: { count: 1, errors: 0, tokens: 300 },
      on_error: { count: 1, errors: 1, tokens: 0 },
    },
    by_provider: { openai: { count: 1, errors: 0, tokens: 300, cost_usd: 0.5, unpriced: 0 } },
    by_model: { 'gpt-4o': { count: 1, errors: 0, tokens: 300, cost_usd: 0.5, unpriced: 3 } },
    ...overrides,
  }
}

/** The path every `request` call was made with, in order. */
function requestedPaths(): string[] {
  return request.mock.calls.map((call) => String(call[0]))
}

/** Reports the live URL so a test can assert what a filter wrote to it. */
function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

function renderPage(entry = '/analytics') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <AnalyticsPage />
      <LocationProbe />
    </MemoryRouter>,
  )
}

describe('AnalyticsPage', () => {
  beforeEach(() => {
    request.mockReset()
    request.mockResolvedValue(stats())
  })


  it('explains a missing request-log store instead of reporting a failure', async () => {
    // 501 is the gateway saying no store is configured — a deployment choice.
    // Rendering it as an error told the operator something was broken and gave
    // them nothing to do about it.
    request.mockRejectedValue(new ApiError('request log storage is not enabled', 501))
    renderPage()

    expect(await screen.findByText('Request logging is disabled')).toBeInTheDocument()
    expect(screen.getByText(/REQUEST_LOG_STORE_BACKEND/)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    // The filter form is meaningless with nothing to filter.
    expect(screen.queryByRole('button', { name: 'Apply' })).not.toBeInTheDocument()
  })

  it('asks the gateway for more groups than it displays', async () => {
    renderPage()

    await waitFor(() => expect(request).toHaveBeenCalled())
    // The server truncates by count; the page also ranks by cost, so the two
    // limits must not be the same number.
    expect(requestedPaths()[0]).toContain('limit=10')
    expect(requestedPaths()[0]).toContain('buckets=48')
  })

  it('sends the filters to the gateway rather than discarding rows in the browser', async () => {
    const user = userEvent.setup()
    renderPage()
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))

    await user.type(screen.getByRole('textbox', { name: 'Provider' }), 'anthropic')
    await user.type(screen.getByRole('textbox', { name: 'Model' }), 'claude-3')
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
    const path = requestedPaths()[1]
    expect(path).toContain('provider=anthropic')
    expect(path).toContain('model=claude-3')
  })

  it('puts the applied filters in the URL so a filtered view can be sent to a colleague', async () => {
    const user = userEvent.setup()
    renderPage()
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))

    await user.type(screen.getByRole('textbox', { name: 'Provider' }), 'anthropic')
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('provider=anthropic'))
  })

  it('restores the range and filters a shared link carries', async () => {
    renderPage('/analytics?hours=1&provider=anthropic&model=claude-3&stage=on_error')

    await waitFor(() => expect(request).toHaveBeenCalled())
    const path = requestedPaths()[0]
    expect(path).toContain('provider=anthropic')
    expect(path).toContain('model=claude-3')
    expect(path).toContain('stage=on_error')
    // The form shows what the link asked for, not an empty default.
    expect(screen.getByRole('textbox', { name: 'Provider' })).toHaveValue('anthropic')
    // Filtered to one terminal stage, "1 failure in 1 request" is a tautology
    // rather than a measured failure rate.
    expect(await screen.findByText('not derivable while filtered to one stage')).toBeInTheDocument()
  })

  it('ignores a range the control cannot represent', async () => {
    renderPage('/analytics?hours=nonsense')

    await waitFor(() => expect(request).toHaveBeenCalled())
    // A NaN range would reach the gateway as a window with no start.
    expect(requestedPaths()[0]).toMatch(/since=\d{4}-\d{2}-\d{2}T/)
    expect(screen.getByRole('combobox', { name: 'Time range' })).toHaveValue('24')
  })

  it('says how much of a cost ranking the catalog could not price', async () => {
    renderPage()

    // Without this the $0.50 reads as the whole spend for gpt-4o, when three of
    // its four requests contributed nothing to it.
    expect(await screen.findByText(/3 unpriced/)).toBeInTheDocument()
  })

  it('omits the unpriced count from rankings it does not qualify', async () => {
    renderPage()

    expect(await screen.findByRole('region', { name: 'Models by cost' })).toHaveTextContent('3 unpriced')
    // The token ranking counts those rows in full, so repeating the caveat
    // there would read as a gap in a number that has none.
    expect(screen.getByRole('region', { name: 'Models by tokens' })).not.toHaveTextContent('unpriced')
  })

  it('reports a genuine failure as an error rather than as a missing store', async () => {
    // Only a 501 is a deployment choice. Everything else is something broken,
    // and swallowing it would leave an empty page with no account of why.
    request.mockRejectedValue(new ApiError('the request log store is unreachable', 503))
    renderPage()

    expect(await screen.findByRole('alert')).toHaveTextContent('the request log store is unreachable')
    expect(screen.queryByText('Request logging is disabled')).not.toBeInTheDocument()
  })

  it('calls an idle range idle, rather than printing a strip of confident zeros', async () => {
    request.mockResolvedValue(
      stats({ summary: { total_entries: 0, error_entries: 0, total_tokens: 0 }, by_stage: {}, by_provider: {}, by_model: {} }),
    )
    renderPage()

    expect(await screen.findByText('No activity in this range')).toBeInTheDocument()
    // "0% failures" over nothing reads as a healthy gateway rather than a quiet
    // one, so the metric strip is withheld entirely.
    expect(screen.queryByRole('region', { name: 'Request latency' })).not.toBeInTheDocument()
  })

  it('leads on the tail an operator is paged about, not on the mean', async () => {
    request.mockResolvedValue(
      stats({
        latency_ms: { p50: 120, p95: 2400, p99: 9800, max: 15_000, mean: 310, count: 42 },
        ttft_ms: null,
      }),
    )
    renderPage()

    const latency = await screen.findByRole('region', { name: 'Request latency' })
    expect(within(latency).getByText('p99')).toBeInTheDocument()
    expect(within(latency).getByText('9.80 s')).toBeInTheDocument()
    // The mean sits beside the percentiles for scale; one request in twenty at
    // ten seconds barely moves it.
    expect(within(latency).getByText(/42 measured · mean 310 ms/)).toBeInTheDocument()

    const ttft = screen.getByRole('region', { name: 'Time to first token' })
    expect(within(ttft).getByText(/No streaming request in this range/)).toBeInTheDocument()
  })

  it('plots traffic and tokens from the series the gateway aggregated', async () => {
    request.mockResolvedValue(
      stats({
        series: {
          points: [
            { start: '2026-07-23T10:00:00Z', requests: 3, errors: 1, prompt_tokens: 200, completion_tokens: 100 },
            { start: '2026-07-23T11:00:00Z', requests: 1, errors: 0, prompt_tokens: 100, completion_tokens: 50 },
          ],
          truncated: false,
        },
      }),
    )
    renderPage()

    const charts = await screen.findAllByRole('img')
    expect(charts).toHaveLength(2)
    expect(charts[0]).toHaveAccessibleName(/4 requests over/)
    expect(charts[1]).toHaveAccessibleName(/450 tokens over/)
    expect(screen.queryByText(/does not report a time series/)).not.toBeInTheDocument()
  })

  it('says an older gateway sent no series instead of drawing an empty chart', async () => {
    renderPage()

    // The dashboard deploys separately from the gateway, so this is a version
    // gap rather than a fault — and it has to say which.
    expect(await screen.findByText(/does not report a time series/)).toBeInTheDocument()
  })

  it('ranks the most frequent failures, and says plainly when there are none', async () => {
    request.mockResolvedValue(stats({ top_errors: [{ message: 'context deadline exceeded', count: 9 }] }))
    const { unmount } = renderPage()

    const failures = await screen.findByRole('region', { name: 'Top failures' })
    expect(within(failures).getByText('context deadline exceeded')).toBeInTheDocument()
    expect(within(failures).getByText('9')).toBeInTheDocument()
    unmount()

    request.mockResolvedValue(stats())
    renderPage()
    expect(await screen.findByText('No failures were recorded in this range.')).toBeInTheDocument()
  })

  it('reports what an older gateway did not send rather than a confident zero', async () => {
    // An older gateway sends no cost at all: not in the summary, and not on any
    // group either.
    request.mockResolvedValue(
      stats({
        summary: { total_entries: 4, error_entries: 1, total_tokens: 300 },
        by_model: { 'gpt-4o': { count: 1, errors: 0, tokens: 300 } },
      }),
    )
    renderPage()

    expect(await screen.findByText('input/output split not reported')).toBeInTheDocument()
    expect(screen.getByText('not reported by this gateway')).toBeInTheDocument()
    // Nothing was priced, so the cost ranking has nothing to rank.
    expect(screen.getByRole('region', { name: 'Models by cost' })).toHaveTextContent('No request in this range was priced')
  })

  it('states the token split and the per-request average once the gateway reports them', async () => {
    request.mockResolvedValue(
      stats({
        summary: {
          total_entries: 4,
          error_entries: 1,
          total_tokens: 300,
          prompt_tokens: 240,
          completion_tokens: 60,
          cost_usd: 0.5,
          unpriced_requests: 0,
        },
      }),
    )
    renderPage()

    // Output tokens bill at a multiple of input tokens, so the pair is what
    // explains a bill that a single total cannot.
    expect(await screen.findByText('240 in · 60 out')).toBeInTheDocument()
    expect(screen.getByText('150 tokens per request')).toBeInTheDocument()
  })

  it('withholds a request count no terminal stage was recorded for', async () => {
    request.mockResolvedValue(
      stats({ by_stage: { before_request: { count: 4, errors: 0, tokens: 0 } } }),
    )
    renderPage()

    expect(await screen.findAllByText('no completed requests recorded')).toHaveLength(2)
  })

  it('sends a chosen stage to the gateway, and puts it in the URL', async () => {
    const user = userEvent.setup()
    renderPage()
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))

    await user.click(screen.getByRole('combobox', { name: 'Stage' }))
    await user.click(await screen.findByRole('option', { name: 'On error' }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(requestedPaths().some((path) => path.includes('stage=on_error'))).toBe(true))
    expect(screen.getByTestId('location')).toHaveTextContent('stage=on_error')
  })

  it('re-reads the range as soon as it is chosen, without waiting for Apply', async () => {
    const user = userEvent.setup()
    renderPage()
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))

    // The range is not a draft filter: nothing else on the form depends on it,
    // so making an operator confirm it twice would be noise.
    await user.selectOptions(screen.getByRole('combobox', { name: 'Time range' }), '1')

    await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
    expect(screen.getByTestId('location')).toHaveTextContent('hours=1')
  })
})
