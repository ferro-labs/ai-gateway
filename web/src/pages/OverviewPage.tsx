import { CalendarDays, RefreshCw } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { SeriesChart } from '../components/SeriesChart'
import { TRAFFIC_BANDS } from '../lib/chartBands'
import { Button, EmptyState, LoadingState, Notice, PageHeader, StatusPill } from '../components/ui'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { logFailed, metricTone, requestTotals } from '../lib/logs'
import { buildSince, formatNumber, timeAgo } from '../lib/format'
import { DEFAULT_HOURS, hoursFrom, TIME_RANGES } from '../lib/timeRange'
import { request, requestProbe } from '../lib/api'
import { useLoad } from '../lib/useLoad'
import type { AdminHealth, DashboardSummary, LogsResponse, LogsStats, MCPServerHealth, Readiness, RequestLog } from '../types'

interface OverviewData {
  summary: DashboardSummary
  stats: LogsStats | null
  logs: RequestLog[]
  health: AdminHealth
  /** Null when /readyz itself could not be reached — see `loadOverview`. */
  readiness: Readiness | null
}

/**
 * Rows fetched for the Recent Requests table, which shows the newest handful.
 *
 * This used to be 200, because the chart was bucketed here from raw rows. It no
 * longer is: the gateway aggregates the series over the whole range, so the
 * chart is no longer limited to whatever one page of the log happened to cover
 * — a few seconds of traffic on a busy gateway, drawn as if it were the day.
 */
const RECENT_LOGS_LIMIT = 12

/** Buckets requested for the traffic chart; matches the Analytics page. */
const SERIES_BUCKETS = 48

/**
 * How often the whole page re-reads itself.
 *
 * This is the page an operator leaves open beside an incident, so it has to move
 * without being asked. Thirty seconds is also the floor: `/readyz` is guarded by
 * a process-global 10 rps limiter shared with every other caller — orchestrator
 * probes included — and a dashboard is not entitled to spend that budget.
 */
const REFRESH_MS = 30_000

async function loadOverview(hours: number, signal: AbortSignal): Promise<OverviewData> {
  const since = buildSince(hours)
  const [summary, health, statsResult, logsResult, readiness] = await Promise.all([
    request<DashboardSummary>('/admin/dashboard', { signal }),
    request<AdminHealth>('/admin/health', { signal }),
    request<LogsStats>(
      `/admin/logs/stats?since=${encodeURIComponent(since)}&buckets=${SERIES_BUCKETS}`,
      { signal },
    ).catch(() => null),
    request<LogsResponse>(`/admin/logs?limit=${RECENT_LOGS_LIMIT}&since=${encodeURIComponent(since)}`, { signal }).catch(() => null),
    // A 503 from /readyz is the answer, not a failure — `requestProbe` returns
    // its body. Null therefore means the endpoint could not be read at all,
    // which is a fact about one panel and must not blank the rest of the page.
    requestProbe<Readiness>('/readyz', { signal }).catch(() => null),
  ])
  return {
    summary,
    health,
    stats: statsResult,
    logs: logsResult?.data ?? [],
    readiness,
  }
}

/**
 * Why the request metrics have no value to show.
 *
 * "Logging is off" and "the statistics query failed" are different facts and
 * need different words: the first is a configuration choice, the second is a
 * fault. Reporting the fault as the choice tells an operator their gateway is
 * behaving as configured when it is not.
 */
function statsUnavailableReason(data: OverviewData): string | null {
  if (data.stats) return null
  return data.summary.request_logs.enabled ? 'statistics unavailable' : 'logging disabled'
}

/**
 * Whether the gateway is in rotation, and what is keeping it out.
 *
 * Separate from the component list above it: a component reports a subsystem's
 * own state, whereas this is the single answer a load balancer acts on. An
 * unready gateway serves nothing at all, so it cannot be one grey row among
 * several.
 */
function ReadinessPanel({ readiness, servers }: { readiness: Readiness | null; servers: MCPServerHealth[] }) {
  const ready = readiness?.status === 'ready'

  return (
    <div className="flex flex-col gap-2">
      {readiness ? (
        <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm">
          <span>Serving traffic</span>
          <StatusPill tone={ready ? 'success' : 'error'}>{ready ? 'Ready' : 'Not ready'}</StatusPill>
        </div>
      ) : (
        <p className="rounded-md border border-border px-3 py-2 text-sm text-muted-foreground">
          Readiness could not be read, so whether this gateway is in rotation is unknown.
        </p>
      )}
      {/*
       * The reason is the whole point of a 503 here: the gateway refuses every
       * request while it is unready, and this string is the only thing that says
       * which of its dependencies caused that. It is deliberately generic — the
       * endpoint is unauthenticated, so the underlying error, which can quote a
       * DSN or an authorization header, is withheld there. When an MCP server is
       * what went wrong, the per-server reason below is that missing detail.
       */}
      {readiness?.reason ? (
        <p className="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger">
          No traffic is being accepted: {readiness.reason}.
        </p>
      ) : null}

      {servers.length > 0 ? (
        <div className="flex flex-col gap-2">
          <h4 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">MCP servers</h4>
          <ul className="flex flex-col gap-2">
            {servers.map((server) => {
              // A required server that is down takes the instance out of
              // rotation, so it is a failure of the gateway rather than of one
              // tool source. An optional one only costs its own tools.
              const blocking = !server.ready && server.required
              return (
                <li className="rounded-md border border-border px-3 py-2 text-sm" key={server.name}>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="min-w-0 font-medium break-all text-foreground">{server.name}</span>
                    <span className="flex shrink-0 items-center gap-1.5">
                      {server.required ? <StatusPill tone="neutral">Required</StatusPill> : null}
                      <StatusPill tone={server.ready ? 'success' : blocking ? 'error' : 'warning'}>
                        {server.ready ? 'Ready' : 'Unready'}
                      </StatusPill>
                    </span>
                  </div>
                  {blocking ? (
                    <p className="mt-1 text-xs text-danger">
                      Every request through this gateway is refused while this server is unready, including requests that use no tools.
                    </p>
                  ) : null}
                  {!server.ready && !server.required ? (
                    <p className="mt-1 text-xs text-muted-foreground">
                      Its tools are withdrawn from the model. Requests that do not call them are unaffected.
                    </p>
                  ) : null}
                  {/*
                   * Why it is down, which used to be readable only in the
                   * gateway's own logs. It reaches this page from the
                   * authenticated /admin/health and nowhere else; the
                   * unauthenticated /readyz withholds it because it can quote a
                   * server URL, a header, or a command line.
                   */}
                  {server.last_error ? (
                    <p className="mt-1 font-mono text-xs break-all text-muted-foreground">{server.last_error}</p>
                  ) : null}
                </li>
              )
            })}
          </ul>
          {/*
           * Said plainly because the pills above cannot say it: readiness is
           * measured at the handshake, and only a stdio server's death is
           * noticed afterwards. An operator who reads "Ready" as "answering
           * right now" will trust a dead HTTP server through an incident.
           */}
          <p className="text-xs text-muted-foreground">
            Ready means the server completed its handshake. A subprocess server that later exits is withdrawn; an HTTP server that stops answering is not detected and keeps reporting ready.
          </p>
        </div>
      ) : null}
    </div>
  )
}

export default function OverviewPage() {
  // The range lives in the URL so a reload keeps it and a link carries it: the
  // window an incident is discussed in is part of what gets pasted into a chat.
  const [params, setParams] = useSearchParams()
  const hours = hoursFrom(params)

  const { data, loading, error, refresh } = useLoad(
    (signal) => loadOverview(hours, signal),
    [hours],
    'Dashboard data could not be loaded.',
    { refetchInterval: REFRESH_MS },
  )

  const totals = requestTotals(data?.stats)
  const unavailable = data ? statsUnavailableReason(data) : null

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Overview"
        description="Monitor gateway traffic and provider health."
        actions={
          <>
            <label className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-sm text-foreground">
              <CalendarDays aria-hidden="true" className="text-muted-foreground" size={18} />
              <span className="sr-only">Time range</span>
              <select
                className="bg-transparent text-sm outline-none"
                value={hours}
                onChange={(event) => {
                  const chosen = event.target.value
                  // Replace rather than push: flicking between ranges would
                  // otherwise bury the page the operator arrived from under a
                  // dozen history entries. The default is dropped from the URL
                  // so the common link stays short.
                  setParams(
                    (current) => {
                      const next = new URLSearchParams(current)
                      if (Number(chosen) === DEFAULT_HOURS) next.delete('hours')
                      else next.set('hours', chosen)
                      return next
                    },
                    { replace: true },
                  )
                }}
              >
                {TIME_RANGES.map((range) => (
                  <option key={range.hours} value={range.hours}>{range.label}</option>
                ))}
              </select>
            </label>
            <Button aria-label="Refresh overview" size="icon" variant="ghost" onClick={refresh}>
              <RefreshCw aria-hidden="true" className={loading ? 'animate-spin' : ''} size={18} />
            </Button>
          </>
        }
      />

      {error ? (
        <Notice tone="error">
          {error} <Button size="sm" variant="link" onClick={refresh}>Try again</Button>
        </Notice>
      ) : null}
      {loading && !data ? <LoadingState label="Loading gateway overview" /> : null}

      {data ? (
        <>
          <section aria-label="Gateway summary" className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div className="rounded-lg border border-border bg-card px-4 py-3">
              <span className="text-sm text-muted-foreground">Providers</span>
              <strong className="block text-xl font-semibold text-foreground">{formatNumber(data.summary.providers.available)}</strong>
              <small className="text-xs text-muted-foreground">/ {formatNumber(data.summary.providers.total)} registered</small>
            </div>
            <div className="rounded-lg border border-border bg-card px-4 py-3">
              <span className="text-sm text-muted-foreground">Active API Keys</span>
              <strong className="block text-xl font-semibold text-foreground">{formatNumber(data.summary.keys.active)}</strong>
              <small className="text-xs text-muted-foreground">{formatNumber(data.summary.keys.expired)} expired</small>
            </div>
            <div className="rounded-lg border border-border bg-card px-4 py-3">
              <span className="text-sm text-muted-foreground">Requests</span>
              <strong className="block text-xl font-semibold text-foreground">{unavailable || totals.degraded ? '—' : formatNumber(totals.completed)}</strong>
              <small className="text-xs text-muted-foreground">{unavailable ?? (totals.degraded ? 'no completed requests recorded' : `completed in the last ${hours} hours`)}</small>
            </div>
            <div className="rounded-lg border border-border bg-card px-4 py-3">
              <span className="text-sm text-muted-foreground">Error Rate</span>
              <strong className={cn('block text-xl font-semibold', unavailable ? 'text-foreground' : metricTone(totals))}>
                {unavailable || totals.degraded ? '—' : `${totals.errorRate.toFixed(1)}%`}
              </strong>
              <small className="text-xs text-muted-foreground">{unavailable ?? (totals.degraded ? 'no completed requests recorded' : `${formatNumber(totals.failed)} failed`)}</small>
            </div>
          </section>

          <div className="grid gap-4 lg:grid-cols-3">
            <section aria-labelledby="traffic-heading" className="rounded-lg border border-border bg-card p-4 lg:col-span-2">
              <div className="mb-3">
                <h2 className="text-sm font-semibold text-foreground" id="traffic-heading">Requests over time</h2>
                <p className="text-sm text-muted-foreground">Latest persisted activity in the selected range.</p>
              </div>
              {!data.summary.request_logs.enabled ? (
                <EmptyState title="Request logging is disabled" description="Enable the request logger plugin to see traffic history and analytics." />
              ) : data.stats?.series ? (
                <SeriesChart
                  bands={TRAFFIC_BANDS}
                  emptyLabel="No completed requests in this range."
                  series={data.stats.series}
                  unit="requests"
                />
              ) : (
                <EmptyState title="No traffic history" description="This gateway did not report a time series for the selected range." />
              )}
            </section>

            <section aria-labelledby="health-heading" className="rounded-lg border border-border bg-card p-4">
              <div className="mb-3">
                <h2 className="text-sm font-semibold text-foreground" id="health-heading">Gateway health</h2>
                <p className="text-sm text-muted-foreground">Current control-plane state.</p>
              </div>
              <div className="flex flex-col gap-3">
                <ul className="flex flex-col gap-2">
                  {(data.health.components ?? []).map((component) => {
                    const tone = component.status === 'healthy' ? 'success' : component.status === 'disabled' ? 'neutral' : 'warning'
                    return (
                      <li className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm" key={component.name}>
                        <span>{component.name}</span>
                        <StatusPill tone={tone}>{component.status.replace('_', ' ')}</StatusPill>
                      </li>
                    )
                  })}
                </ul>
                {/*
                 * Announced, because the panel re-reads itself on a timer: a
                 * gateway dropping out of rotation while the operator is
                 * reading the traffic chart is the one change on this page
                 * worth interrupting for.
                 */}
                <div aria-live="polite" className="flex flex-col gap-2 border-t border-border pt-3">
                  <h3 className="text-sm font-semibold text-foreground">Readiness</h3>
                  {/*
                   * The server list comes from /admin/health rather than from
                   * /readyz beside it: that body omits mcp_servers entirely on a
                   * 503, so the list disappeared exactly when a required server
                   * was down — the one case an operator opens this panel for.
                   */}
                  <ReadinessPanel readiness={data.readiness} servers={data.health.mcp_servers ?? []} />
                </div>
              </div>
            </section>
          </div>

          <section aria-labelledby="recent-heading" className="rounded-lg border border-border bg-card p-4">
            <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
              <div>
                <h2 className="text-sm font-semibold text-foreground" id="recent-heading">Recent Requests</h2>
                <p className="text-sm text-muted-foreground">Newest gateway events in the selected range.</p>
              </div>
              <Link className="text-sm font-medium text-primary hover:underline" to="/logs">View all requests</Link>
            </div>
            {data.logs.length === 0 ? (
              <EmptyState
                title={data.summary.request_logs.enabled ? 'No requests in this range' : 'Request logging is disabled'}
                description={data.summary.request_logs.enabled ? 'Try a request in the Playground or choose a wider time range.' : 'Enable request logging to populate this table.'}
                action={data.summary.request_logs.enabled ? <Button nativeButton={false} render={<Link to="/playground" />} variant="secondary">Open Playground</Button> : undefined}
              />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Time</TableHead>
                    {/* Second, as in the full log table — the two are read the same way. */}
                    <TableHead>Status</TableHead>
                    <TableHead>Provider</TableHead>
                    <TableHead>Model</TableHead>
                    <TableHead className="text-right">Tokens</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.logs.slice(0, 6).map((log, index) => {
                    const failed = logFailed(log)
                    return (
                      <TableRow key={`${log.trace_id}-${log.stage}-${index}`}>
                        <TableCell className="tabular-nums text-muted-foreground">{timeAgo(log.created_at)}</TableCell>
                        <TableCell><StatusPill tone={failed ? 'error' : 'success'}>{failed ? 'Error' : 'OK'}</StatusPill></TableCell>
                        <TableCell>{log.provider || '—'}</TableCell>
                        <TableCell><code className="font-mono text-xs">{log.model || '—'}</code></TableCell>
                        <TableCell className="text-right tabular-nums">{formatNumber(log.total_tokens)}</TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            )}
          </section>
        </>
      ) : null}
    </div>
  )
}
