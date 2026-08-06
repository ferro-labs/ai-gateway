import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { STRATEGIES, STRATEGY_DOCS } from '../lib/strategies'
import StrategyPage from './StrategyPage'

const request = vi.fn()

vi.mock('../lib/api', () => ({
  request: (path: string, init?: RequestInit) => request(path, init) as Promise<unknown>,
}))

const config = {
  strategy: { mode: 'fallback' },
  targets: [
    { virtual_key: 'openai', weight: 1, retry: { attempts: 3 }, concurrency: { max_concurrency: 32, queue_size: 500 } },
    { virtual_key: 'anthropic', circuit_breaker: { failure_threshold: 5 } },
  ],
}

function arm(document: Record<string, unknown> = config) {
  request.mockImplementation((path: string) =>
    path === '/admin/config' ? Promise.resolve(document) : Promise.reject(new Error(`unexpected path ${path}`)),
  )
}

/** The catalog card for one mode, found by the label it renders. */
function card(label: string): HTMLElement {
  const link = screen.getByRole('link', { name: new RegExp(label) })
  return link
}

beforeEach(() => {
  request.mockReset()
})

describe('StrategyPage', () => {
  it('names the mode the gateway is actually running', async () => {
    arm()
    render(<StrategyPage />)

    const active = await screen.findByRole('heading', { name: 'Fallback' })
    expect(active).toBeInTheDocument()
    expect(screen.getByText('strategy.mode: fallback')).toBeInTheDocument()
  })

  it('offers every mode, each linking to the reference that documents the value', async () => {
    arm()
    render(<StrategyPage />)
    await screen.findByRole('heading', { name: 'Fallback' })

    for (const strategy of STRATEGIES) {
      expect(card(strategy.label)).toHaveAttribute('href', STRATEGY_DOCS)
    }
  })

  it('marks exactly one card active, so the catalog cannot contradict the header', async () => {
    arm()
    render(<StrategyPage />)
    await screen.findByRole('heading', { name: 'Fallback' })

    expect(within(card('Fallback')).getByText('Active')).toBeInTheDocument()
    expect(within(card('Load balance')).queryByText('Active')).toBeNull()
  })

  it('reports a mode this build has no card for rather than showing none as active', async () => {
    // A newer gateway may route by a mode released after this dashboard. The
    // page must still say what is running.
    arm({ strategy: { mode: 'quantum-routing' }, targets: [{ virtual_key: 'openai' }] })
    render(<StrategyPage />)

    expect(await screen.findByRole('heading', { name: 'quantum-routing' })).toBeInTheDocument()
    expect(screen.getByText(/does not recognise/)).toBeInTheDocument()
  })

  it('distinguishes a target left on the gateway defaults from one that was tuned', async () => {
    arm()
    render(<StrategyPage />)
    await screen.findByRole('heading', { name: 'Fallback' })

    const openai = screen.getByRole('row', { name: /openai/ })
    expect(within(openai).getByText('3 attempts')).toBeInTheDocument()
    expect(within(openai).getByText(/32 in flight · 500 queued/)).toBeInTheDocument()

    const anthropic = screen.getByRole('row', { name: /anthropic/ })
    // Unset is not the same as 1: showing a value nobody configured is how an
    // operator comes to believe a retry policy exists.
    expect(within(anthropic).getByText('1 attempt')).toBeInTheDocument()
    expect(within(anthropic).getByText('unbounded')).toBeInTheDocument()
    expect(within(anthropic).getByText('Configured')).toBeInTheDocument()
  })

  it('shows the variant split the gateway actually applies, zero weights included', async () => {
    // A zero weight is zero traffic — `weightedPick` skips the variant outright
    // — so a drained variant must read as 0%. The panel used to normalise it to
    // 1 and report 25%/75%, which is the reading an operator drains a variant to
    // check.
    arm({
      strategy: {
        mode: 'ab-test',
        ab_variants: [
          { target_key: 'openai', weight: 0, label: 'control' },
          { target_key: 'anthropic', weight: 3, label: 'challenger' },
        ],
      },
      targets: [{ virtual_key: 'openai' }, { virtual_key: 'anthropic' }],
    })
    render(<StrategyPage />)

    expect(await screen.findByRole('heading', { name: 'Variants' })).toBeInTheDocument()
    expect(screen.getByText('0%')).toBeInTheDocument()
    expect(screen.getByText('100%')).toBeInTheDocument()
  })

  it('names an unset mode rather than leaving an empty heading', async () => {
    // `strategy.mode` absent arrives as "", which is a string: a nullish
    // fallback passes it straight through and the panel titles itself with
    // nothing at all.
    arm({ targets: [{ virtual_key: 'openai' }] })
    render(<StrategyPage />)

    expect(await screen.findByRole('heading', { name: 'No strategy configured' })).toBeInTheDocument()
    expect(screen.getByText('strategy.mode: unset')).toBeInTheDocument()
    // Nothing is active, so nothing may be badged active.
    expect(screen.queryByText('Active')).toBeNull()
  })

  it('says a gateway has nowhere to route rather than showing an empty table', async () => {
    arm({ strategy: { mode: 'single' } })
    render(<StrategyPage />)

    expect(await screen.findByText('No routing targets configured')).toBeInTheDocument()
  })

  it('reports a failed load as an error rather than as an unconfigured gateway', async () => {
    request.mockImplementation(() => Promise.reject(new Error('admin config is unreachable')))
    render(<StrategyPage />)

    expect(await screen.findByRole('alert')).toHaveTextContent('admin config is unreachable')
    expect(screen.queryByText('No routing targets configured')).toBeNull()
  })
})
