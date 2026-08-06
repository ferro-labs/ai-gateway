import { ChevronDown, ChevronRight, Filter, RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { DataTable, type DataTableColumn } from '../components/DataTable'
import { Button, CopyButton, EmptyState, Notice, PageHeader, StatusPill } from '../components/ui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ApiError, request } from '../lib/api'
import { buildSince, formatNumber, formatTimestamp, timeAgo } from '../lib/format'
import { DEFAULT_HOURS, hoursFrom, offsetFrom, TIME_RANGES } from '../lib/timeRange'
import { useLoad } from '../lib/useLoad'
import type { AuditEntry, AuditOutcome, AuditResponse } from '../types'

interface Filters {
  action: string
  actorId: string
  outcome: string
  hours: number
}

const pageSize = 50
/** The three the handler accepts; anything else is a 400. */
const OUTCOMES: readonly string[] = ['ok', 'denied', 'error']
/** Long enough that a watched page stays current, slow enough to stay cheap. */
const REFRESH_MS = 30000

/**
 * How each outcome is shown, and the one line of this page that is easy to get
 * backwards.
 *
 * `denied` is not a fault: it is a refusal the gateway enforced, and "someone
 * tried to delete the last admin key and was refused" is precisely what this
 * trail is read to answer. Rendering it in the error tone would send an operator
 * hunting for a broken gateway on evidence that the gateway held. The error tone
 * belongs to `error` alone — the action was permitted and then failed.
 */
const OUTCOME_PRESENTATION: Record<AuditOutcome, { label: string; tone: 'success' | 'warning' | 'error' }> = {
  ok: { label: 'OK', tone: 'success' },
  denied: { label: 'Denied', tone: 'warning' },
  error: { label: 'Error', tone: 'error' },
}

/**
 * The filter set, read from the URL rather than component state.
 *
 * An audit investigation is pasted into an incident channel, and a reload must
 * not drop back to "last 24 hours, everything". An `outcome` the handler does
 * not accept is dropped rather than forwarded: sent on it is a 400, and left in
 * the Select it is a value with no option to match it.
 */
function parseFilters(params: URLSearchParams): Filters {
  const outcome = params.get('outcome') ?? ''
  return {
    action: params.get('action') ?? '',
    actorId: params.get('actor_id') ?? '',
    outcome: OUTCOMES.includes(outcome) ? outcome : '',
    hours: hoursFrom(params),
  }
}

// Defaults are omitted so the interesting part of a shared link is the part
// that was actually changed.
function toParams(filters: Filters, offset: number): URLSearchParams {
  const params = new URLSearchParams()
  if (filters.action) params.set('action', filters.action)
  if (filters.actorId) params.set('actor_id', filters.actorId)
  if (filters.outcome) params.set('outcome', filters.outcome)
  if (filters.hours !== DEFAULT_HOURS) params.set('hours', String(filters.hours))
  if (offset > 0) params.set('offset', String(offset))
  return params
}

/**
 * A 501 is the audit store being unconfigured — a deployment choice, not a
 * fault — so it is carried as a value rather than thrown. Left as a rejection it
 * would reach the shared loader's error path and render as a red failure banner
 * telling an operator to fix something that is not broken. LogsPage and
 * KeysPage carry their own 501s the same way.
 */
type AuditLoad = { disabled: true } | { disabled: false; response: AuditResponse }

/**
 * `detail` as indented JSON when it parses, and verbatim when it does not.
 *
 * The server composes it as JSON, but it is stored after redaction — a
 * substitution inside a quoted value can leave text that no longer parses. The
 * stored characters are still the evidence, so a parse failure shows them
 * rather than swallowing the row's only description of what changed.
 */
function formatDetail(detail: string): string {
  try {
    return JSON.stringify(JSON.parse(detail) as unknown, null, 2)
  } catch {
    return detail
  }
}

/** Distinguishes rows in a trail that has no id column of its own. */
function rowKey(entry: AuditEntry, index: number): string {
  return `${entry.occurred_at}-${entry.action}-${entry.actor_id ?? ''}-${index}`
}

export default function AuditPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const filters = useMemo(() => parseFilters(searchParams), [searchParams])
  const offset = offsetFrom(searchParams)

  const [draft, setDraft] = useState<Filters>(filters)
  // Follows the URL, so the Back button — or a link opened into this same view —
  // leaves the form showing the filters the table is actually built from.
  useEffect(() => setDraft(filters), [filters])

  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set<string>())

  const { data, loading, error, refresh } = useLoad<AuditLoad>(
    async (signal) => {
      const params = new URLSearchParams({ limit: String(pageSize), offset: String(offset), since: buildSince(filters.hours) })
      if (filters.action) params.set('action', filters.action)
      if (filters.actorId) params.set('actor_id', filters.actorId)
      if (filters.outcome) params.set('outcome', filters.outcome)
      try {
        return { disabled: false, response: await request<AuditResponse>(`/admin/audit?${params.toString()}`, { signal }) }
      } catch (loadError) {
        if (loadError instanceof ApiError && loadError.status === 501) return { disabled: true }
        throw loadError
      }
    },
    [filters.action, filters.actorId, filters.outcome, filters.hours, offset],
    'The audit trail could not be loaded.',
    { refetchInterval: REFRESH_MS },
  )

  const auditDisabled = data?.disabled === true
  const result = data && !data.disabled ? data.response : null
  // Memoized because `result?.data ?? []` would otherwise be a fresh array
  // identity on every render, defeating the memo below that depends on it.
  const entries = useMemo(() => result?.data ?? [], [result])
  const total = result?.summary.total_entries ?? 0

  /*
   * Suggestions for the action box, taken from the rows on screen rather than a
   * list kept here. The server matches `action` exactly, so a guessed verb
   * returns an empty page that reads exactly like a quiet gateway; a list
   * hard-coded in the dashboard would answer that by going stale the first time
   * the gateway records a verb this file has not heard of.
   */
  const actionOptions = useMemo(
    () => [...new Set(entries.map((entry) => entry.action).filter(Boolean))].sort(),
    [entries],
  )

  const applyFilters = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    // Back to the first page: page 3 of the previous filter is usually past the
    // end of the new one, which shows an empty page for a filter that has rows.
    setSearchParams(toParams({ ...draft, action: draft.action.trim(), actorId: draft.actorId.trim() }, 0))
  }

  const toggleDetail = useCallback((key: string) => {
    setExpanded((current) => {
      const next = new Set(current)
      if (!next.delete(key)) next.add(key)
      return next
    })
  }, [])

  const columns = useMemo<readonly DataTableColumn<AuditEntry>[]>(() => [
    {
      id: 'detail',
      header: <span className="sr-only">Detail</span>,
      cellClassName: 'max-sm:block max-sm:py-1',
      render: (entry, index) => {
        const key = rowKey(entry, index)
        const isExpanded = expanded.has(key)
        return entry.detail ? (
          <Button
            aria-expanded={isExpanded}
            aria-label={`${isExpanded ? 'Hide' : 'Show'} detail for ${entry.action}`}
            size="icon"
            type="button"
            variant="ghost"
            onClick={() => toggleDetail(key)}
          >
            {isExpanded ? <ChevronDown aria-hidden="true" size={18} /> : <ChevronRight aria-hidden="true" size={18} />}
          </Button>
        ) : null
      },
    },
    {
      id: 'when',
      header: 'When',
      mobileLabel: 'When',
      volatile: true,
      cellClassName: 'whitespace-nowrap text-muted-foreground tabular-nums',
      render: (entry) => (
        <>
          <span className="text-foreground">{formatTimestamp(entry.occurred_at)}</span>
          <span className="text-xs max-sm:ml-2 sm:block">{timeAgo(entry.occurred_at)}</span>
        </>
      ),
    },
    {
      id: 'action',
      header: 'Action',
      mobileLabel: 'Action',
      render: (entry) => <code className="font-mono text-xs">{entry.action}</code>,
    },
    {
      id: 'actor',
      header: 'Actor',
      mobileLabel: 'Actor',
      render: (entry) => (
        <>
          <span className="font-medium text-foreground">{entry.actor || '—'}</span>
          {entry.actor_id ? (
            <span className="block font-mono text-xs text-muted-foreground">{entry.actor_id}</span>
          ) : null}
        </>
      ),
    },
    {
      id: 'target',
      header: 'Target',
      mobileLabel: 'Target',
      render: (entry) => entry.target_id ? (
        <code className="font-mono text-xs text-muted-foreground">{entry.target_id}</code>
      ) : <span className="text-muted-foreground">—</span>,
    },
    {
      id: 'outcome',
      header: 'Outcome',
      mobileLabel: 'Outcome',
      render: (entry) => {
        const outcome = OUTCOME_PRESENTATION[entry.outcome]
        return <StatusPill tone={outcome.tone}>{outcome.label}</StatusPill>
      },
    },
    {
      id: 'source-ip',
      header: 'Source IP',
      mobileLabel: 'Source IP',
      cellClassName: 'text-muted-foreground',
      render: (entry) => entry.source_ip || '—',
    },
    {
      id: 'trace-id',
      header: 'Trace ID',
      mobileLabel: 'Trace ID',
      headerClassName: 'w-full',
      render: (entry) => entry.trace_id ? (
        <span className="flex items-center gap-1">
          <Link
            className="font-mono text-xs text-primary hover:underline"
            title="Find this trace in the request logs"
            to={`/logs?q=${encodeURIComponent(entry.trace_id)}`}
          >
            {entry.trace_id}
          </Link>
          <CopyButton label={`Copy trace ID ${entry.trace_id}`} size="icon-xs" value={entry.trace_id} />
        </span>
      ) : <span className="text-muted-foreground">—</span>,
    },
  ], [expanded, toggleDetail])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Audit Trail"
        description="Every credential change, sign-in, and log purge this gateway recorded. A denied entry is a refusal the gateway enforced, not a fault."
        actions={
          <Button aria-label="Refresh audit trail" size="icon" type="button" variant="outline" onClick={refresh}>
            <RefreshCw aria-hidden="true" className={loading ? 'animate-spin' : ''} size={18} />
          </Button>
        }
      />
      {error ? <Notice tone="error">{error}</Notice> : null}

      {auditDisabled ? (
        <EmptyState
          title="The audit trail is not being recorded"
          description={
            'This gateway is running without an audit store, so administrative actions are not kept. The audit store follows the API key store backend: '
            + 'the in-memory default keeps only the most recent actions and does not survive a restart, so a deployment that needs the full history sets '
            + 'API_KEY_STORE_BACKEND to sqlite or postgres along with API_KEY_STORE_DSN.'
          }
        />
      ) : (
        <>
          <form className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-2.5" onSubmit={applyFilters}>
            <Label className="min-w-[170px] flex-1">
              <span className="sr-only">Action</span>
              <Input
                list="audit-action-options"
                placeholder="Action, e.g. key.create"
                value={draft.action}
                onChange={(event) => setDraft((current) => ({ ...current, action: event.target.value }))}
              />
            </Label>
            <datalist id="audit-action-options">
              {actionOptions.map((action) => (
                <option key={action} value={action} />
              ))}
            </datalist>
            {/*
             * The credential id, not the display name: `actor` is frozen at
             * write time and is neither stable nor unique, so filtering on it
             * would mix two operators who once shared a key name and miss the
             * rows one of them wrote under a name since changed.
             */}
            <Label className="min-w-[170px] flex-1">
              <span className="sr-only">Actor ID</span>
              <Input
                placeholder="Actor ID"
                value={draft.actorId}
                onChange={(event) => setDraft((current) => ({ ...current, actorId: event.target.value }))}
              />
            </Label>
            <Select value={draft.outcome} onValueChange={(value) => setDraft((current) => ({ ...current, outcome: value ?? current.outcome }))}>
              <SelectTrigger aria-label="Outcome">
                <SelectValue placeholder="All outcomes" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">All outcomes</SelectItem>
                <SelectItem value="ok">OK</SelectItem>
                <SelectItem value="denied">Denied</SelectItem>
                <SelectItem value="error">Error</SelectItem>
              </SelectContent>
            </Select>
            <Select value={draft.hours} onValueChange={(value) => setDraft((current) => ({ ...current, hours: value ?? current.hours }))}>
              <SelectTrigger aria-label="Time range">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TIME_RANGES.map((range) => (
                  <SelectItem key={range.hours} value={range.hours}>{range.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button type="submit" variant="outline">
              <Filter aria-hidden="true" size={16} />
              Apply
            </Button>
          </form>

          {loading || result ? (
            <DataTable
              ariaLabel="Recorded administrative actions"
              columns={columns}
              description={
                <span aria-live="polite">
                  {result ? `${formatNumber(total)} matching entries` : 'Loading entries'}
                </span>
              }
              emptyDescription={
                filters.action || filters.actorId || filters.outcome
                  ? 'Action and actor ID are matched exactly. Check the value, choose a wider time range, or clear the filters.'
                  : 'No administrative action was recorded in this window. Choose a wider time range to look further back.'
              }
              emptyTitle="No audit entries"
              loading={loading && !result}
              loadingLabel="Loading audit trail"
              pagination={
                result && (entries.length > 0 || total > 0 || offset > 0)
                  ? {
                      busy: loading,
                      offset,
                      pageSize,
                      returned: entries.length,
                      total,
                      onOffsetChange: (next) => setSearchParams(toParams(filters, next)),
                    }
                  : undefined
              }
              renderSubRow={(entry, index) => {
                const key = rowKey(entry, index)
                return expanded.has(key) && entry.detail ? (
                  <pre className="overflow-x-auto py-1 font-mono text-xs whitespace-pre-wrap text-muted-foreground">
                    {formatDetail(entry.detail)}
                  </pre>
                ) : null
              }}
              rowKey={rowKey}
              rows={entries}
              title="Recorded actions"
            />
          ) : null}
        </>
      )}
    </div>
  )
}
