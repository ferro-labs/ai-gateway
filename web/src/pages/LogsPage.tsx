import { Filter, RefreshCw, Search, Trash2 } from 'lucide-react'
import { useAuth } from '../auth/AuthProvider'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { DataTable, type DataTableColumn } from '../components/DataTable'
import { Button, CopyButton, EmptyState, Modal, Notice, PageHeader, StatusPill } from '../components/ui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ApiError, request } from '../lib/api'
import { buildSince, errorMessage, formatDateTime, formatNumber, formatTimestamp } from '../lib/format'
import { DEFAULT_HOURS, hoursFrom, offsetFrom, TIME_RANGES } from '../lib/timeRange'
import { logFailed } from '../lib/logs'
import { useLoad } from '../lib/useLoad'
import type { APIKey, LogsResponse, RequestLog } from '../types'

// The stage filter's options. One list feeds both the open menu and the
// trigger's label so the two cannot describe the same value differently.
const STAGE_OPTIONS = [
  { value: '', label: 'Requests' },
  { value: 'all', label: 'All stages' },
  { value: 'before_request', label: 'Before request' },
  { value: 'after_request', label: 'After request' },
  { value: 'on_error', label: 'On error' },
] as const

interface Filters {
  query: string
  provider: string
  model: string
  stage: string
  apiKeyId: string
  hours: number
}

const pageSize = 50
/**
 * How GET /admin/logs is asked for the rows that name no credential.
 *
 * A reserved value rather than a blank one: blank is how every filter on that
 * endpoint spells "no filter". It cannot collide with a real id — stored keys
 * carry a UUID, and the master and bootstrap credentials carry prefixed
 * synthetic ids.
 */
const API_KEY_NONE = 'none'
/**
 * The stage filter's accepted values.
 *
 * `all` is not a stage: it is how GET /admin/logs is asked for every logged
 * row. The default — an absent `stage` — is one row per request, because the
 * logger writes a row per plugin stage and the unfiltered listing paired every
 * request with a provider-less, zero-token copy of itself.
 */
const STAGES: readonly string[] = STAGE_OPTIONS.map((option) => option.value).filter(Boolean)
const PURGE_DEFAULT_DAYS = 30
const DAY_MS = 86400000
/** Long enough that a watched page stays current, slow enough to stay cheap. */
const REFRESH_MS = 30000

/**
 * The filter set, read from the URL rather than component state.
 *
 * A link to a filtered view is what an operator pastes into an incident
 * channel, and a reload during an incident must not drop back to "last 24
 * hours, everything". Values that fail validation fall back to the default
 * instead of being forwarded: a hand-edited `stage=whatever` returns nothing
 * and looks exactly like a quiet gateway, and an arbitrary `hours` puts a value
 * in the Select that has no option to match it.
 */
function parseFilters(params: URLSearchParams): Filters {
  const stage = params.get('stage') ?? ''
  return {
    query: params.get('q') ?? '',
    provider: params.get('provider') ?? '',
    model: params.get('model') ?? '',
    stage: STAGES.includes(stage) ? stage : '',
    // Not validated against the keys this gateway still holds, unlike `stage`
    // above. A deleted key's rows outlive the key, and "which key spent this"
    // is asked about a key that has since been removed more often than about a
    // current one — so an id nothing on this page can name is still forwarded.
    apiKeyId: params.get('api_key_id') ?? '',
    hours: hoursFrom(params),
  }
}

// Defaults are omitted so the common URL stays short and the interesting part
// of a shared link is the part that was actually changed.
function toParams(filters: Filters, offset: number): URLSearchParams {
  const params = new URLSearchParams()
  if (filters.query) params.set('q', filters.query)
  if (filters.provider) params.set('provider', filters.provider)
  if (filters.model) params.set('model', filters.model)
  if (filters.stage) params.set('stage', filters.stage)
  if (filters.apiKeyId) params.set('api_key_id', filters.apiKeyId)
  if (filters.hours !== DEFAULT_HOURS) params.set('hours', String(filters.hours))
  if (offset > 0) params.set('offset', String(offset))
  return params
}

/**
 * A `Date` as the `yyyy-mm-dd` an `<input type="date">` reads and writes.
 *
 * Built from the local fields rather than sliced off `toISOString()`, which is
 * UTC: for anyone far enough east or west, that names the day before or after
 * the one on the operator's own calendar — and the value picked here decides
 * what gets permanently deleted.
 */
function dateInputValue(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

/** Local midnight of a `yyyy-mm-dd` value, or null when it is not a date. */
function cutoffFor(value: string): Date | null {
  if (!value) return null
  // No trailing Z: "before the 3rd" means the operator's 3rd, not UTC's.
  const cutoff = new Date(`${value}T00:00:00`)
  return Number.isNaN(cutoff.getTime()) ? null : cutoff
}

/**
 * A 501 is the request-log store being unconfigured — a deployment choice, not
 * a fault — so it is carried as a value rather than thrown. Left as a rejection
 * it would reach the shared loader's error path and render as a red failure
 * banner telling an operator to fix something that is not broken.
 */
type LogsLoad = { disabled: true } | { disabled: false; response: LogsResponse }

function statusFor(log: RequestLog) {
  const failed = logFailed(log)
  return <StatusPill tone={failed ? 'error' : 'success'}>{failed ? 'Error' : 'OK'}</StatusPill>
}

/**
 * What has become of a key since it served the request, or '' while it is
 * current.
 *
 * Read from the two timestamps rather than from `active`, which is false for
 * both and so cannot say which. The distinction is the point: a revoked key was
 * withdrawn by an operator, an expired one simply reached its date, and the
 * first is the one worth noticing beside traffic it served.
 */
function keyState(key: APIKey): string {
  if (key.revoked_at) return 'revoked'
  if (key.expires_at && new Date(key.expires_at).getTime() <= Date.now()) return 'expired'
  return ''
}

/**
 * Which credential served the request.
 *
 * The stored value is an opaque id, and a column of those names nobody: it is
 * resolved to the key's name so the row answers the question the id was
 * recorded for. Three outcomes, all of which occur on a real gateway:
 *
 *  - a key the store still holds — its name, with what has since happened to it
 *    when that is anything, and the id on the title so a row can still be tied
 *    to a log line or a span attribute;
 *  - an id the store cannot name — a deleted key, or the synthetic ids the
 *    master and bootstrap credentials carry — the recorded id verbatim, because
 *    calling it "deleted" would claim more than is known;
 *  - no id at all — an unauthenticated request, or a row written before the
 *    gateway recorded this. Shown as absent, which is what it is.
 *
 * Only `id` and `name` are ever read. GET /admin/keys serves a masked display
 * form of the secret and it is never rendered.
 */
function apiKeyCell(log: RequestLog, keys: ReadonlyMap<string, APIKey>) {
  const id = log.api_key_id ?? ''
  if (!id) return <span className="text-muted-foreground">—</span>

  const key = keys.get(id)
  if (!key) return <code className="font-mono text-xs text-muted-foreground">{id}</code>

  const state = keyState(key)
  return (
    <span title={id}>
      <span className="text-foreground">{key.name}</span>
      {state ? <span className="block text-xs text-muted-foreground">{state}</span> : null}
    </span>
  )
}

export default function LogsPage() {
  const { isAdmin } = useAuth()
  const [searchParams, setSearchParams] = useSearchParams()
  const filters = useMemo(() => parseFilters(searchParams), [searchParams])
  const offset = offsetFrom(searchParams)

  const [draft, setDraft] = useState<Filters>(filters)
  // Follows the URL, so the Back button — or a link opened into this same view —
  // leaves the form showing the filters the table is actually built from.
  useEffect(() => setDraft(filters), [filters])

  const [notice, setNotice] = useState('')
  const [purgeOpen, setPurgeOpen] = useState(false)
  const [purgeBefore, setPurgeBefore] = useState(() => dateInputValue(new Date(Date.now() - PURGE_DEFAULT_DAYS * DAY_MS)))
  const [purgeScoped, setPurgeScoped] = useState(false)
  const [purgeError, setPurgeError] = useState('')
  const [busy, setBusy] = useState(false)

  const { data, loading, error, refresh } = useLoad<LogsLoad>(
    async (signal) => {
      const params = new URLSearchParams({ limit: String(pageSize), offset: String(offset), since: buildSince(filters.hours) })
      if (filters.provider) params.set('provider', filters.provider)
      if (filters.model) params.set('model', filters.model)
      if (filters.stage) params.set('stage', filters.stage)
      if (filters.apiKeyId) params.set('api_key_id', filters.apiKeyId)
      try {
        return { disabled: false, response: await request<LogsResponse>(`/admin/logs?${params.toString()}`, { signal }) }
      } catch (loadError) {
        if (loadError instanceof ApiError && loadError.status === 501) return { disabled: true }
        throw loadError
      }
    },
    // `query` is deliberately absent: it filters the rows already on screen and
    // never reaches the gateway, so re-fetching on every keystroke would cost a
    // request per character and change nothing.
    [filters.provider, filters.model, filters.stage, filters.apiKeyId, filters.hours, offset],
    'Request logs could not be loaded.',
    { refetchInterval: REFRESH_MS },
  )

  /*
   * The key list, loaded once, purely to turn recorded ids into names.
   *
   * Its own load rather than part of the one above: keys change on an operator's
   * timescale, not a request's, so tying it to the filters and the 30-second
   * poll would re-fetch every key each time the table refreshed.
   *
   * A failure here is deliberately not raised as a banner. The ids are still
   * rendered, so nothing the gateway recorded is hidden — only the courtesy of
   * a name is lost — and a red failure over a working request log would send an
   * operator after the wrong thing.
   */
  const { data: keyList } = useLoad<APIKey[]>(
    (signal) => request<APIKey[]>('/admin/keys', { signal }),
    [],
    'API key names could not be loaded.',
  )
  const keysById = useMemo(
    () => new Map((keyList ?? []).map((key) => [key.id, key] as const)),
    [keyList],
  )

  const loggingDisabled = data?.disabled === true
  const result = data && !data.disabled ? data.response : null

  // The rows the server actually returned for this page — independent of the
  // client-side search below, so pagination and the count caption stay honest
  // about what the server said even while a search narrows what's displayed.
  // Memoized because `result?.data ?? []` would otherwise be a fresh array
  // identity on every render, defeating the useMemo below that depends on it.
  const pageRows = useMemo(() => result?.data ?? [], [result])

  const visibleLogs = useMemo(() => {
    const query = filters.query.trim().toLowerCase()
    if (!query) return pageRows
    return pageRows.filter((log) => [log.trace_id, log.provider, log.model, log.stage, log.error_message].some((value) => value.toLowerCase().includes(query)))
  }, [filters.query, pageRows])

  const columns = useMemo<readonly DataTableColumn<RequestLog>[]>(() => [
    {
      id: 'time',
      header: 'Time',
      mobileLabel: 'Time',
      volatile: true,
      cellClassName: 'text-muted-foreground tabular-nums',
      render: (log) => formatTimestamp(log.created_at),
    },
    {
      id: 'status',
      header: 'Status',
      mobileLabel: 'Status',
      render: statusFor,
    },
    {
      id: 'api-key',
      header: 'API key',
      mobileLabel: 'API key',
      render: (log) => apiKeyCell(log, keysById),
    },
    {
      id: 'provider',
      header: 'Provider',
      mobileLabel: 'Provider',
      render: (log) => log.provider || '—',
    },
    {
      id: 'model',
      header: 'Model',
      mobileLabel: 'Model',
      render: (log) => <code className="font-mono text-xs">{log.model || '—'}</code>,
    },
    {
      id: 'stage',
      header: 'Stage',
      mobileLabel: 'Stage',
      cellClassName: 'text-muted-foreground',
      render: (log) => log.stage || '—',
    },
    {
      id: 'tokens-in',
      header: 'Tokens in',
      mobileLabel: 'Tokens in',
      headerClassName: 'text-right',
      cellClassName: 'tabular-nums sm:text-right',
      render: (log) => formatNumber(log.prompt_tokens),
    },
    {
      id: 'tokens-out',
      header: 'Tokens out',
      mobileLabel: 'Tokens out',
      headerClassName: 'text-right',
      cellClassName: 'tabular-nums sm:text-right',
      render: (log) => formatNumber(log.completion_tokens),
    },
    {
      id: 'trace-id',
      header: 'Trace ID',
      mobileLabel: 'Trace ID',
      render: (log) => log.trace_id ? (
        <span className="flex items-center gap-1">
          <code className="font-mono text-xs text-muted-foreground">{log.trace_id}</code>
          <CopyButton label={`Copy trace ID ${log.trace_id}`} size="icon-xs" value={log.trace_id} />
        </span>
      ) : <span className="text-muted-foreground">—</span>,
    },
    {
      id: 'detail',
      header: 'Detail',
      mobileLabel: 'Detail',
      headerClassName: 'w-full',
      cellClassName: 'max-sm:empty:hidden text-xs break-words whitespace-normal text-danger',
      render: (log) => log.error_message || null,
    },
  ], [keysById])

  /*
   * The options the key filter offers: every key this gateway holds, by name.
   *
   * Sorted here because GET /admin/keys is unordered — the in-memory store
   * iterates a map — so two refreshes would otherwise reshuffle the list under
   * the operator's cursor.
   *
   * A filter already applied to an id the list does not cover keeps its own
   * option, so the open list still shows what the table is narrowed to. That is
   * the ordinary case for a deleted key, whose rows outlive it.
   */
  const keyOptions = useMemo(() => {
    const options = [...keysById.values()]
      .map((key) => ({ id: key.id, label: keyState(key) ? `${key.name} (${keyState(key)})` : key.name }))
      .sort((left, right) => left.label.localeCompare(right.label))
    if (draft.apiKeyId && draft.apiKeyId !== API_KEY_NONE && !keysById.has(draft.apiKeyId)) {
      options.push({ id: draft.apiKeyId, label: draft.apiKeyId })
    }
    return options
  }, [keysById, draft.apiKeyId])

  /*
   * What the key control's trigger reads.
   *
   * Spelled out because the trigger renders the selected *value* unless told
   * otherwise, and this control's values are opaque ids — leaving it would put
   * a uuid in the one place an operator is picking a person. An id with no
   * option to name it falls back to itself rather than to a blank.
   */
  const apiKeyLabel = (value: string) => {
    if (!value) return 'All keys'
    if (value === API_KEY_NONE) return 'No API key'
    return keyOptions.find((option) => option.id === value)?.label ?? value
  }

  // Every Select trigger resolves its own label. The primitive renders the raw
  // value otherwise, so a trigger would read "on_error" and "1" where the open
  // list reads "On error" and "Last hour".
  const stageLabel = (value: string) =>
    STAGE_OPTIONS.find((option) => option.value === value)?.label ?? value
  const rangeLabel = (value: number) =>
    TIME_RANGES.find((range) => range.hours === value)?.label ?? String(value)

  const applyFilters = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setSearchParams(toParams(draft, 0))
  }

  // Only the three the gateway can narrow a purge by. Neither the search box
  // nor the API key filter is among them — the search never reaches the server
  // at all, and DELETE /admin/logs takes no api_key_id — so offering either
  // here would promise a scope the deletion ignores. This list is what the
  // checkbox names, so what it deletes is always exactly what it says.
  const scopedFilters = [
    { key: 'provider', label: 'provider', value: filters.provider },
    { key: 'model', label: 'model', value: filters.model },
    // `all` is the listing's "every stage" sentinel, not a stage. Forwarding it
    // to the delete narrowed the purge to a stage no row carries, so a dialog
    // promising a scoped deletion removed nothing.
    { key: 'stage', label: 'stage', value: filters.stage === 'all' ? '' : filters.stage },
  ].filter((entry) => entry.value !== '')

  const purgeCutoff = cutoffFor(purgeBefore)
  const scopeSummary = purgeScoped && scopedFilters.length > 0
    ? scopedFilters.map((entry) => `${entry.label} ${entry.value}`).join(', ')
    : ''

  const openPurge = () => {
    setPurgeOpen(true)
    setPurgeError('')
  }

  const closePurge = () => {
    if (busy) return
    setPurgeOpen(false)
    setPurgeError('')
  }

  const purgeLogs = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const cutoff = cutoffFor(purgeBefore)
    if (!cutoff) {
      setPurgeError('Choose the date to delete entries before.')
      return
    }
    // A future cutoff deletes the entire log, including the events of the
    // incident being investigated. The input's `max` stops the picker; a typed
    // value reaches here regardless.
    if (cutoff.getTime() > Date.now()) {
      setPurgeError('Choose a date no later than today.')
      return
    }
    setBusy(true)
    setPurgeError('')
    setNotice('')
    const params = new URLSearchParams({ before: cutoff.toISOString() })
    if (purgeScoped) {
      for (const entry of scopedFilters) params.set(entry.key, entry.value)
    }
    try {
      const deleted = await request<{ deleted: number }>(`/admin/logs?${params.toString()}`, { method: 'DELETE' })
      setNotice(
        `${formatNumber(deleted.deleted)} request-log entries before ${formatDateTime(cutoff.toISOString())} were deleted` +
          `${scopeSummary ? ` (${scopeSummary})` : ''}.`,
      )
      setPurgeOpen(false)
      // Returning to the first page already re-runs the query, so asking for a
      // refresh as well would fire the same request twice. Page 1 has no offset
      // to reset, and there the explicit refresh is the only thing that reloads.
      if (offset > 0) setSearchParams(toParams(filters, 0))
      else refresh()
    } catch (deleteRequestError) {
      setPurgeError(errorMessage(deleteRequestError, 'Request logs could not be deleted.'))
    } finally {
      setBusy(false)
    }
  }

  const total = result?.summary.total_entries ?? 0
  /*
   * What a row means depends on the filter, and the two are not the same unit.
   * Unfiltered, the gateway returns the row each request reached an outcome at
   * — one per request. A named stage, and `all`, return logged events instead,
   * of which a request writes one per plugin stage.
   */
  const rowNoun = filters.stage === '' ? 'request' : 'event'
  const hasQuery = filters.query.trim().length > 0

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Request Logs"
        description="One row per request, newest first. Filter by API key, provider, model, stage, and time; choose a stage to see the raw logged events instead."
        actions={
          <>
            <Button aria-label="Refresh request logs" size="icon" type="button" variant="outline" onClick={refresh}>
              <RefreshCw aria-hidden="true" className={loading ? 'animate-spin' : ''} size={18} />
            </Button>
            {isAdmin && !loggingDisabled ? (
              <Button type="button" variant="destructive" onClick={openPurge}>
                <Trash2 aria-hidden="true" size={17} />
                Delete old logs
              </Button>
            ) : null}
          </>
        }
      />
      {notice ? <Notice tone="success">{notice}</Notice> : null}
      {error ? <Notice tone="error">{error}</Notice> : null}

      {loggingDisabled ? (
        <EmptyState
          title="Request logging is disabled"
          description="Set the REQUEST_LOG_STORE_BACKEND environment variable on the gateway to persist and browse request logs."
        />
      ) : (
        <>
          <form className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-2.5" onSubmit={applyFilters}>
            <Label className="relative min-w-[200px] flex-1">
              <span className="sr-only">Search current page</span>
              <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                className="pl-8"
                placeholder="Search trace, provider, model, or error"
                value={draft.query}
                onChange={(event) => setDraft((value) => ({ ...value, query: event.target.value }))}
              />
            </Label>
            <Label className="min-w-[120px]">
              <span className="sr-only">Provider</span>
              <Input placeholder="Provider" value={draft.provider} onChange={(event) => setDraft((value) => ({ ...value, provider: event.target.value }))} />
            </Label>
            <Label className="min-w-[120px]">
              <span className="sr-only">Model</span>
              <Input placeholder="Model" value={draft.model} onChange={(event) => setDraft((value) => ({ ...value, model: event.target.value }))} />
            </Label>
            {/*
              * Keys are chosen by name, never typed: the value the gateway
              * matches on is an opaque id an operator has no way to recall, and
              * a mistyped one returns an empty page that reads exactly like a
              * quiet gateway.
              */}
            <Select value={draft.apiKeyId} onValueChange={(value) => setDraft((current) => ({ ...current, apiKeyId: value ?? current.apiKeyId }))}>
              <SelectTrigger aria-label="API key">
                <SelectValue>{(value: string) => apiKeyLabel(value)}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">All keys</SelectItem>
                <SelectItem value={API_KEY_NONE}>No API key</SelectItem>
                {keyOptions.map((option) => (
                  <SelectItem key={option.id} value={option.id}>{option.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={draft.stage} onValueChange={(value) => setDraft((current) => ({ ...current, stage: value ?? current.stage }))}>
              <SelectTrigger aria-label="Stage">
                <SelectValue>{(value: string) => stageLabel(value)}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                {STAGE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={draft.hours} onValueChange={(value) => setDraft((current) => ({ ...current, hours: value ?? current.hours }))}>
              <SelectTrigger aria-label="Time range">
                <SelectValue>{(value: number) => rangeLabel(value)}</SelectValue>
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
              ariaLabel="Persisted request events"
              columns={columns}
              description={result ? `${formatNumber(total)} matching ${rowNoun}${total === 1 ? '' : 's'}` : 'Loading entries'}
              emptyDescription={
                pageRows.length > 0
                  ? 'Nothing on this page matches the search. Clear it or turn the page to keep looking.'
                  : 'Broaden the filters, choose a wider time range, or send a request through the Playground.'
              }
              emptyTitle={pageRows.length > 0 ? 'No matches on this page' : 'No request logs'}
              loading={loading && !result}
              loadingLabel="Loading request logs"
              pagination={
                result && (pageRows.length > 0 || total > 0 || offset > 0)
                  ? {
                      busy: loading,
                      note: hasQuery ? `${formatNumber(visibleLogs.length)} match the search on this page` : undefined,
                      offset,
                      pageSize,
                      returned: pageRows.length,
                      total,
                      onOffsetChange: (next) => setSearchParams(toParams(filters, next)),
                    }
                  : undefined
              }
              rowKey={(log, index) => `${log.trace_id}-${log.stage}-${log.created_at}-${index}`}
              rows={visibleLogs}
              title="Persisted events"
            />
          ) : null}
        </>
      )}

      <Modal
        description="Deletion is permanent and is recorded in the gateway audit log."
        open={purgeOpen}
        title="Delete old request logs"
        onClose={closePurge}
        footer={
          <>
            <Button disabled={busy} type="button" variant="outline" onClick={closePurge}>Cancel</Button>
            <Button disabled={busy} form="purge-logs-form" type="submit" variant="destructive">
              {busy ? 'Deleting…' : 'Delete logs'}
            </Button>
          </>
        }
      >
        <form className="flex flex-col gap-4" id="purge-logs-form" onSubmit={(event) => void purgeLogs(event)}>
          {purgeError ? (
            <p className="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger" role="alert">
              {purgeError}
            </p>
          ) : null}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="purge-before">Delete entries before</Label>
            <Input
              id="purge-before"
              max={dateInputValue(new Date())}
              required
              type="date"
              value={purgeBefore}
              onChange={(event) => setPurgeBefore(event.target.value)}
            />
          </div>
          {scopedFilters.length > 0 ? (
            <div className="flex items-start gap-2">
              <input
                checked={purgeScoped}
                className="mt-1 size-4"
                id="purge-scoped"
                type="checkbox"
                onChange={(event) => setPurgeScoped(event.target.checked)}
              />
              <Label className="text-sm font-normal" htmlFor="purge-scoped">
                Limit the deletion to the current filters ({scopedFilters.map((entry) => `${entry.label} ${entry.value}`).join(', ')})
              </Label>
            </div>
          ) : null}
          {/*
           * Spelling out the resolved cutoff and the exact scope, rather than
           * leaving the operator to infer them from the controls above: this
           * deletes rows that cannot be recovered, and the two questions asked
           * afterwards are always "how far back did that go" and "did it only
           * hit the provider I was looking at".
           */}
          <p className="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
            {purgeCutoff
              ? `Deletes every request-log entry created before ${formatDateTime(purgeCutoff.toISOString())}, ${scopeSummary ? `limited to ${scopeSummary}` : 'across every provider, model, and stage'}.`
              : 'Choose a date to see what will be deleted.'}
          </p>
        </form>
      </Modal>
    </div>
  )
}
