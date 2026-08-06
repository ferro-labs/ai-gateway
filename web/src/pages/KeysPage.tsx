import { KeyRound, Pencil, Plus, RefreshCw, RotateCw, ShieldOff, Trash2, type LucideIcon } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { useAuth } from '../auth/AuthProvider'
import { SessionsPanel } from '../components/SessionsPanel'
import { Button, ConfirmDialog, CopyButton, EmptyState, LoadingState, Modal, Notice, PageHeader, Pagination, StatusPill } from '../components/ui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { request } from '../lib/api'
import { errorMessage, formatDateTime, formatNumber, maskKey, timeAgo } from '../lib/format'
import type { APIKey, Scope } from '../types'

type KeyAction = { kind: 'rotate' | 'revoke' | 'delete'; key: APIKey } | null

interface CreateKeyInput {
  name: string
  scopes: Scope[]
  expiresInDays: string
}

/**
 * GET /admin/keys/usage — `keyUsage` in internal/admin/handlers/keys.go.
 *
 * Not in types.ts because nothing else calls this endpoint. `summary` describes
 * the *filtered* set rather than the store: with `active=false` applied,
 * `total_keys` is how many revoked keys exist. Paging arithmetic and the caption
 * both read it, so they stay consistent with the rows on screen.
 */
interface KeyUsageResponse {
  data: APIKey[]
  summary: {
    total_keys: number
    active_keys: number
    total_usage: number
    returned_keys: number
  }
}

/** `sort=` as the handler spells it — anything else is a 400. */
type KeySort = 'usage' | 'last_used'
/** `active=`, where the empty string means the filter is not sent at all. */
type ActiveFilter = '' | 'true' | 'false'

// The three things an edit can do to expiry: leave it exactly as stored,
// extend it N days from the moment Save is pressed, or clear it. Distinct
// from the create form's day choices because "leave unchanged" only makes
// sense once a key already exists to have a current value to keep.
type ExpiryChoice = 'keep' | 'never' | '7' | '30' | '90' | '365'

interface EditKeyState {
  key: APIKey
  name: string
  scopes: Scope[]
  expiryChoice: ExpiryChoice
}

const pageSize = 20
const defaultCreateInput: CreateKeyInput = { name: '', scopes: ['admin'], expiresInDays: '30' }

const SCOPE_OPTIONS: ReadonlyArray<{ value: Scope; label: string; hint: string }> = [
  { value: 'admin', label: 'Admin', hint: 'Issue, rotate, revoke, and edit credentials, and change configuration.' },
  { value: 'read_only', label: 'Read only', hint: 'Inspect keys, logs, and configuration without changing anything.' },
]

function toggleScope(scopes: readonly Scope[], scope: Scope, checked: boolean): Scope[] {
  if (!checked) return scopes.filter((value) => value !== scope)
  return scopes.includes(scope) ? [...scopes] : [...scopes, scope]
}

function statusForKey(key: APIKey): { label: string; tone: 'success' | 'warning' | 'error' } {
  if (!key.active) return { label: 'Revoked', tone: 'error' }
  if (key.expires_at && new Date(key.expires_at).getTime() < Date.now()) return { label: 'Expired', tone: 'warning' }
  return { label: 'Active', tone: 'success' }
}

// Translates one `ExpiryChoice` into the fields `PUT /admin/keys/{id}` reads.
// "keep" sends neither field, which is how the handler leaves expiry
// untouched; everything else is an explicit instruction.
function expiryUpdateFields(choice: ExpiryChoice): { expires_at?: string; clear_expiration?: boolean } {
  if (choice === 'keep') return {}
  if (choice === 'never') return { clear_expiration: true }
  return { expires_at: new Date(Date.now() + Number(choice) * 86400000).toISOString() }
}

/**
 * One icon-only row control.
 *
 * `label` names the key it acts on so a screen reader hears "Revoke Ops laptop"
 * rather than four identical "Revoke"s; `title` explains the control, including
 * why it is disabled, since a disabled button cannot say that any other way.
 */
function IconAction({
  icon: Icon,
  label,
  title,
  dangerous = false,
  disabled = false,
  onClick,
}: {
  icon: LucideIcon
  label: string
  title: string
  dangerous?: boolean
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <Button
      aria-label={label}
      className={dangerous ? 'text-danger hover:bg-danger-soft hover:text-danger' : 'text-muted-foreground hover:text-foreground'}
      disabled={disabled}
      size="icon-sm"
      title={title}
      type="button"
      variant="ghost"
      onClick={onClick}
    >
      <Icon aria-hidden="true" size={16} />
    </Button>
  )
}

/**
 * Checkboxes rather than a Select: the wire field is a list, and the two scopes
 * are not alternatives — a key may legitimately carry both. A single-choice
 * control made the second scope unreachable from here at all.
 */
function ScopeField({
  idPrefix,
  scopes,
  onChange,
}: {
  idPrefix: string
  scopes: readonly Scope[]
  onChange: (scopes: Scope[]) => void
}) {
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1.5 text-sm leading-none font-medium text-foreground">Scopes</legend>
      {SCOPE_OPTIONS.map((option) => (
        <Label className="items-start gap-2 font-normal" htmlFor={`${idPrefix}-scope-${option.value}`} key={option.value}>
          <input
            checked={scopes.includes(option.value)}
            className="mt-0.5 size-4 shrink-0"
            id={`${idPrefix}-scope-${option.value}`}
            type="checkbox"
            onChange={(event) => onChange(toggleScope(scopes, option.value, event.target.checked))}
          />
          <span className="min-w-0">
            <span className="block font-medium text-foreground">{option.label}</span>
            <span className="block text-xs text-muted-foreground">{option.hint}</span>
          </span>
        </Label>
      ))}
    </fieldset>
  )
}

/*
 * Below `sm` each row becomes its own labelled card instead of a table row: the
 * table/head/body switch to block display and every cell prints its column name
 * from `data-label` through a generated `::before` (Tailwind's `content-[attr()]`
 * arbitrary value). This is the same technique the old hand-written stylesheet
 * used for dense tables on a phone, ported onto utilities.
 */
const mobileRow = 'max-sm:mb-3 max-sm:block max-sm:rounded-lg max-sm:border max-sm:border-border max-sm:p-2.5 max-sm:last:mb-0'
const mobileCell =
  'max-sm:flex max-sm:items-baseline max-sm:justify-between max-sm:gap-3 max-sm:border-b max-sm:border-border/60 max-sm:py-1 max-sm:last:border-0 ' +
  'max-sm:before:shrink-0 max-sm:before:text-[11px] max-sm:before:font-semibold max-sm:before:tracking-wide max-sm:before:text-muted-foreground max-sm:before:uppercase max-sm:before:content-[attr(data-label)]'

export default function KeysPage() {
  const { isAdmin } = useAuth()
  const [usage, setUsage] = useState<KeyUsageResponse | null>(null)
  const [sort, setSort] = useState<KeySort>('usage')
  const [activeFilter, setActiveFilter] = useState<ActiveFilter>('')
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [createInput, setCreateInput] = useState<CreateKeyInput>(defaultCreateInput)
  const [createError, setCreateError] = useState('')
  const [action, setAction] = useState<KeyAction>(null)
  const [actionError, setActionError] = useState('')
  const [busy, setBusy] = useState(false)
  const [revealedKey, setRevealedKey] = useState<{ title: string; value: string } | null>(null)
  const [editState, setEditState] = useState<EditKeyState | null>(null)
  const [editBusy, setEditBusy] = useState(false)
  const [editError, setEditError] = useState('')
  const [reload, setReload] = useState(0)
  const refresh = useCallback(() => setReload((value) => value + 1), [])

  /*
   * `/admin/keys/usage` rather than `/admin/keys`: the plain list is
   * unpaginated and unordered — the in-memory store iterates a map, so two
   * refreshes can return the same keys in a different order and a deployment
   * with a few hundred keys renders every one of them. This endpoint sorts
   * server-side and returns a page.
   */
  useEffect(() => {
    const controller = new AbortController()
    const params = new URLSearchParams({ limit: String(pageSize), offset: String(offset), sort })
    if (activeFilter) params.set('active', activeFilter)
    setLoading(true)
    setError('')
    request<KeyUsageResponse>(`/admin/keys/usage?${params.toString()}`, { signal: controller.signal })
      .then(setUsage)
      .catch((loadError: unknown) => {
        if (loadError instanceof DOMException && loadError.name === 'AbortError') return
        setError(errorMessage(loadError, 'API keys could not be loaded.'))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [activeFilter, offset, reload, sort])

  // Memoized because `usage?.data ?? []` is a fresh array identity on every
  // render, which would defeat the lookup built from it below.
  const keys = useMemo(() => usage?.data ?? [], [usage])
  const total = usage?.summary.total_keys ?? 0

  // Lets the sessions panel name the credential each session was minted from.
  // Covers this page of keys only — see SessionsPanel for what it does with a
  // credential it cannot name.
  const keyNames = useMemo(() => new Map(keys.map((key) => [key.id, key.name])), [keys])

  const changeQuery = (apply: () => void) => {
    // Page 3 of "most used" is not page 3 of "recently used", and a narrowed
    // filter usually has fewer pages than the current offset assumes.
    setOffset(0)
    apply()
  }

  // Opening a new confirmation retires the previous one's failure, so a stale
  // "revoke failed" can't reappear on a later delete of a different key.
  const openAction = (next: KeyAction) => {
    setAction(next)
    setActionError('')
  }

  const openCreate = () => {
    setCreateInput(defaultCreateInput)
    setCreateError('')
    setCreateOpen(true)
  }

  const createKey = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    // Both refusals below are reported inside the dialog. They used to write to
    // the page-level notice, which the create dialog's own backdrop covers — so
    // an empty name and a rejected request were both indistinguishable from the
    // Create button doing nothing at all.
    if (!createInput.name.trim()) {
      setCreateError('A key name is required.')
      return
    }
    if (createInput.scopes.length === 0) {
      setCreateError('Select at least one scope. A key with none can authenticate but reach nothing.')
      return
    }
    setBusy(true)
    setCreateError('')
    setNotice('')
    const days = Number(createInput.expiresInDays)
    const body: { name: string; scopes: Scope[]; expires_at?: string } = {
      name: createInput.name.trim(),
      scopes: createInput.scopes,
    }
    if (Number.isFinite(days) && days > 0) body.expires_at = new Date(Date.now() + days * 86400000).toISOString()
    try {
      const created = await request<APIKey>('/admin/keys', { method: 'POST', body: JSON.stringify(body) })
      setCreateOpen(false)
      setCreateInput(defaultCreateInput)
      setRevealedKey({ title: 'API key created', value: created.key })
      refresh()
    } catch (submitError) {
      setCreateError(errorMessage(submitError, 'The API key could not be created.'))
    } finally {
      setBusy(false)
    }
  }

  const performAction = async () => {
    if (!action) return
    setBusy(true)
    setActionError('')
    setNotice('')
    try {
      if (action.kind === 'rotate') {
        const rotated = await request<APIKey>(`/admin/keys/${encodeURIComponent(action.key.id)}/rotate`, { method: 'POST' })
        setRevealedKey({ title: 'API key rotated', value: rotated.key })
      } else if (action.kind === 'revoke') {
        await request<APIKey>(`/admin/keys/${encodeURIComponent(action.key.id)}/revoke`, { method: 'POST' })
        setNotice(`${action.key.name} was revoked.`)
      } else {
        await request<void>(`/admin/keys/${encodeURIComponent(action.key.id)}`, { method: 'DELETE' })
        setNotice(`${action.key.name} was deleted.`)
      }
      setAction(null)
      refresh()
    } catch (mutationError) {
      // Keep the dialog open and show the failure there: a 409 refusing to revoke
      // the credential this session is authenticated with, or refusing to remove
      // the last admin key, used to land in a page-level Notice sitting behind the
      // dialog's own backdrop, unreadable until the operator dismissed the dialog.
      setActionError(errorMessage(mutationError, 'The key operation failed.'))
    } finally {
      setBusy(false)
    }
  }

  const openEdit = (key: APIKey) => {
    setEditState({ key, name: key.name, scopes: [...key.scopes], expiryChoice: 'keep' })
    setEditError('')
  }

  const submitEdit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!editState) return
    const name = editState.name.trim()
    if (!name) {
      setEditError('A key name is required.')
      return
    }
    // The handler reads an empty list as "leave scopes alone", so submitting
    // none would silently save the name and quietly keep the old scopes —
    // exactly the outcome an operator clearing both boxes is not asking for.
    if (editState.scopes.length === 0) {
      setEditError('Select at least one scope. To take a key out of service, revoke it instead.')
      return
    }
    setEditBusy(true)
    setEditError('')
    // Scopes are sent: the server owns the two rules that make changing them
    // dangerous — it refuses to de-scope the last admin key and refuses to
    // de-scope the credential this session authenticated with — and returns a
    // 409 the in-modal error below renders. Withholding the field here did not
    // make either rule safer; it only meant a read-only key could never be
    // promoted, and demoting one meant deleting and reissuing it.
    const body: { name: string; scopes: Scope[]; expires_at?: string; clear_expiration?: boolean } = {
      name,
      scopes: editState.scopes,
      ...expiryUpdateFields(editState.expiryChoice),
    }
    try {
      await request<APIKey>(`/admin/keys/${encodeURIComponent(editState.key.id)}`, { method: 'PUT', body: JSON.stringify(body) })
      setNotice(`${name} was updated.`)
      setEditState(null)
      refresh()
    } catch (updateError) {
      // Rendered inside the modal itself, not the page-level Notice: the two
      // 409s this endpoint can return — an expiry landing in the past on the
      // credential this session authenticated with, or de-scoping the last
      // admin key — would otherwise sit behind the dialog's backdrop and look
      // exactly like Save doing nothing.
      setEditError(errorMessage(updateError, 'The key could not be updated.'))
    } finally {
      setEditBusy(false)
    }
  }

  const actionCopy = action ? {
    rotate: { title: `Rotate ${action.key.name}?`, description: 'The current secret stops working immediately. Update every client with the new key.', confirm: 'Rotate key', dangerous: false },
    revoke: { title: `Revoke ${action.key.name}?`, description: 'This key stops authenticating immediately. This action cannot be undone.', confirm: 'Revoke key', dangerous: true },
    delete: { title: `Delete ${action.key.name}?`, description: 'The key record and its visible usage metadata will be removed.', confirm: 'Delete key', dangerous: true },
  }[action.kind] : null

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="API Keys"
        description="Issue, rotate, and revoke credentials without exposing stored secrets."
        actions={
          <>
            <Button aria-label="Refresh API keys" size="icon" type="button" variant="outline" onClick={refresh}>
              <RefreshCw aria-hidden="true" className={loading ? 'animate-spin' : ''} size={18} />
            </Button>
            {isAdmin ? (
              <Button type="button" onClick={openCreate}>
                <Plus aria-hidden="true" size={17} />
                Create key
              </Button>
            ) : null}
          </>
        }
      />
      {!isAdmin ? <Notice>Read-only sessions can inspect keys but cannot create, rotate, revoke, or delete them.</Notice> : null}
      {notice ? <Notice tone="success">{notice}</Notice> : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
      {loading && !usage ? <LoadingState label="Loading API keys" /> : null}
      {/*
        * Rendered whenever the server has answered, not only when this page has
        * rows: a filter that matches nothing must not take the filter controls
        * and the pagination away with it, which is the only route back to a page
        * that does have rows. LogsPage documents the same failure.
        */}
      {usage ? (
        <section aria-labelledby="keys-table-heading" className="rounded-lg border border-border bg-card">
          <div className="flex flex-wrap items-end justify-between gap-3 border-b border-border px-4 py-2.5">
            <div className="min-w-0">
              <h2 className="text-sm font-semibold text-foreground" id="keys-table-heading">Gateway keys</h2>
              {/* Announced, because sorting and filtering rewrite the table
                  underneath a reader with no other signal that anything moved.
                  The counts describe the filtered set, which is why they are
                  read from the response rather than counted from the rows. */}
              <p aria-live="polite" className="text-sm text-muted-foreground">
                {formatNumber(usage.summary.total_keys)} total · {formatNumber(usage.summary.active_keys)} active · {formatNumber(usage.summary.total_usage)} requests served
              </p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Select value={sort} onValueChange={(value) => changeQuery(() => setSort(value ?? sort))}>
                <SelectTrigger aria-label="Sort keys">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="usage">Most used first</SelectItem>
                  <SelectItem value="last_used">Recently used first</SelectItem>
                </SelectContent>
              </Select>
              <Select value={activeFilter} onValueChange={(value) => changeQuery(() => setActiveFilter(value ?? activeFilter))}>
                <SelectTrigger aria-label="Filter by state">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="">All keys</SelectItem>
                  <SelectItem value="true">Active only</SelectItem>
                  <SelectItem value="false">Revoked only</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          {keys.length === 0 ? (
            <div className="p-4">
              {activeFilter ? (
                <EmptyState
                  title="No keys match this filter"
                  description="Choose All keys to see every credential this gateway holds."
                />
              ) : (
                <EmptyState
                  title="No API keys"
                  description="Create a scoped key for an application or a read-only operator."
                  action={isAdmin ? (
                    <Button type="button" onClick={openCreate}>
                      <Plus aria-hidden="true" size={17} />
                      Create key
                    </Button>
                  ) : undefined}
                />
              )}
            </div>
          ) : (
            <Table className="max-sm:block">
              <TableHeader className="max-sm:hidden">
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Key</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Scope</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead>Usage</TableHead>
                  <TableHead>Last used</TableHead>
                  <TableHead><span className="sr-only">Actions</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="max-sm:block">
                {keys.map((key) => {
                  const status = statusForKey(key)
                  const expired = status.label === 'Expired'
                  return (
                    <TableRow className={mobileRow} key={key.id}>
                      <TableCell className={mobileCell} data-label="Name">
                        <div className="font-medium text-foreground">{key.name || 'Unnamed key'}</div>
                        <div className="text-xs text-muted-foreground">Created {formatDateTime(key.created_at)}</div>
                        {/* A rotated key is a different secret under the same
                            name, so when it last changed is what tells a client
                            failing to authenticate from one that never could. */}
                        {key.rotated_at ? (
                          <div className="text-xs text-muted-foreground">Rotated {timeAgo(key.rotated_at)}</div>
                        ) : null}
                      </TableCell>
                      <TableCell className={mobileCell} data-label="Key">
                        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{maskKey(key.key)}</code>
                      </TableCell>
                      <TableCell className={mobileCell} data-label="Status">
                        <div className="flex flex-col items-start gap-0.5">
                          <StatusPill tone={status.tone}>{status.label}</StatusPill>
                          {/* Without the date the pill answers "is this key
                              dead?" but not "since when?", which is the question
                              asked when a client suddenly starts failing. */}
                          {key.revoked_at ? (
                            <span className="text-xs text-muted-foreground">{formatDateTime(key.revoked_at)}</span>
                          ) : null}
                        </div>
                      </TableCell>
                      <TableCell className={mobileCell} data-label="Scope">{key.scopes.join(', ') || '—'}</TableCell>
                      <TableCell className={mobileCell} data-label="Expires">
                        {key.expires_at ? (
                          <span className={cn(expired && 'font-medium text-danger')}>{formatDateTime(key.expires_at)}</span>
                        ) : (
                          <span className="text-muted-foreground">Never</span>
                        )}
                      </TableCell>
                      <TableCell className={mobileCell} data-label="Usage">{formatNumber(key.usage_count)}</TableCell>
                      <TableCell className={mobileCell} data-label="Last used">{timeAgo(key.last_used_at)}</TableCell>
                      <TableCell className={cn(mobileCell, 'max-sm:flex-row max-sm:justify-end max-sm:border-0 max-sm:py-0')}>
                        {isAdmin ? (
                          <div className="flex items-center gap-1">
                            <IconAction icon={Pencil} label={`Edit ${key.name}`} title="Edit key" onClick={() => openEdit(key)} />
                            <IconAction
                              disabled={!key.active || expired}
                              icon={RotateCw}
                              label={`Rotate ${key.name}`}
                              title={
                                !key.active
                                  ? 'Revoked keys cannot be rotated.'
                                  : expired
                                    ? 'Expired keys cannot be rotated — create a new key instead.'
                                    : 'Rotate key'
                              }
                              onClick={() => openAction({ kind: 'rotate', key })}
                            />
                            <IconAction
                              dangerous
                              disabled={!key.active}
                              icon={ShieldOff}
                              label={`Revoke ${key.name}`}
                              title="Revoke key"
                              onClick={() => openAction({ kind: 'revoke', key })}
                            />
                            <IconAction
                              dangerous
                              icon={Trash2}
                              label={`Delete ${key.name}`}
                              title="Delete key"
                              onClick={() => openAction({ kind: 'delete', key })}
                            />
                          </div>
                        ) : null}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
          {total > 0 ? (
            <Pagination
              busy={loading}
              offset={offset}
              pageSize={pageSize}
              returned={keys.length}
              total={total}
              onOffsetChange={setOffset}
            />
          ) : null}
        </section>
      ) : null}

      <SessionsPanel keyNames={keyNames} reloadSignal={reload} onNotice={setNotice} onSignOut={refresh} />

      <Modal
        description="The full secret is shown once after creation."
        open={createOpen}
        title="Create API key"
        onClose={() => !busy && setCreateOpen(false)}
        footer={
          <>
            <Button disabled={busy} type="button" variant="outline" onClick={() => setCreateOpen(false)}>Cancel</Button>
            <Button disabled={busy} form="create-key-form" type="submit">{busy ? 'Creating…' : 'Create key'}</Button>
          </>
        }
      >
        <form className="flex flex-col gap-4" id="create-key-form" onSubmit={(event) => void createKey(event)}>
          {createError ? (
            <p className="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger" role="alert">
              {createError}
            </p>
          ) : null}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="create-key-name">Name</Label>
            <Input
              autoFocus
              id="create-key-name"
              maxLength={80}
              placeholder="Production application"
              value={createInput.name}
              onChange={(event) => setCreateInput((value) => ({ ...value, name: event.target.value }))}
            />
          </div>
          <ScopeField
            idPrefix="create-key"
            scopes={createInput.scopes}
            onChange={(scopes) => setCreateInput((current) => ({ ...current, scopes }))}
          />
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="create-key-expiry">Expires</Label>
            <Select
              value={createInput.expiresInDays}
              onValueChange={(value) => setCreateInput((current) => ({ ...current, expiresInDays: value ?? current.expiresInDays }))}
            >
              <SelectTrigger aria-label="Expires" className="w-full" id="create-key-expiry">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="7">7 days</SelectItem>
                <SelectItem value="30">30 days</SelectItem>
                <SelectItem value="90">90 days</SelectItem>
                <SelectItem value="365">1 year</SelectItem>
                <SelectItem value="">Never</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </form>
      </Modal>

      <Modal
        description="Change the name or the scopes, and extend or clear the expiry."
        open={editState != null}
        title="Edit API key"
        onClose={() => !editBusy && setEditState(null)}
        footer={
          <>
            <Button disabled={editBusy} type="button" variant="outline" onClick={() => setEditState(null)}>Cancel</Button>
            <Button disabled={editBusy} form="edit-key-form" type="submit">{editBusy ? 'Saving…' : 'Save changes'}</Button>
          </>
        }
      >
        {editState ? (
          <form className="flex flex-col gap-4" id="edit-key-form" onSubmit={(event) => void submitEdit(event)}>
            {editError ? (
              <p className="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger" role="alert">
                {editError}
              </p>
            ) : null}
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="edit-key-name">Name</Label>
              <Input
                autoFocus
                id="edit-key-name"
                maxLength={80}
                value={editState.name}
                onChange={(event) => {
                  const nextName = event.target.value
                  setEditState((current) => (current ? { ...current, name: nextName } : current))
                }}
              />
            </div>
            <ScopeField
              idPrefix="edit-key"
              scopes={editState.scopes}
              onChange={(scopes) => setEditState((current) => (current ? { ...current, scopes } : current))}
            />
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="edit-key-expiry">Expires</Label>
              <Select
                value={editState.expiryChoice}
                onValueChange={(value) => {
                  setEditState((current) => (current ? { ...current, expiryChoice: value ?? current.expiryChoice } : current))
                }}
              >
                <SelectTrigger aria-label="Expires" className="w-full" id="edit-key-expiry">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="keep">
                    {editState.key.expires_at ? `Keep current expiry (${formatDateTime(editState.key.expires_at)})` : 'Keep — never expires'}
                  </SelectItem>
                  <SelectItem value="7">Extend 7 days from now</SelectItem>
                  <SelectItem value="30">Extend 30 days from now</SelectItem>
                  <SelectItem value="90">Extend 90 days from now</SelectItem>
                  <SelectItem value="365">Extend 1 year from now</SelectItem>
                  <SelectItem value="never">Never expires</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </form>
        ) : null}
      </Modal>

      <Modal
        description="Copy this key now. The gateway stores only its hash and cannot show it again."
        dismissible={false}
        open={revealedKey != null}
        title={revealedKey?.title ?? 'API key'}
        onClose={() => setRevealedKey(null)}
        footer={<Button type="button" onClick={() => setRevealedKey(null)}>Done</Button>}
      >
        <div className="flex items-center gap-3 rounded-lg border border-border bg-muted/40 p-3">
          <KeyRound aria-hidden="true" className="shrink-0 text-muted-foreground" size={20} />
          <code className="min-w-0 flex-1 font-mono text-sm break-all">{revealedKey?.value}</code>
          {/* CopyButton reports success and refusal in place. The previous
              hand-rolled call wrote a denied clipboard to the page-level notice,
              which this dialog's own backdrop covers — so losing the one copy of
              a new key looked identical to copying it. */}
          <CopyButton label="Copy API key" value={revealedKey?.value ?? ''} />
        </div>
      </Modal>

      <ConfirmDialog
        busy={busy}
        confirmLabel={actionCopy?.confirm ?? 'Continue'}
        dangerous={actionCopy?.dangerous}
        description={actionCopy?.description ?? ''}
        error={actionError}
        open={action != null}
        title={actionCopy?.title ?? ''}
        onCancel={() => { if (!busy) { setAction(null); setActionError('') } }}
        onConfirm={() => void performAction()}
      />
    </div>
  )
}
