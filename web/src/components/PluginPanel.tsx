import { ShieldCheck, ShieldOff } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import {
  FAILURE_POLICY_TEXT,
  failurePolicy,
  pluginIcon,
  pluginInfo,
  pluginStatus,
  type PluginCatalogEntry,
  type PluginGroup,
} from '../lib/plugins'
import { EmptyState, StatusPill } from './ui'

/**
 * The middleware on the request path, as the configuration document describes
 * it.
 *
 * Shared by the Configuration page's Plugins tab and the Plugins page, which
 * passes `showCatalog` to list the built-in plugins that are not configured.
 * Cards rather than a table: a plugin's settings are a variable-length list,
 * and a column wide enough for the longest one left every other row mostly
 * empty.
 *
 * `catalog` comes from the gateway. Null while it loads, or on a gateway too
 * old to serve it, and every panel below degrades to what the configuration
 * document itself says rather than to a guess.
 */

function PolicyNote({ catalog, name }: { catalog: PluginCatalogEntry[] | null; name: string }) {
  const policy = failurePolicy(catalog, name)
  // Nothing is claimed for a plugin the gateway did not describe: the deciding
  // type is the one it reports from Type(), which only the gateway can see.
  if (!policy) return null
  const Icon = policy === 'open' ? ShieldCheck : ShieldOff
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 text-xs',
        policy === 'open' ? 'text-muted-foreground' : 'text-warning',
      )}
    >
      <Icon aria-hidden="true" className="size-3.5 shrink-0" />
      {FAILURE_POLICY_TEXT[policy]}
    </span>
  )
}

function ConfiguredCard({ catalog, group }: { catalog: PluginCatalogEntry[] | null; group: PluginGroup }) {
  const status = pluginStatus(group)
  const info = pluginInfo(catalog, group.name)
  const Icon = pluginIcon(group.name)
  const enabled = group.enabledCount > 0
  return (
    <li
      className={cn(
        'flex flex-col gap-3 rounded-xl border bg-card p-4',
        enabled ? 'border-border' : 'border-dashed border-border',
      )}
    >
      <div className="flex items-start gap-3">
        <span
          className={cn(
            'inline-flex size-9 shrink-0 items-center justify-center rounded-lg border',
            enabled ? 'border-primary/25 bg-primary-soft/60 text-primary' : 'border-border bg-muted/60 text-muted-foreground',
          )}
        >
          <Icon aria-hidden="true" size={18} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-semibold text-foreground">{group.name}</h3>
            <StatusPill tone={status.tone}>{status.label}</StatusPill>
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {info?.summary ?? 'Not a plugin this build ships, so no description is available.'}
          </p>
        </div>
      </div>

      <dl className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs">
        <div className="flex items-center gap-1.5">
          <dt className="text-muted-foreground">Type</dt>
          <dd className="font-medium text-foreground">{group.types.join(', ') || '—'}</dd>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <dt className="text-muted-foreground">Stages</dt>
          <dd className="flex flex-wrap gap-1">
            {group.stages.length === 0
              ? '—'
              : group.stages.map((stage) => (
                <Badge key={stage} variant="outline">
                  {stage}
                </Badge>
              ))}
          </dd>
        </div>
      </dl>

      {group.settings.length > 0 || group.redactedKeys.length > 0 ? (
        <ul className="flex flex-wrap gap-1">
          {group.settings.map((setting) => (
            <li className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground" key={setting.key}>
              {setting.key}: {setting.value}
            </li>
          ))}
          {group.redactedKeys.map((key) => (
            <li className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground" key={key}>
              {key}: redacted
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-muted-foreground">No settings.</p>
      )}

      <PolicyNote catalog={catalog} name={group.name} />
    </li>
  )
}

function AvailableCatalog({ catalog, configured }: { catalog: PluginCatalogEntry[] | null; configured: Set<string> }) {
  // Nothing is offered until the gateway has said what it ships. The panel that
  // told an operator to add one of these previously named four config keys no
  // plugin reads, and an unknown key is accepted in silence — so an empty panel
  // is the honest state while the answer is unknown.
  const available = (catalog ?? []).filter((plugin) => !configured.has(plugin.name))
  if (available.length === 0) return null
  return (
    <section aria-labelledby="plugin-catalog-heading" className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold text-foreground" id="plugin-catalog-heading">
          Built in, not configured
        </h2>
        <p className="text-sm text-muted-foreground">
          Add an entry under <code className="font-mono text-xs">plugins</code> in the gateway configuration to run one of these.
        </p>
      </div>
      <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {available.map((plugin) => {
          const Icon = pluginIcon(plugin.name)
          return (
            <li className="flex flex-col gap-2 rounded-xl border border-dashed border-border bg-card/60 p-3.5" key={plugin.name}>
              <span className="flex items-center gap-2.5">
                <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg border border-border bg-muted/60 text-muted-foreground">
                  <Icon aria-hidden="true" size={16} />
                </span>
                <h3 className="text-sm font-semibold text-foreground">{plugin.name}</h3>
              </span>
              <span className="text-xs leading-relaxed text-muted-foreground">{plugin.summary}</span>
              <span className="font-mono text-[11px] text-muted-foreground">{plugin.settings.join(' · ')}</span>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

export function PluginPanel({
  catalog,
  groups,
  showCatalog = false,
}: {
  catalog: PluginCatalogEntry[] | null
  groups: PluginGroup[]
  showCatalog?: boolean
}) {
  const configured = new Set(groups.map((group) => group.name))
  return (
    <div className="flex flex-col gap-5">
      {groups.length === 0 ? (
        <EmptyState
          description="Add entries under plugins in the gateway configuration to run middleware on the request path."
          title="No plugins configured"
        />
      ) : (
        <section aria-labelledby="configured-plugins-heading" className="flex flex-col gap-3">
          <div>
            <h2 className="text-sm font-semibold text-foreground" id="configured-plugins-heading">
              Configured plugins
            </h2>
            <p className="text-sm text-muted-foreground">
              Read from the configuration document, so settings are shown as the gateway holds them.
            </p>
          </div>
          <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {groups.map((group) => (
              <ConfiguredCard catalog={catalog} group={group} key={group.name} />
            ))}
          </ul>
        </section>
      )}

      {showCatalog ? <AvailableCatalog catalog={catalog} configured={configured} /> : null}

      <p className="text-xs text-muted-foreground">
        A plugin that acts before a request and records after it — response-cache, budget, request-logger — needs one entry per
        stage, carrying identical settings so both resolve to the same instance. Two stages on one card is that setup, not a
        duplicate. The Admin API shows a value only under a key the plugin declares it reads — the keys listed on its catalog
        card — and replaces every other value, and any value carrying credential material, with a placeholder. A plugin
        registered outside the gateway declares nothing, so all of its values are withheld. {'${VAR}'} references name a value
        rather than carrying one, so they are shown as written.
      </p>
    </div>
  )
}
