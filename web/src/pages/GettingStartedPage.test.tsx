import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DashboardSummary } from '../types'
import GettingStartedPage from './GettingStartedPage'

const request = vi.hoisted(() => vi.fn())

vi.mock('../lib/api', () => ({ request }))

/** A gateway with nothing set up yet: no provider, no key, no traffic. */
function summary(overrides: Partial<DashboardSummary> = {}): DashboardSummary {
  return {
    providers: { total: 0, available: 0 },
    keys: { total: 0, active: 0, expired: 0, total_usage: 0 },
    request_logs: { enabled: true, total: 0 },
    ...overrides,
  }
}

/** Everything the gateway can confirm is confirmed. */
const readyGateway = summary({
  providers: { total: 1, available: 1 },
  keys: { total: 2, active: 2, expired: 0, total_usage: 9 },
  request_logs: { enabled: true, total: 17 },
})

function renderPage() {
  return render(
    <MemoryRouter>
      <GettingStartedPage />
    </MemoryRouter>,
  )
}

/** The checklist entry an operator reads under the given heading. */
function step(title: string): HTMLElement {
  const item = screen.getByRole('heading', { name: title }).closest('li')
  if (!item) throw new Error(`no checklist entry for "${title}"`)
  return item
}

/** How the step reports its own state — the word a screen reader reaches. */
function stateOf(title: string): string {
  return within(step(title)).getByText(/^(Complete|Not done yet|Suggested)$/).textContent ?? ''
}

function isTicked(title: string): boolean {
  return stateOf(title) === 'Complete'
}

function progress(): HTMLProgressElement {
  return screen.getByRole<HTMLProgressElement>('progressbar')
}

beforeEach(() => {
  request.mockReset()
  request.mockResolvedValue(summary())
})

describe('GettingStartedPage', () => {
  it('ticks "Connect a provider" only once the gateway reports a provider it can actually reach', async () => {
    // A configured-but-unreachable provider is the case that matters: the
    // credential exists, so `total` is 1, yet no model can be served.
    const view = renderPage()
    request.mockResolvedValue(summary({ providers: { total: 1, available: 0 } }))
    expect(await screen.findByRole('heading', { name: 'Connect a provider' })).toBeInTheDocument()
    expect(isTicked('Connect a provider')).toBe(false)
    view.unmount()

    request.mockResolvedValue(summary({ providers: { total: 1, available: 1 } }))
    renderPage()

    await waitFor(() => expect(isTicked('Connect a provider')).toBe(true))
  })

  it('ticks "Create an API key" only once a key exists to send traffic with', async () => {
    const view = renderPage()
    expect(await screen.findByRole('heading', { name: 'Create an API key' })).toBeInTheDocument()
    expect(isTicked('Create an API key')).toBe(false)
    view.unmount()

    request.mockResolvedValue(summary({ keys: { total: 1, active: 1, expired: 0, total_usage: 0 } }))
    renderPage()

    await waitFor(() => expect(isTicked('Create an API key')).toBe(true))
  })

  it('ticks "Inspect request activity" only once the gateway has logged a request', async () => {
    request.mockResolvedValue(summary({ request_logs: { enabled: true, total: 0 } }))
    const view = renderPage()
    expect(await screen.findByRole('heading', { name: 'Inspect request activity' })).toBeInTheDocument()
    expect(isTicked('Inspect request activity')).toBe(false)
    view.unmount()

    request.mockResolvedValue(summary({ request_logs: { enabled: true, total: 1 } }))
    renderPage()

    await waitFor(() => expect(isTicked('Inspect request activity')).toBe(true))
  })

  it('never ticks "Send a test request", the step an earlier build called done the moment the Playground was opened', async () => {
    const view = renderPage()

    await screen.findByRole('heading', { name: 'Send a test request' })
    // The gateway cannot tell a Playground request from any other traffic, so
    // no state confirms it — the page must say so rather than imply a check.
    expect(step('Send a test request')).toHaveTextContent("this can't be checked automatically")
    // Nor may it look like an outstanding verifiable step: "not done yet" beside
    // those reads as work the operator still has to finish.
    expect(stateOf('Send a test request')).toBe('Suggested')
    expect(stateOf('Create an API key')).toBe('Not done yet')
    view.unmount()

    // And on a fully set-up gateway it stays unticked while everything the
    // gateway can prove is ticked.
    request.mockResolvedValue(readyGateway)
    renderPage()

    await waitFor(() => expect(isTicked('Create an API key')).toBe(true))
    expect(isTicked('Send a test request')).toBe(false)
  })

  it('counts only the verifiable steps toward progress, leaving the unverifiable one out of the total', async () => {
    request.mockResolvedValue(readyGateway)
    renderPage()

    const bar = await screen.findByRole<HTMLProgressElement>('progressbar')
    // Four steps are listed, three of them provable.
    expect(screen.getAllByRole('listitem')).toHaveLength(4)
    expect(bar.max).toBe(3)
    expect(bar.value).toBe(3)
    expect(bar).toHaveAccessibleName('3 of 3 verifiable steps complete')
  })

  it('reads empty on a gateway with nothing set up, the state the previous inline-width bar showed as full', async () => {
    renderPage()

    await screen.findByRole('heading', { name: 'Connect a provider' })
    // Asserted as value/max rather than a rendered width: the Content-Security
    // -Policy drops the inline style the old bar filled itself with, so the
    // only fill that survives deployment is the attribute one.
    expect(progress().value).toBe(0)
    expect(progress().max).toBe(3)
  })

  it('reports how many verifiable steps remain while any are outstanding', async () => {
    request.mockResolvedValue(
      summary({
        providers: { total: 1, available: 1 },
        keys: { total: 1, active: 1, expired: 0, total_usage: 0 },
      }),
    )
    renderPage()

    expect(await screen.findByText('2 of 3 verifiable steps complete')).toBeInTheDocument()
    expect(progress().value).toBe(2)
  })

  it('claims completion only for what could be verified, never for the whole checklist', async () => {
    request.mockResolvedValue(readyGateway)
    renderPage()

    // "Setup complete" would be a lie while an unverifiable step is listed.
    expect(await screen.findByText('Every step that can be verified automatically is complete.')).toBeInTheDocument()
    expect(screen.queryByText(/of 3 verifiable steps complete/)).toBeNull()
  })

  it('shows that setup status is being checked instead of an empty checklist', () => {
    // A request that never settles: the page must hold the operator in the
    // checking state rather than render a checklist it has no answers for.
    request.mockReturnValue(new Promise(() => {}))
    renderPage()

    expect(screen.getByRole('status')).toHaveTextContent('Checking gateway setup')
    expect(screen.queryByRole('progressbar')).toBeNull()
  })

  it('surfaces a failed status check as an alert rather than an empty checklist', async () => {
    request.mockRejectedValue(new Error('dashboard summary unavailable'))
    renderPage()

    expect(await screen.findByRole('alert')).toHaveTextContent('dashboard summary unavailable')
    expect(screen.queryByRole('progressbar')).toBeNull()
  })

  it('offers a retry that re-reads the gateway, so a transient failure is not a dead page', async () => {
    const user = userEvent.setup()
    request.mockRejectedValueOnce(new Error('gateway unreachable'))
    request.mockResolvedValue(readyGateway)
    renderPage()

    await user.click(await screen.findByRole('button', { name: 'Try again' }))

    expect(await screen.findByRole('progressbar')).toHaveAccessibleName('3 of 3 verifiable steps complete')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(request.mock.calls.map((call: unknown[]) => call[0])).toEqual(['/admin/dashboard', '/admin/dashboard'])
  })

  it('links each step to the page that completes it', async () => {
    request.mockResolvedValue(summary())
    renderPage()

    // Queried by role "button": these are anchors, but the button primitive
    // stamps role="button" on them, so that is what an operator's screen
    // reader hears. The href is what decides where the step actually goes.
    expect(await screen.findByRole('button', { name: 'Review providers' })).toHaveAttribute('href', '/providers')
    expect(screen.getByRole('button', { name: 'Manage keys' })).toHaveAttribute('href', '/keys')
    expect(screen.getByRole('button', { name: 'Open Playground' })).toHaveAttribute('href', '/playground')
    expect(screen.getByRole('button', { name: 'View logs' })).toHaveAttribute('href', '/logs')
  })
})
