import { ChevronDown, ChevronRight, Code2, History, Puzzle, RefreshCw, RotateCcw, Route, Save, Undo2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'
import { useAuth } from '../auth/AuthProvider'
import { ConfigDiff } from '../components/ConfigDiff'
import { JsonEditor } from '../components/JsonEditor'
import { PluginPanel } from '../components/PluginPanel'
import { StrategyPanel } from '../components/StrategyPanel'
import { Button, ConfirmDialog, EmptyState, LoadingState, Notice, PageHeader, StatusPill, TabCount } from '../components/ui'
import { ApiError, request } from '../lib/api'
import { changedTopLevelKeys } from '../lib/configDiff'
import { findRedactedFields, redactedFieldsMessage } from '../lib/configRedaction'
import { errorMessage, formatDateTime } from '../lib/format'
import { groupPlugins, readPlugins, usePluginCatalog } from '../lib/plugins'
import { readStrategy } from '../lib/strategies'
import type { ConfigHistoryEntry, ConfigHistoryResponse } from '../types'

type Tab = 'current' | 'strategy' | 'plugins' | 'history'

function parseEditor(editor: string): Record<string, unknown> | null {
  try {
    const parsed: unknown = JSON.parse(editor)
    if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') return null
    return parsed as Record<string, unknown>
  } catch {
    return null
  }
}

const pretty = (value: unknown): string => JSON.stringify(value, null, 2)

// See KeysPage.tsx for why this exists: below `sm` a row becomes a labelled
// card via `data-label` and a generated `::before`, instead of a table row.
const mobileRow = 'max-sm:mb-2 max-sm:block max-sm:rounded-lg max-sm:border max-sm:border-border max-sm:p-2.5 max-sm:last:mb-0'
const mobileCell =
  'max-sm:flex max-sm:items-baseline max-sm:justify-between max-sm:gap-3 max-sm:border-b max-sm:border-border/60 max-sm:py-1 max-sm:last:border-0 ' +
  'max-sm:before:shrink-0 max-sm:before:text-[11px] max-sm:before:font-semibold max-sm:before:tracking-wide max-sm:before:text-muted-foreground max-sm:before:uppercase max-sm:before:content-[attr(data-label)]'

export default function ConfigPage() {
  const { isAdmin } = useAuth()
  const [tab, setTab] = useState<Tab>('current')
  const [config, setConfig] = useState<Record<string, unknown> | null>(null)
  const [editor, setEditor] = useState('')
  const [editing, setEditing] = useState(false)
  const [history, setHistory] = useState<ConfigHistoryEntry[]>([])
  const [expandedVersion, setExpandedVersion] = useState<number | null>(null)
  const [rollback, setRollback] = useState<ConfigHistoryEntry | null>(null)
  const [rollbackError, setRollbackError] = useState('')
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  const [resetOpen, setResetOpen] = useState(false)
  const [resetError, setResetError] = useState('')
  // A 501 from DELETE /admin/config means this deployment's config manager is
  // not a ConfigResetter — a deployment choice, not a fault — so the control is
  // withdrawn and explained rather than reported red. There is no read endpoint
  // that answers the same question, and the only other way to ask is to perform
  // the reset, so this is learned from the first attempt rather than on load.
  const [resetUnsupported, setResetUnsupported] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [reload, setReload] = useState(0)
  const refresh = useCallback(() => setReload((value) => value + 1), [])

  // Read inside the load effect without making `editing` a dependency — that
  // would refetch on every Edit/Cancel click. The effect only needs the latest
  // value at the moment a load resolves.
  const editingRef = useRef(editing)
  editingRef.current = editing

  const dirty = editing && config != null && editor !== pretty(config)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    Promise.all([
      request<Record<string, unknown>>('/admin/config', { signal: controller.signal }),
      request<ConfigHistoryResponse>('/admin/config/history', { signal: controller.signal }),
    ])
      .then(([current, versions]) => {
        setConfig(current)
        // An in-progress edit must survive a background reload — only reseed
        // the editor when there is nothing in it to lose.
        if (!editingRef.current) setEditor(pretty(current))
        setHistory([...versions.data].reverse())
      })
      .catch((loadError: unknown) => {
        if (loadError instanceof DOMException && loadError.name === 'AbortError') return
        setError(errorMessage(loadError, 'Gateway configuration could not be loaded.'))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [reload])

  const redactedFields = useMemo(() => {
    if (!editing) return []
    const parsed = parseEditor(editor)
    return parsed ? findRedactedFields(parsed) : []
  }, [editing, editor])

  const activeJson = useMemo(() => (config ? pretty(config) : ''), [config])
  const pluginGroups = useMemo(() => (config ? groupPlugins(readPlugins(config)) : []), [config])
  const pluginCatalog = usePluginCatalog()
  const strategy = useMemo(() => (config ? readStrategy(config) : null), [config])

  const handleRefreshClick = () => {
    if (dirty) {
      setConfirmDiscard(true)
      return
    }
    setNotice('')
    refresh()
  }

  const discardAndRefresh = () => {
    setConfirmDiscard(false)
    setEditing(false)
    setNotice('')
    refresh()
  }

  const save = async () => {
    let parsed: Record<string, unknown>
    try {
      const value: unknown = JSON.parse(editor)
      if (!value || Array.isArray(value) || typeof value !== 'object') throw new Error('Configuration must be a JSON object.')
      parsed = value as Record<string, unknown>
    } catch (parseError) {
      setError(errorMessage(parseError, 'Configuration is not valid JSON.'))
      return
    }
    const blocked = findRedactedFields(parsed)
    if (blocked.length > 0) {
      setError(redactedFieldsMessage(blocked))
      return
    }
    setNotice('')
    setBusy(true)
    setError('')
    try {
      await request<{ status: string }>('/admin/config', { method: 'PUT', body: JSON.stringify(parsed) })
      setNotice('Configuration saved and applied.')
      setEditing(false)
      refresh()
    } catch (saveError) {
      setError(errorMessage(saveError, 'Configuration could not be applied.'))
    } finally {
      setBusy(false)
    }
  }

  const openRollback = (entry: ConfigHistoryEntry) => {
    setRollback(entry)
    setRollbackError('')
  }

  const performRollback = async () => {
    if (!rollback) return
    setNotice('')
    setBusy(true)
    setRollbackError('')
    try {
      await request(`/admin/config/rollback/${rollback.version}`, { method: 'POST' })
      setNotice(`Configuration rolled back to version ${rollback.version}.`)
      setRollback(null)
      setEditing(false)
      refresh()
    } catch (rollbackFailure) {
      // Rendered inside the dialog, not in the page-level notice: the dialog
      // stays open on failure and its backdrop covers the page, so a rollback
      // the gateway refused used to look exactly like the button doing nothing.
      setRollbackError(errorMessage(rollbackFailure, 'Configuration rollback failed.'))
    } finally {
      setBusy(false)
    }
  }

  const revertToStartup = async () => {
    setNotice('')
    setBusy(true)
    setResetError('')
    try {
      await request<{ status: string }>('/admin/config', { method: 'DELETE' })
      setNotice('The gateway is running the configuration it loaded at startup, recorded as a new history version.')
      setResetOpen(false)
      setEditing(false)
      refresh()
    } catch (resetFailure) {
      if (resetFailure instanceof ApiError && resetFailure.status === 501) {
        setResetUnsupported(true)
        setResetOpen(false)
        return
      }
      setResetError(errorMessage(resetFailure, 'The startup configuration could not be restored.'))
    } finally {
      setBusy(false)
    }
  }

  const rollbackChanges = rollback && config ? changedTopLevelKeys(config, rollback.config) : []
  const rollbackDescription = !rollback
    ? ''
    : rollbackChanges.length === 0
      ? `Version ${rollback.version} matches the active configuration, so nothing changes. A new history entry still records the operation.`
      : `Version ${rollback.version} replaces the active configuration and changes ${rollbackChanges.length === 1 ? 'one section' : `${rollbackChanges.length} sections`}: ${rollbackChanges.join(', ')}. A new history entry will record the operation.`

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        actions={
          <>
            <Button aria-label="Refresh configuration" size="icon" variant="outline" onClick={handleRefreshClick}>
              <RefreshCw aria-hidden="true" className={cn('size-4', loading && 'animate-spin')} />
            </Button>
            {isAdmin && !resetUnsupported ? (
              <Button type="button" variant="outline" onClick={() => { setResetOpen(true); setResetError('') }}>
                <Undo2 aria-hidden="true" size={16} />
                Revert to startup configuration
              </Button>
            ) : null}
          </>
        }
        description="Inspect the active runtime configuration and restore recorded versions."
        title="Configuration"
      />
      {!isAdmin ? <Notice>Read-only sessions can inspect configuration and history but cannot apply or roll back changes.</Notice> : null}
      {resetUnsupported ? (
        <Notice>
          This gateway manages its configuration through a component that cannot restore the startup configuration. Roll back to a
          recorded version instead.
        </Notice>
      ) : null}
      {notice ? <Notice tone="success">{notice}</Notice> : null}
      {error ? <Notice tone="error">{error}</Notice> : null}

      {loading && !config ? <LoadingState label="Loading configuration" /> : null}

      {config ? (
        <Tabs value={tab} onValueChange={(value) => setTab(value as Tab)}>
          <TabsList aria-label="Configuration views">
            <TabsTrigger value="current">
              <Code2 aria-hidden="true" size={16} />
              Current config
            </TabsTrigger>
            <TabsTrigger value="strategy">
              <Route aria-hidden="true" size={16} />
              Strategy
              <TabCount value={strategy?.targets.length ?? 0} />
            </TabsTrigger>
            <TabsTrigger value="plugins">
              <Puzzle aria-hidden="true" size={16} />
              Plugins
              <TabCount value={pluginGroups.length} />
            </TabsTrigger>
            <TabsTrigger value="history">
              <History aria-hidden="true" size={16} />
              History
              <TabCount value={history.length} />
            </TabsTrigger>
          </TabsList>

          <TabsContent className="mt-3" value="current">
            <section aria-label="Current configuration" className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h2 className="text-sm font-semibold text-foreground">Runtime configuration</h2>
                  <p className="text-sm text-muted-foreground">
                    Headers, subprocess environments and exporter settings are withheld by the Admin API; a plugin&rsquo;s
                    declared settings and {'${VAR}'} references are shown as written.
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  {isAdmin ? (
                    editing ? (
                      <>
                        <Button
                          disabled={busy}
                          variant="outline"
                          onClick={() => {
                            if (config) setEditor(pretty(config))
                            setEditing(false)
                          }}
                        >
                          Cancel
                        </Button>
                        <Button disabled={busy || redactedFields.length > 0} onClick={() => void save()}>
                          <Save aria-hidden="true" size={16} />
                          {busy ? 'Saving…' : 'Save and apply'}
                        </Button>
                      </>
                    ) : (
                      <Button variant="outline" onClick={() => setEditing(true)}>Edit JSON</Button>
                    )
                  ) : null}
                </div>
              </div>
              {editing && redactedFields.length > 0 ? (
                <Notice tone="warning">{redactedFieldsMessage(redactedFields)}</Notice>
              ) : null}
              {editing ? (
                <JsonEditor
                  copyLabel="Configuration JSON"
                  label="Gateway configuration JSON"
                  value={editor}
                  onChange={setEditor}
                />
              ) : (
                <JsonEditor copyLabel="Configuration JSON" label="Active gateway configuration" value={activeJson} />
              )}
            </section>
          </TabsContent>

          <TabsContent className="mt-3" value="strategy">
            {strategy ? <StrategyPanel state={strategy} /> : null}
          </TabsContent>

          <TabsContent className="mt-3" value="plugins">
            <PluginPanel catalog={pluginCatalog} groups={pluginGroups} />
          </TabsContent>

          <TabsContent className="mt-3" value="history">
            {history.length === 0 ? (
              <EmptyState description="Runtime updates and rollbacks will appear here." title="No config history" />
            ) : (
              <section className="rounded-xl border border-border bg-card">
                <Table className="max-sm:block">
                  <TableHeader className="max-sm:hidden">
                    <TableRow>
                      <TableHead>Version</TableHead>
                      <TableHead>Updated</TableHead>
                      <TableHead>Actor</TableHead>
                      <TableHead>Strategy</TableHead>
                      <TableHead>Rolled back from</TableHead>
                      <TableHead>
                        <span className="sr-only">Actions</span>
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody className="max-sm:block">
                    {history.map((entry) => {
                      const mode = entry.config.strategy as { mode?: string } | undefined
                      const expanded = expandedVersion === entry.version
                      const panelId = `config-version-${entry.version}`
                      return [
                        <TableRow className={mobileRow} key={entry.version}>
                          <TableCell className={mobileCell} data-label="Version">
                            <code className="text-xs">v{entry.version}</code>
                          </TableCell>
                          <TableCell className={mobileCell} data-label="Updated" data-volatile>{formatDateTime(entry.updated_at)}</TableCell>
                          {/*
                           * The whole point of issuing one key per operator: a
                           * version nobody can be asked about is a version
                           * nobody owns. Entries written before actors were
                           * recorded say so rather than showing a bare dash.
                           */}
                          <TableCell className={mobileCell} data-label="Actor">
                            {entry.actor ? entry.actor : <span className="text-muted-foreground">Not recorded</span>}
                          </TableCell>
                          <TableCell className={mobileCell} data-label="Strategy">
                            <StatusPill tone="info">{mode?.mode || 'unspecified'}</StatusPill>
                          </TableCell>
                          <TableCell className={mobileCell} data-label="Rolled back from">{entry.rolled_back_from ? `v${entry.rolled_back_from}` : '—'}</TableCell>
                          <TableCell className={cn(mobileCell, 'max-sm:flex-row max-sm:justify-end max-sm:border-0')}>
                            <div className="flex items-center justify-end gap-1">
                              <Button
                                aria-controls={panelId}
                                aria-expanded={expanded}
                                size="sm"
                                variant="ghost"
                                onClick={() => setExpandedVersion(expanded ? null : entry.version)}
                              >
                                {expanded ? <ChevronDown aria-hidden="true" size={14} /> : <ChevronRight aria-hidden="true" size={14} />}
                                {expanded ? 'Hide config' : `Compare v${entry.version}`}
                              </Button>
                              {isAdmin ? (
                                <Button size="sm" variant="outline" onClick={() => openRollback(entry)}>
                                  <RotateCcw aria-hidden="true" size={14} />
                                  Rollback
                                </Button>
                              ) : null}
                            </div>
                          </TableCell>
                        </TableRow>,
                        expanded ? (
                          <TableRow className="max-sm:block" key={`${entry.version}-detail`}>
                            <TableCell className="max-sm:block whitespace-normal" colSpan={6} id={panelId}>
                              <div className="flex flex-col gap-3 py-1">
                                <div>
                                  <h3 className="text-sm font-semibold text-foreground">
                                    What rolling back to v{entry.version} would change
                                  </h3>
                                  <p className="text-xs text-muted-foreground">
                                    Removed lines are the active configuration; added lines are version {entry.version}.
                                  </p>
                                </div>
                                <ConfigDiff
                                  after={pretty(entry.config)}
                                  before={activeJson}
                                  emptyLabel={`Version ${entry.version} is identical to the active configuration.`}
                                />
                                <details>
                                  <summary className="cursor-pointer text-sm text-muted-foreground">
                                    Full configuration recorded as version {entry.version}
                                  </summary>
                                  <JsonEditor
                                    className="mt-2"
                                    copyLabel={`Version ${entry.version} configuration JSON`}
                                    label={`Configuration recorded as version ${entry.version}`}
                                    value={pretty(entry.config)}
                                  />
                                </details>
                              </div>
                            </TableCell>
                          </TableRow>
                        ) : null,
                      ]
                    })}
                  </TableBody>
                </Table>
              </section>
            )}
            <p className="mt-3 text-xs text-muted-foreground">
              This trail is durable when the gateway is configured with a config store, and held in memory — cleared on restart, and
              renumbered from version 1 — when it is not. The response does not say which one served this list. Either way the
              configuration loaded from disk at startup is not itself a recorded version, so it is not a rollback target; use
              Revert to startup configuration to restore it.
            </p>
          </TabsContent>
        </Tabs>
      ) : null}

      <ConfirmDialog
        busy={busy}
        confirmLabel="Rollback configuration"
        description={rollbackDescription}
        error={rollbackError}
        open={rollback != null}
        title={`Roll back to version ${rollback?.version ?? ''}?`}
        onCancel={() => { if (!busy) { setRollback(null); setRollbackError('') } }}
        onConfirm={() => void performRollback()}
      />

      <ConfirmDialog
        busy={busy}
        confirmLabel="Revert configuration"
        dangerous
        description="The gateway returns to the configuration it loaded from disk at startup. Every change applied since — including any rollback — stops taking effect, and the restored configuration is recorded as a new history version."
        error={resetError}
        open={resetOpen}
        title="Revert to the startup configuration?"
        onCancel={() => { if (!busy) { setResetOpen(false); setResetError('') } }}
        onConfirm={() => void revertToStartup()}
      />

      <ConfirmDialog
        confirmLabel="Discard and refresh"
        dangerous
        description="Refreshing replaces the editor with the gateway's current configuration. Your unsaved edits will be lost."
        open={confirmDiscard}
        title="Discard unsaved changes?"
        onCancel={() => setConfirmDiscard(false)}
        onConfirm={discardAndRefresh}
      />
    </div>
  )
}
