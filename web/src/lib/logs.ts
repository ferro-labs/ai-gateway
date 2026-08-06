import type { DimensionStat, LogsStats, RequestLog } from '../types'

/**
 * Whether a request-log row represents a failure.
 *
 * This is a business rule, not a formatting concern: it decides what the
 * dashboard counts as a failed request. It lives here because three separate
 * call sites previously each carried their own copy, so adding a failure stage
 * server-side would have updated some and not others — leaving the chart and
 * the table disagreeing about the same data.
 */
export function logFailed(log: RequestLog): boolean {
  return Boolean(log.error_message) || log.stage === STAGE_ON_ERROR
}

const STAGE_BEFORE = 'before_request'
const STAGE_AFTER = 'after_request'
const STAGE_ON_ERROR = 'on_error'

/** Request-level facts derived from the stage-partitioned log statistics. */
export interface RequestTotals {
  /** Requests that reached an outcome: succeeded or failed. */
  completed: number
  /** Requests that failed. */
  failed: number
  /** Failure percentage of completed requests, 0 when nothing has completed. */
  errorRate: number
  /**
   * True when the statistics carry no stage breakdown, so request-level counts
   * could not be derived and only raw event counts are available.
   */
  degraded: boolean
}

/**
 * Derives request counts from stage-partitioned log statistics.
 *
 * `summary.total_entries` now counts requests rather than rows, so this no
 * longer exists to correct it. It reads `by_stage` for a different reason: that
 * is the only place the difference between an idle gateway and a logger
 * recording no terminal stage survives. Both leave the summary at zero, and
 * only the presence of `before_request` rows tells them apart — reported as
 * `degraded` rather than as a confident zero.
 *
 * In-flight requests stay excluded: they have written a `before_request` row
 * but have no outcome yet, so counting them would understate the failure rate
 * for as long as they are open.
 */
export function requestTotals(stats: LogsStats | null | undefined): RequestTotals {
  const byStage = stats?.by_stage
  if (!byStage) {
    return { completed: 0, failed: 0, errorRate: 0, degraded: true }
  }

  const succeeded = byStage[STAGE_AFTER]?.count ?? 0
  const failed = byStage[STAGE_ON_ERROR]?.count ?? 0
  const completed = succeeded + failed

  // A logger configured to record neither terminal stage leaves nothing to
  // derive a request count from. Report that rather than showing a confident
  // zero, which is indistinguishable from a genuinely idle gateway.
  if (completed === 0 && (byStage[STAGE_BEFORE]?.count ?? 0) > 0) {
    return { completed: 0, failed: 0, errorRate: 0, degraded: true }
  }

  return {
    completed,
    failed,
    errorRate: completed > 0 ? (failed / completed) * 100 : 0,
    degraded: false,
  }
}

/** The measures a dimension can be ranked by. Cost is absent on an older gateway. */
export type RankMeasure = 'count' | 'tokens' | 'cost_usd'

/** A group's value for one measure, treating an absent cost as zero. */
export function measureOf(stat: DimensionStat, measure: RankMeasure): number {
  return measure === 'cost_usd' ? (stat.cost_usd ?? 0) : stat[measure]
}

/**
 * A dimension's groups, ranked by a chosen measure, highest first.
 *
 * Groups measuring zero are dropped rather than listed at the bottom: on the
 * cost ranking they are the models the catalog cannot price, and a row of
 * "$0" against a model that certainly cost something is worse than its absence.
 */
export function rankDimension(
  dimension: Record<string, DimensionStat> | undefined,
  measure: RankMeasure,
  limit: number,
): Array<{ name: string; stat: DimensionStat }> {
  return Object.entries(dimension ?? {})
    .map(([name, stat]) => ({ name, stat }))
    .filter((entry) => measureOf(entry.stat, measure) > 0)
    .sort((a, b) => measureOf(b.stat, measure) - measureOf(a.stat, measure) || a.name.localeCompare(b.name))
    .slice(0, limit)
}

/**
 * Input and output token totals for the range.
 *
 * They are reported separately because they are not interchangeable: output
 * tokens are billed at a multiple of input tokens on every major provider, so a
 * single "total tokens" figure hides the thing that actually moves the bill.
 *
 * `null` when the gateway did not send the split — an older one, since the
 * dashboard deploys independently — so the page can say "not reported" instead
 * of rendering a confident zero.
 */
export function tokenSplit(stats: LogsStats | null | undefined): { input: number; output: number } | null {
  const input = stats?.summary.prompt_tokens
  const output = stats?.summary.completion_tokens
  if (input == null || output == null) return null
  return { input, output }
}

/**
 * Estimated spend for the range, with what it could not price.
 *
 * `usd` is a floor rather than a total: a request whose model the catalog does
 * not price contributes nothing to it. `unpriced` is how many such requests
 * there were, so a caller can say "at least $X, and N requests are unpriced"
 * instead of presenting a floor as the bill.
 *
 * `null` when the gateway sent no cost at all — an older one, since the
 * dashboard deploys independently.
 */
export function spend(stats: LogsStats | null | undefined): { usd: number; unpriced: number } | null {
  const usd = stats?.summary.cost_usd
  if (usd == null) return null
  return { usd, unpriced: stats?.summary.unpriced_requests ?? 0 }
}

/**
 * Formats a millisecond duration at a readable precision.
 *
 * Sub-second latency is what a gateway is judged on, and rounding 840ms to "1s"
 * discards the digit that matters.
 */
export function formatDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms)) return '—'
  if (ms < 1) return '<1 ms'
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
}

/**
 * Formats estimated spend.
 *
 * Four decimals below a dollar: a single request routinely costs a fraction of
 * a cent, and two decimals would render most of a day's traffic as $0.00.
 */
export function formatUSD(usd: number): string {
  if (!Number.isFinite(usd)) return '—'
  if (usd === 0) return '$0'
  if (usd < 1) return `$${usd.toFixed(4)}`
  return `$${usd.toFixed(2)}`
}

/**
 * The metric class for a failure rate.
 *
 * Degraded totals get the neutral tone rather than the healthy one: an unknown
 * rate is not a good rate, and colouring it green is the same category of lie
 * this module exists to remove.
 */
export function metricTone(totals: RequestTotals): string {
  if (totals.degraded) return 'text-foreground'
  if (totals.errorRate >= 5) return 'text-danger'
  if (totals.errorRate >= 1) return 'text-warning'
  return 'text-success'
}
