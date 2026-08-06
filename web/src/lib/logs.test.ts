import { describe, expect, it } from 'vitest'
import { formatDuration, formatUSD, logFailed, rankDimension, requestTotals, spend, tokenSplit } from './logs'
import type { LogsStats, RequestLog } from '../types'

function log(overrides: Partial<RequestLog> = {}): RequestLog {
  return {
    trace_id: 't', stage: 'after_request', model: 'm', provider: 'p',
    prompt_tokens: 0, completion_tokens: 0, total_tokens: 0,
    error_message: '', created_at: '2026-07-22T00:00:00Z',
    ...overrides,
  }
}

// Each dimension group now carries its error and token totals alongside the
// count; these cases only exercise the count, so the rest stay zero.
function stats(byStage: Record<string, number>, summary?: Partial<LogsStats['summary']>): LogsStats {
  return {
    summary: { total_entries: 0, error_entries: 0, total_tokens: 0, ...summary },
    by_stage: Object.fromEntries(
      Object.entries(byStage).map(([name, count]) => [name, { count, errors: 0, tokens: 0 }]),
    ),
    by_provider: {},
    by_model: {},
  }
}

describe('logFailed', () => {
  it.each([
    ['an on_error row', { stage: 'on_error' }, true],
    ['a row carrying error text', { error_message: 'upstream timed out' }, true],
    ['a completed row', { stage: 'after_request' }, false],
    ['a pending row', { stage: 'before_request' }, false],
  ])('%s', (_name, overrides, expected) => {
    expect(logFailed(log(overrides))).toBe(expected)
  })
})

describe('requestTotals', () => {
  it('counts completed requests, not stage rows', () => {
    // 4 successes + 1 failure = 5 requests, written as 10 rows.
    const totals = requestTotals(stats(
      { before_request: 5, after_request: 4, on_error: 1 },
      { total_entries: 10, error_entries: 1 },
    ))
    expect(totals.completed).toBe(5)
    expect(totals.failed).toBe(1)
    expect(totals.errorRate).toBe(20)
  })

  it('reports 100% when every request failed', () => {
    // The regression this module exists for: dividing error_entries by
    // total_entries reported 50% here, and the amber/red threshold is 5%.
    const totals = requestTotals(stats(
      { before_request: 3, on_error: 3 },
      { total_entries: 6, error_entries: 3 },
    ))
    expect(totals.errorRate).toBe(100)
  })

  it('excludes in-flight requests from the denominator', () => {
    // 2 completed, 3 still open. Counting the open ones would report 0% failure
    // for a gateway that has failed half of everything it finished.
    const totals = requestTotals(stats({ before_request: 5, after_request: 1, on_error: 1 }))
    expect(totals.completed).toBe(2)
    expect(totals.errorRate).toBe(50)
  })

  it('reports an idle gateway as zero, not degraded', () => {
    const totals = requestTotals(stats({}))
    expect(totals).toEqual({ completed: 0, failed: 0, errorRate: 0, degraded: false })
  })

  it('flags degraded when only pending rows exist', () => {
    expect(requestTotals(stats({ before_request: 4 })).degraded).toBe(true)
  })

  it('flags degraded when statistics are unavailable', () => {
    expect(requestTotals(null).degraded).toBe(true)
    expect(requestTotals(undefined).degraded).toBe(true)
  })
})

describe('rankDimension', () => {
  const dimension = {
    'gpt-4o-mini': { count: 90, errors: 1, tokens: 900 },
    'gpt-4o': { count: 10, errors: 0, tokens: 8000 },
    unused: { count: 0, errors: 0, tokens: 0 },
  }

  it('ranks by the chosen measure, so the busiest model is not assumed to be the costliest', () => {
    // The same data answers two different questions, and reading the token
    // ranking off the call count gets it backwards here: the model called nine
    // times as often consumes a ninth as many tokens.
    expect(rankDimension(dimension, 'count', 5).map((entry) => entry.name)).toEqual(['gpt-4o-mini', 'gpt-4o'])
    expect(rankDimension(dimension, 'tokens', 5).map((entry) => entry.name)).toEqual(['gpt-4o', 'gpt-4o-mini'])
  })

  it('drops groups with nothing to show and honours the limit', () => {
    expect(rankDimension(dimension, 'tokens', 5).map((entry) => entry.name)).not.toContain('unused')
    expect(rankDimension(dimension, 'tokens', 1)).toHaveLength(1)
    expect(rankDimension(undefined, 'tokens', 5)).toEqual([])
  })
})

describe('tokenSplit', () => {
  it('reports input and output separately', () => {
    const withSplit = stats({}, { total_tokens: 30, prompt_tokens: 20, completion_tokens: 10 })
    expect(tokenSplit(withSplit)).toEqual({ input: 20, output: 10 })
  })

  it('returns null when the gateway did not send a split', () => {
    // The dashboard deploys separately from the gateway, so it can be pointed
    // at one that predates the field. Zero would read as "no input tokens",
    // which is a different claim from "not reported".
    expect(tokenSplit(stats({}, { total_tokens: 30 }))).toBeNull()
    expect(tokenSplit(null)).toBeNull()
  })
})

describe('spend', () => {
  it('reports the priced floor and what it could not price', () => {
    const priced = stats({}, { cost_usd: 41.87, unpriced_requests: 118 })
    expect(spend(priced)).toEqual({ usd: 41.87, unpriced: 118 })
  })

  it('returns null when the gateway sent no cost at all', () => {
    // Zero would claim a free day's traffic. Absent means the gateway predates
    // cost recording, which is a different statement.
    expect(spend(stats({}))).toBeNull()
    expect(spend(null)).toBeNull()
  })
})

describe('formatDuration', () => {
  it.each([
    [0.4, '<1 ms'],
    [18.6, '19 ms'],
    // Sub-second latency is what a gateway is judged on, so 840ms must not
    // round to "1 s".
    [840, '840 ms'],
    [1102.2, '1.10 s'],
    [30000, '30.0 s'],
  ])('formats %sms as %s', (ms, expected) => {
    expect(formatDuration(ms)).toBe(expected)
  })

  it('reports an absent measurement rather than zero', () => {
    expect(formatDuration(null)).toBe('—')
    expect(formatDuration(undefined)).toBe('—')
  })
})

describe('formatUSD', () => {
  it('keeps four decimals below a dollar', () => {
    // A day of small traffic really does cost fractions of a cent; two decimals
    // would render the whole page as $0.00.
    expect(formatUSD(0.006427)).toBe('$0.0064')
    expect(formatUSD(41.8734)).toBe('$41.87')
    expect(formatUSD(0)).toBe('$0')
  })
})

describe('rankDimension by cost', () => {
  it('ranks by spend and drops models the catalog could not price', () => {
    const dimension = {
      'gpt-4o': { count: 96, errors: 3, tokens: 1347, cost_usd: 0.005243, unpriced: 25 },
      'gpt-4o-mini': { count: 66, errors: 3, tokens: 925, cost_usd: 0.000226, unpriced: 16 },
      // Unpriced entirely: listing it at $0 beside models that cost something
      // would read as "this one is free", which is precisely what is unknown.
      local: { count: 40, errors: 0, tokens: 800, cost_usd: 0, unpriced: 40 },
    }
    expect(rankDimension(dimension, 'cost_usd', 5).map((e) => e.name)).toEqual(['gpt-4o', 'gpt-4o-mini'])
  })
})
