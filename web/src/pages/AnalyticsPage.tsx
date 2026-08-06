import { CalendarDays, Filter, RefreshCw } from 'lucide-react'
import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Button, EmptyState, LoadingState, Notice, PageHeader } from '../components/ui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { SeriesChart } from '../components/SeriesChart'
import { TOKEN_BANDS, TRAFFIC_BANDS } from '../lib/chartBands'
import { cn } from '@/lib/utils'
import { ApiError, request } from '../lib/api'
import { buildSince, formatNumber } from '../lib/format'
import { hoursFrom, TIME_RANGES } from '../lib/timeRange'
import {
  formatDuration,
  formatUSD,
  measureOf,
  metricTone,
  rankDimension,
  requestTotals,
  spend,
  tokenSplit,
  type RankMeasure,
} from '../lib/logs'
import { useLoad } from '../lib/useLoad'
import type { DimensionStat, LogsStats, Percentiles } from '../types'

/**
 * How many buckets to ask the gateway for.
 *
 * The series is computed server-side over the whole range. Asking for a fixed
 * count keeps the bar width stable as the range changes, rather than drawing an
 * hour and a week at wildly different densities.
 */
const SERIES_BUCKETS = 48

/** How many groups a ranked panel shows before it stops being a ranking. */
const RANK_LIMIT = 6

/**
 * How many groups per dimension to ask the gateway for.
 *
 * Deliberately above `RANK_LIMIT`. The server keeps the highest-*count* groups
 * when this caps `by_provider`/`by_model`, but this page also ranks by cost and
 * by tokens — so asking for exactly what is displayed would drop the
 * low-traffic, expensive model that the cost ranking exists to surface. The
 * margin is what lets the three rankings disagree with the server's ordering.
 */
const GROUP_LIMIT = 10

/** The group the gateway aggregates rows it could not attribute under. */
const UNATTRIBUTED = 'unknown'

/**
 * How often to re-read the statistics while the page is open.
 *
 * An operator watching an incident should see it move without reaching for
 * refresh. A minute is slow enough that a range covering a week is not
 * recomputed every few seconds for a picture that barely changes.
 */
const REFETCH_INTERVAL_MS = 60_000

/** The plugin stages a request-log row can carry. */
const STAGES = ['before_request', 'after_request', 'on_error'] as const

/**
 * A 501 from the statistics endpoint, kept as data rather than raised.
 *
 * The gateway answers 501 when no request-log store is configured — a
 * deployment choice, not a fault — so it must reach the page as its own state
 * and get its own explanation instead of a red failure banner. LogsPage and
 * KeysPage treat their own 501s the same way.
 */
const STORE_DISABLED = 'store-disabled'

type StatsLoad = LogsStats | typeof STORE_DISABLED

/** The selected stage, or '' for every stage. Same URL-supplied caveat. */
function stageFrom(params: URLSearchParams): string {
  const stage = params.get('stage') ?? ''
  return (STAGES as readonly string[]).includes(stage) ? stage : ''
}

/**
 * Provider groups with the unattributed rows removed.
 *
 * Only an `after_request` row carries a provider: a request is logged before one
 * is chosen, and a failure is logged without ever reaching one. Those rows land
 * under "unknown", which then takes first place in the ranking and reads as a
 * provider that served the most traffic — the opposite of what it means.
 *
 * Removed before ranking, not after, or excluding it inside the top six would
 * quietly return five.
 */
function answeredByProvider(dimension: Record<string, DimensionStat>): Record<string, DimensionStat> {
  return Object.fromEntries(Object.entries(dimension).filter(([name]) => name !== UNATTRIBUTED))
}

function Metric({
  label,
  value,
  detail,
  tone,
}: {
  label: string
  value: string
  detail: ReactNode
  tone?: string
}) {
  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3">
      <span className="text-sm text-muted-foreground">{label}</span>
      <strong className={cn('block text-xl font-semibold tabular-nums', tone ?? 'text-foreground')}>{value}</strong>
      <small className="text-xs text-muted-foreground">{detail}</small>
    </div>
  )
}

function Panel({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children: ReactNode
}) {
  const headingId = title.toLowerCase().replaceAll(' ', '-') + '-heading'
  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
      <div>
        <h2 className="text-sm font-semibold text-foreground" id={headingId}>{title}</h2>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
      {children}
    </section>
  )
}

/**
 * A ranked bar list over one dimension.
 *
 * `measure` sets both the ordering and the bar length, because the two
 * questions differ: the model called most often is routinely not the model
 * consuming the tokens, and ranking by call count hides that.
 */
/**
 * A measured distribution, shown as percentiles rather than a mean.
 *
 * A mean latency hides the tail an operator is paged about: one request in
 * twenty taking ten seconds barely moves it. p95 and p99 are what that looks
 * like, so they lead and the mean sits beside them for scale.
 */
function Distribution({
  percentiles,
  emptyLabel,
}: {
  percentiles: Percentiles | null | undefined
  emptyLabel: string
}) {
  if (!percentiles || percentiles.count === 0) {
    return <p className="text-sm text-muted-foreground">{emptyLabel}</p>
  }
  const rows: Array<[string, number]> = [
    ['p50', percentiles.p50],
    ['p95', percentiles.p95],
    ['p99', percentiles.p99],
    ['max', percentiles.max],
  ]
  return (
    <div className="flex flex-col gap-2">
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1.5">
        {rows.map(([label, value]) => (
          <div className="flex items-baseline justify-between gap-2 text-sm" key={label}>
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="font-medium tabular-nums text-foreground">{formatDuration(value)}</dd>
          </div>
        ))}
      </dl>
      <p className="text-xs text-muted-foreground">
        {formatNumber(percentiles.count)} measured · mean {formatDuration(percentiles.mean)}
      </p>
    </div>
  )
}

function Ranking({
  entries,
  measure,
  emptyLabel,
}: {
  entries: Array<{ name: string; stat: DimensionStat }>
  measure: RankMeasure
  emptyLabel: string
}) {
  if (entries.length === 0) {
    return <p className="text-sm text-muted-foreground">{emptyLabel}</p>
  }
  const peak = Math.max(1e-9, ...entries.map((entry) => measureOf(entry.stat, measure)))
  return (
    <ol className="flex flex-col gap-2">
      {entries.map(({ name, stat }) => {
        // Only meaningful beside a cost: a group's unpriced rows are exactly
        // the ones missing from its cost, and a ranking built mostly from them
        // reads as authoritative unless each row says how much it is missing.
        // Tokens and counts include those rows already.
        const unpriced = measure === 'cost_usd' ? (stat.unpriced ?? 0) : 0
        return (
          <li key={name}>
            <div className="flex items-baseline justify-between gap-2 text-sm">
              <code className="truncate font-mono text-xs text-foreground">{name}</code>
              <span className="shrink-0 tabular-nums text-muted-foreground">
                {measure === 'cost_usd' ? formatUSD(measureOf(stat, measure)) : formatNumber(measureOf(stat, measure))}
                {unpriced > 0 ? <span className="text-warning"> · {formatNumber(unpriced)} unpriced</span> : null}
                {stat.errors > 0 ? <span className="text-danger"> · {formatNumber(stat.errors)} failed</span> : null}
              </span>
            </div>
            <meter
              aria-label={`${name}: ${measureOf(stat, measure)} ${measure}`}
              className="ranking-meter h-1.5 w-full"
              max={peak}
              min={0}
              value={measureOf(stat, measure)}
            >
              {measureOf(stat, measure)}
            </meter>
          </li>
        )
      })}
    </ol>
  )
}

export default function AnalyticsPage() {
  // The range and the filters live in the URL because a filtered view is the
  // unit an incident is discussed in: "the failures on anthropic in the last
  // hour" has to survive being pasted into a channel and reloaded.
  const [params, setParams] = useSearchParams()
  const hours = hoursFrom(params)
  const provider = params.get('provider') ?? ''
  const model = params.get('model') ?? ''
  const stage = stageFrom(params)

  const [draft, setDraft] = useState({ provider, model, stage })

  // A link opened with filters already in it, and the back button, both change
  // the applied filters without touching the form. Without this the inputs keep
  // showing whatever was last typed and misdescribe the data beside them.
  useEffect(() => {
    setDraft({ provider, model, stage })
  }, [provider, model, stage])

  const updateQuery = (changes: Record<string, string>) => {
    // Replace rather than push: flicking between ranges would otherwise bury
    // the page the operator arrived from under a dozen history entries.
    setParams(
      (current) => {
        const next = new URLSearchParams(current)
        for (const [key, value] of Object.entries(changes)) {
          if (value) next.set(key, value)
          else next.delete(key)
        }
        return next
      },
      { replace: true },
    )
  }

  const applyFilters = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    updateQuery({ provider: draft.provider.trim(), model: draft.model.trim(), stage: draft.stage })
  }

  const { data, loading, error, refresh } = useLoad<StatsLoad>(
    (signal) => {
      const query = new URLSearchParams({
        since: buildSince(hours),
        buckets: String(SERIES_BUCKETS),
        // Caps `by_provider` and `by_model` server-side. `by_stage` is never
        // capped, so the request totals below stay derivable.
        limit: String(GROUP_LIMIT),
      })
      if (provider) query.set('provider', provider)
      if (model) query.set('model', model)
      if (stage) query.set('stage', stage)
      return request<LogsStats>(`/admin/logs/stats?${query.toString()}`, { signal }).catch((loadError: unknown) => {
        if (loadError instanceof ApiError && loadError.status === 501) return STORE_DISABLED
        throw loadError
      })
    },
    [hours, provider, model, stage],
    'Analytics could not be loaded.',
    { refetchInterval: REFETCH_INTERVAL_MS },
  )

  const storeDisabled = data === STORE_DISABLED
  const stats = storeDisabled ? null : data

  // Every count below is scoped to the filtered stage, so a filter naming one
  // terminal stage makes the failure rate a tautology: filtered to
  // `after_request` nothing failed, filtered to `on_error` everything did.
  // Reported as underivable rather than as a confident, green 0%.
  const stageScoped = stage !== ''
  const totals = requestTotals(stats)
  const split = tokenSplit(stats)
  const cost = spend(stats)
  const totalTokens = stats?.summary.total_tokens ?? 0
  // A stats object with zero recorded entries is a genuinely idle gateway, not
  // a failure to derive numbers from — show that plainly rather than a metric
  // strip of confident zeros, which reads as "0% errors" instead of "no data".
  const isEmpty = stats != null && stats.summary.total_entries === 0

  const tokensPerRequest = totals.completed > 0 ? Math.round(totalTokens / totals.completed) : null
  const topErrors = stats?.top_errors ?? []
  const series = stats?.series

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Analytics"
        description="Traffic, token consumption, and failures across the selected range."
        actions={
          <>
            <label className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-sm text-foreground">
              <CalendarDays aria-hidden="true" className="text-muted-foreground" size={18} />
              <span className="sr-only">Time range</span>
              <select
                className="bg-transparent text-sm outline-none"
                value={hours}
                onChange={(event) => updateQuery({ hours: event.target.value })}
              >
                {TIME_RANGES.map((range) => (
                  <option key={range.hours} value={range.hours}>{range.label}</option>
                ))}
              </select>
            </label>
            <Button aria-label="Refresh analytics" size="icon" variant="ghost" onClick={refresh}>
              <RefreshCw aria-hidden="true" className={loading ? 'animate-spin' : ''} size={18} />
            </Button>
          </>
        }
      />
      {error ? <Notice tone="error">{error}</Notice> : null}

      {storeDisabled ? (
        <EmptyState
          title="Request logging is disabled"
          description="Set the REQUEST_LOG_STORE_BACKEND environment variable on the gateway to persist request logs and derive analytics from them."
        />
      ) : (
        <form
          className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-2.5"
          onSubmit={applyFilters}
        >
          <Label className="min-w-[140px] flex-1">
            <span className="sr-only">Provider</span>
            <Input
              name="provider"
              placeholder="Provider"
              value={draft.provider}
              onChange={(event) => setDraft((current) => ({ ...current, provider: event.target.value }))}
            />
          </Label>
          <Label className="min-w-[140px] flex-1">
            <span className="sr-only">Model</span>
            <Input
              name="model"
              placeholder="Model"
              value={draft.model}
              onChange={(event) => setDraft((current) => ({ ...current, model: event.target.value }))}
            />
          </Label>
          <Select
            value={draft.stage}
            onValueChange={(value) => setDraft((current) => ({ ...current, stage: value ?? current.stage }))}
          >
            <SelectTrigger aria-label="Stage">
              <SelectValue placeholder="All stages" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">All stages</SelectItem>
              <SelectItem value="before_request">Before request</SelectItem>
              <SelectItem value="after_request">After request</SelectItem>
              <SelectItem value="on_error">On error</SelectItem>
            </SelectContent>
          </Select>
          <Button type="submit" variant="outline">
            <Filter aria-hidden="true" size={16} />
            Apply
          </Button>
        </form>
      )}

      {loading && !stats ? <LoadingState label="Loading analytics" /> : null}
      {stats && isEmpty ? (
        <EmptyState
          title="No activity in this range"
          description="Nothing was recorded for the selected time range and filters. Clear a filter, widen the range, or send a request through the Playground."
        />
      ) : null}

      {stats && !isEmpty ? (
        <>
          {/* Refreshes itself every minute, so the figures change without the
              operator acting — polite, not assertive, because nothing here is
              urgent enough to interrupt what is being read. */}
          <section aria-label="Analytics summary" aria-live="polite" className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Metric
              detail={
                totals.degraded
                  ? 'no completed requests recorded'
                  : stageScoped
                    ? `${stage} events in selected range`
                    : 'completed in selected range'
              }
              label="Requests"
              value={totals.degraded ? '—' : formatNumber(totals.completed)}
            />
            <Metric
              detail={
                stageScoped
                  ? 'not derivable while filtered to one stage'
                  : totals.degraded
                    ? 'no completed requests recorded'
                    : `${formatNumber(totals.failed)} failed`
              }
              label="Failure rate"
              tone={totals.degraded || stageScoped ? undefined : metricTone(totals)}
              value={totals.degraded || stageScoped ? '—' : `${totals.errorRate.toFixed(1)}%`}
            />
            <Metric
              detail={
                split
                  ? `${formatNumber(split.input)} in · ${formatNumber(split.output)} out`
                  : 'input/output split not reported'
              }
              label="Tokens"
              value={formatNumber(totalTokens)}
            />
            <Metric
              detail={
                cost == null
                  ? 'not reported by this gateway'
                  : cost.unpriced > 0
                    ? // A floor, and the page has to say so: the difference is
                      // whatever those requests actually cost.
                      `at least — ${formatNumber(cost.unpriced)} requests unpriced`
                    : `${formatNumber(tokensPerRequest ?? 0)} tokens per request`
              }
              label="Estimated cost"
              value={cost == null ? '—' : formatUSD(cost.usd)}
            />
          </section>

          <div className="grid gap-4 lg:grid-cols-3">
            <Panel
              description="End to end, as the gateway saw it — the provider call plus routing, retries and plugins."
              title="Request latency"
            >
              <Distribution
                emptyLabel="No request has recorded a duration yet. Rows written before this gateway measured one carry none."
                percentiles={stats.latency_ms}
              />
            </Panel>
            <Panel
              description="How long a streamed answer takes to start. Non-streaming requests have none."
              title="Time to first token"
            >
              <Distribution
                emptyLabel="No streaming request in this range recorded a time to first token."
                percentiles={stats.ttft_ms}
              />
            </Panel>
            <Panel description="Where the spend goes, priced from the model catalog." title="Models by cost">
              <Ranking
                emptyLabel="No request in this range was priced. The catalog carries no price for these models."
                entries={rankDimension(stats.by_model, 'cost_usd', RANK_LIMIT)}
                measure="cost_usd"
              />
            </Panel>
          </div>

          {series ? (
            <div className="grid gap-4 lg:grid-cols-2">
              <Panel description="Completed requests per interval, split by outcome." title="Traffic">
                <SeriesChart
                  bands={TRAFFIC_BANDS}
                  emptyLabel="No completed requests in this range."
                  series={series}
                  unit="requests"
                />
              </Panel>
              <Panel description="Input and output tokens per interval." title="Token throughput">
                <SeriesChart
                  bands={TOKEN_BANDS}
                  emptyLabel="No token usage was reported in this range."
                  series={series}
                  unit="tokens"
                />
              </Panel>
            </div>
          ) : (
            <Notice>
              This gateway does not report a time series, so traffic and token trends are unavailable. The dashboard
              deploys separately from the gateway and is currently pointed at an older one.
            </Notice>
          )}

          <div className="grid gap-4 lg:grid-cols-3">
            <Panel
              description="Where the token budget goes — not which model is called most."
              title="Models by tokens"
            >
              <Ranking
                emptyLabel="No token usage was attributed to a model."
                entries={rankDimension(stats.by_model, 'tokens', RANK_LIMIT)}
                measure="tokens"
              />
            </Panel>
            <Panel
              description="Requests a provider answered. Failures never reach one, so they appear under Top failures instead."
              title="Providers by answered requests"
            >
              <Ranking
                emptyLabel="No provider answered a request in this range."
                entries={rankDimension(answeredByProvider(stats.by_provider), 'count', RANK_LIMIT)}
                measure="count"
              />
            </Panel>
            <Panel description="The most frequent error messages in this range." title="Top failures">
              {topErrors.length === 0 ? (
                <p className="text-sm text-muted-foreground">No failures were recorded in this range.</p>
              ) : (
                <ol className="flex flex-col gap-2">
                  {topErrors.map((group) => (
                    <li className="flex items-baseline justify-between gap-3 text-sm" key={group.message}>
                      <span className="min-w-0 break-words text-danger">{group.message}</span>
                      <span className="shrink-0 tabular-nums text-muted-foreground">{formatNumber(group.count)}</span>
                    </li>
                  ))}
                </ol>
              )}
            </Panel>
          </div>
        </>
      ) : null}
    </div>
  )
}
