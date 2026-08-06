import { ClipboardList, DatabaseZap, Puzzle, Ruler, ShieldBan, Timer, Wallet, type LucideIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { PluginCatalogEntry, PluginCatalogResponse } from '../types'
import { request } from './api'
import { containsRedactedValue } from './configRedaction'

export type { PluginCatalogEntry } from '../types'

/**
 * A glyph per built-in plugin.
 *
 * The one part of a plugin's description that stays here: an icon is a choice
 * about this dashboard's visual language, not a fact about the gateway, and
 * putting it on the wire would make the API carry a dependency on an icon set.
 * Everything the gateway actually knows — what a plugin is for, which config
 * keys it reads, the type it reports, whether a request survives it breaking —
 * is served by GET /admin/plugins/catalog.
 *
 * A name with no glyph, including a plugin built outside this repository, falls
 * back rather than failing.
 */
const PLUGIN_ICONS: Record<string, LucideIcon> = {
  'word-filter': ShieldBan,
  'max-token': Ruler,
  'rate-limit': Timer,
  budget: Wallet,
  'response-cache': DatabaseZap,
  'request-logger': ClipboardList,
}

export function pluginIcon(name: string): LucideIcon {
  return PLUGIN_ICONS[name] ?? Puzzle
}

export function pluginInfo(
  catalog: readonly PluginCatalogEntry[] | null | undefined,
  name: string,
): PluginCatalogEntry | undefined {
  return catalog?.find((plugin) => plugin.name === name)
}

/**
 * The built-in plugin catalog, fetched once per session.
 *
 * Cached at module scope because it describes the binary rather than its state:
 * it cannot change while the gateway is running, and two pages render it. A
 * failure is deliberately not surfaced as a page error — the catalog is
 * supplementary, and a gateway too old to serve the route still renders every
 * configured plugin from the configuration document.
 */
let catalogCache: Promise<PluginCatalogEntry[]> | null = null

async function loadPluginCatalog(signal?: AbortSignal): Promise<PluginCatalogEntry[]> {
  const response = await request<PluginCatalogResponse>('/admin/plugins/catalog', { signal })
  return Array.isArray(response.data) ? response.data : []
}

/** Discards the cached catalog. Sign-out and tests, so a session cannot inherit another's. */
export function resetPluginCatalog(): void {
  catalogCache = null
}

export function usePluginCatalog(): PluginCatalogEntry[] | null {
  const [catalog, setCatalog] = useState<PluginCatalogEntry[] | null>(null)

  useEffect(() => {
    let live = true
    catalogCache ??= loadPluginCatalog().catch((error: unknown) => {
      // A failed fetch must not be cached as the answer, or the panel stays
      // empty for the rest of the session over one transient failure.
      catalogCache = null
      throw error
    })
    catalogCache.then(
      (entries) => {
        if (live) setCatalog(entries)
      },
      () => {
        if (live) setCatalog(null)
      },
    )
    return () => {
      live = false
    }
  }, [])

  return catalog
}

/**
 * How a plugin's own failure is treated — the policy `plugin/manager.go`
 * applies, reported by the gateway rather than re-derived here.
 *
 * Worth showing beside a plugin: it is the difference between a broken plugin
 * costing a line in the log and a broken plugin returning 500 to every caller,
 * and nothing on screen otherwise says which. A denial is separate and always
 * honoured; this is only about a plugin that errors or panics.
 *
 * Undefined for a plugin the catalog does not describe. The deciding type is
 * the one the plugin reports from `Type()`, and the configuration document's
 * `plugins[].type` is not consulted by the gateway — so reading the policy off
 * the document would state the wrong outcome for exactly the entries where the
 * two disagree, and both of the repository's own rate limiters are written that
 * way.
 */
export type FailurePolicy = 'open' | 'closed'

export function failurePolicy(
  catalog: readonly PluginCatalogEntry[] | null | undefined,
  name: string,
): FailurePolicy | undefined {
  const entry = pluginInfo(catalog, name)
  if (entry === undefined) return undefined
  return entry.fails_open ? 'open' : 'closed'
}

export const FAILURE_POLICY_TEXT: Record<FailurePolicy, string> = {
  open: 'The request continues if this plugin breaks.',
  closed: 'The request fails with 500 if this plugin breaks.',
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value != null && typeof value === 'object' && !Array.isArray(value)
}

/**
 * One `plugins[]` entry as the config document spells it — `config.PluginConfig`.
 *
 * Read from GET /admin/config rather than GET /admin/plugins, which reports
 * only name, type and enabled. Stage and settings are exactly what an operator
 * comes here to check, and only the config document carries them.
 */
export interface ConfiguredPlugin {
  name: string
  type: string
  stage: string
  enabled: boolean
  config: Record<string, unknown>
}

/**
 * Every entry sharing one plugin name, collapsed to a single row.
 *
 * A plugin whose `Execute` branches on the stage — response-cache, budget and
 * request-logger — is required to be configured once per stage, with identical
 * settings so both entries resolve to the same instance. Rendered as two rows
 * that would read as a duplicated block somebody forgot to delete.
 */
export interface PluginGroup {
  name: string
  types: string[]
  stages: string[]
  entryCount: number
  enabledCount: number
  /** Settings the Admin API left readable: numbers, booleans, `${VAR}` references. */
  settings: Array<{ key: string; value: string }>
  /** Setting names whose value came back as the redaction placeholder. */
  redactedKeys: string[]
}

export function readPlugins(config: Record<string, unknown>): ConfiguredPlugin[] {
  const raw = config.plugins
  if (!Array.isArray(raw)) return []
  return raw.filter(isRecord).map((entry) => ({
    name: typeof entry.name === 'string' ? entry.name : '',
    type: typeof entry.type === 'string' ? entry.type : '',
    stage: typeof entry.stage === 'string' ? entry.stage : '',
    enabled: entry.enabled === true,
    config: isRecord(entry.config) ? entry.config : {},
  }))
}

const SETTING_VALUE_MAX = 80

function formatSettingValue(value: unknown): string {
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  if (text.length <= SETTING_VALUE_MAX) return text
  return `${text.slice(0, SETTING_VALUE_MAX)}…`
}

export function groupPlugins(plugins: ConfiguredPlugin[]): PluginGroup[] {
  const groups = new Map<string, PluginGroup>()
  for (const plugin of plugins) {
    const name = plugin.name || 'unnamed'
    const group = groups.get(name) ?? {
      name,
      types: [],
      stages: [],
      entryCount: 0,
      enabledCount: 0,
      settings: [],
      redactedKeys: [],
    }
    if (plugin.type && !group.types.includes(plugin.type)) group.types.push(plugin.type)
    if (plugin.stage && !group.stages.includes(plugin.stage)) group.stages.push(plugin.stage)
    group.entryCount++
    if (plugin.enabled) group.enabledCount++
    for (const [key, value] of Object.entries(plugin.config)) {
      // The entries of a multi-stage plugin must carry identical settings, so
      // the first one seen describes the instance and the rest repeat it.
      if (group.settings.some((setting) => setting.key === key) || group.redactedKeys.includes(key)) continue
      if (containsRedactedValue(value)) group.redactedKeys.push(key)
      else group.settings.push({ key, value: formatSettingValue(value) })
    }
    groups.set(name, group)
  }
  return [...groups.values()]
}

export interface PluginStatus {
  label: string
  tone: 'success' | 'warning' | 'neutral'
}

export function pluginStatus(group: PluginGroup): PluginStatus {
  if (group.enabledCount === 0) return { label: 'Disabled', tone: 'neutral' }
  if (group.enabledCount === group.entryCount) return { label: 'Enabled', tone: 'success' }
  // Divergent entries resolve to different instances, so the stage that is off
  // silently loses whatever state the stage that is on recorded.
  return { label: 'Partly enabled', tone: 'warning' }
}
