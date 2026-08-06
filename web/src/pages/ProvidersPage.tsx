import { ChevronDown, ChevronRight, RefreshCw, Search, Server, ServerOff, SlidersHorizontal } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'
import { CapabilityMatrix } from '../components/CapabilityMatrix'
import { ProviderLogo } from '../components/ProviderLogo'
import { Button, EmptyState, LoadingState, Notice, PageHeader, StatusPill, TabCount } from '../components/ui'
import { request, requestProbe } from '../lib/api'
import { formatNumber } from '../lib/format'
import { useLoad } from '../lib/useLoad'
import type { CatalogModel, CatalogModelsResponse } from '../lib/playground'
import type { AdminHealth, Capabilities, CircuitState, GatewayHealth, ProviderCatalogResponse } from '../types'

type Tab = 'providers' | 'capabilities'
type SortKey = 'status' | 'name' | 'models'

/**
 * How often the connection state re-reads /health.
 *
 * Circuit state is the one thing on this page that changes without an operator
 * doing anything, and it changes during exactly the minutes nobody wants to be
 * clicking Refresh. /health is unauthenticated and rate-limit exempt, so the
 * cost is a cheap registry walk; /readyz, which shares a process-global 10 rps
 * limiter with every other caller, is deliberately not the endpoint used here.
 */
const CIRCUIT_REFRESH_MS = 15_000

/**
 * One provider row in the gallery, joined from the endpoints that each know part
 * of it. The roster is the full built-in catalog — every provider appears,
 * whether or not a credential registers it — so `registered` decides which half
 * of the card's behaviour applies.
 *
 * `circuit` is null when the provider never resolved: it has no breaker because
 * it has nothing to break. That is a different fact from "closed" and must not
 * render as one. `catalogModels` is the provider's non-deprecated catalog size,
 * the count a card falls back to when the gateway advertises none of its own.
 */
interface ProviderRow {
  name: string
  registered: boolean
  status: string
  message?: string
  models: CatalogModel[]
  catalogModels: number
  circuit: CircuitState | null
}

interface Connection {
  label: string
  tone: 'success' | 'warning' | 'error' | 'neutral'
  /** True only for the state that means traffic is flowing. */
  live: boolean
}

/**
 * What the gateway will currently do with a request for this provider.
 *
 * A provider with no registered credential is "Not configured" — it is in the
 * catalog but nothing routes to it. "Connected" is claimed only where both
 * halves agree: the provider resolved in the registry *and* its breaker is
 * closed. `status` alone is set even when the credential behind it is revoked,
 * so a pill driven by it would report a healthy connection to a provider that
 * has failed every recent call. An open breaker is the gateway's own record of
 * exactly that, and it is the more recent evidence of the two.
 */
function connectionOf(provider: ProviderRow): Connection {
  if (!provider.registered) return { label: 'Not configured', tone: 'neutral', live: false }
  if (provider.status === 'unavailable') return { label: 'Unavailable', tone: 'error', live: false }
  if (provider.status !== 'available') {
    // A newer gateway may report a state this build has no word for. Verbatim
    // and neutral is the only honest rendering: relabelling it into one of the
    // words here would claim knowledge, and colouring it would claim a severity.
    return { label: provider.status, tone: 'neutral', live: false }
  }
  if (provider.circuit === 'open') return { label: 'Circuit open', tone: 'error', live: false }
  if (provider.circuit === 'half-open') return { label: 'Recovering', tone: 'warning', live: false }
  if (provider.circuit === 'closed') return { label: 'Connected', tone: 'success', live: true }
  // Registered, but the gateway declined to report a breaker. Rendering that as
  // connected would claim a healthy link nothing has measured.
  return { label: 'Registered', tone: 'neutral', live: false }
}

/** Live, non-deprecated models the gateway currently advertises for a provider. */
function liveModelCount(provider: ProviderRow): number {
  return provider.models.filter((model) => !model.deprecated).length
}

/**
 * The count a card shows: the live advertised count when the gateway reports
 * one, otherwise the provider's catalog size. A configured provider that
 * advertises nothing (an any-model upstream, a credential that lists no models)
 * and every unconfigured provider both fall back to the catalog, so a card
 * names a real number instead of a bare zero.
 */
function displayModelCount(provider: ProviderRow): number {
  const live = liveModelCount(provider)
  return live > 0 ? live : provider.catalogModels
}

const SORT_LABELS: Record<SortKey, string> = {
  status: 'Connection state',
  name: 'Name',
  models: 'Model count',
}

/** Connected first, then degraded, then everything the gateway cannot use. */
const STATUS_RANK: Record<Connection['tone'], number> = { success: 0, neutral: 1, warning: 2, error: 3 }

/**
 * Unconfigured providers always sort last, whatever the key: they are the
 * catalog's tail, not a connection state to rank among the live ones.
 */
function statusRankOf(provider: ProviderRow): number {
  if (!provider.registered) return 90
  return STATUS_RANK[connectionOf(provider).tone]
}

function sortProviders(providers: ProviderRow[], key: SortKey): ProviderRow[] {
  const sorted = [...providers]
  if (key === 'name') return sorted.sort((a, b) => a.name.localeCompare(b.name))
  if (key === 'models') {
    return sorted.sort((a, b) => displayModelCount(b) - displayModelCount(a) || a.name.localeCompare(b.name))
  }
  return sorted.sort((a, b) => statusRankOf(a) - statusRankOf(b) || a.name.localeCompare(b.name))
}

function StatTile({ label, value, tone }: { label: string; value: string; tone?: 'success' | 'warning' }) {
  return (
    <div className="rounded-xl border border-border bg-card px-4 py-3">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          'mt-0.5 text-2xl font-semibold tabular-nums',
          tone === 'success' ? 'text-success' : tone === 'warning' ? 'text-warning' : 'text-foreground',
        )}
      >
        {value}
      </dd>
    </div>
  )
}

function ProviderCard({ provider, query }: { provider: ProviderRow; query: string }) {
  const [open, setOpen] = useState(false)
  const connection = connectionOf(provider)
  const panelId = `provider-models-${provider.name}`
  // Sorted so a provider advertising two hundred models reads as a catalog
  // rather than as whatever order the upstream happened to answer in.
  const models = useMemo(() => [...provider.models].sort((a, b) => a.id.localeCompare(b.id)), [provider.models])
  const matching = query ? models.filter((model) => model.id.toLowerCase().includes(query)) : models
  const count = displayModelCount(provider)

  return (
    <li className="flex flex-col rounded-xl border border-border bg-card">
      <div className="flex items-start gap-3 p-4">
        {/* The mark stays in full colour so the gallery reads as official
            artwork; an unconfigured provider is dimmed rather than greyed, so it
            is legible as itself while clearly the card that is not wired up. */}
        <ProviderLogo provider={provider.name} size="lg" className={cn(!provider.registered && 'opacity-55')} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-semibold text-foreground">{provider.name}</h3>
            <StatusPill tone={connection.tone}>
              {connection.live ? (
                <span aria-hidden="true" className="size-1.5 rounded-full bg-success" />
              ) : null}
              {connection.label}
            </StatusPill>
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {!provider.registered
              ? `${formatNumber(count)} model${count === 1 ? '' : 's'} in the catalog`
              : provider.status === 'available'
                ? `${formatNumber(count)} model${count === 1 ? '' : 's'}`
                : 'Never resolved, so the gateway holds no model list for it.'}
          </p>
          {/*
           * A provider the gateway reports without a breaker is not the same as
           * one whose breaker is closed, and the pill above says only
           * "Registered". Without this line the difference is invisible, which
           * is how an unreadable /health looks exactly like a healthy one.
           */}
          {provider.registered && provider.status === 'available' && provider.circuit === null ? (
            <p className="mt-1 text-xs text-muted-foreground">No circuit state reported</p>
          ) : null}
          {provider.message ? <p className="mt-1 text-xs text-muted-foreground">{provider.message}</p> : null}
        </div>
      </div>

      {/*
       * The model-list expander only exists for a registered provider: it lists
       * the models the gateway actually advertises, and an unconfigured provider
       * has none — its card shows the catalog size above and nothing to expand.
       */}
      {provider.registered ? (
        <>
          <div className="mt-auto border-t border-border px-2 py-1.5">
            {/*
             * Named for the provider, not for the count: every card on the grid
             * would otherwise offer a button called "Show 2 models", and a list
             * of identically named controls is unusable by anyone navigating by
             * name.
             */}
            <Button
              aria-controls={panelId}
              aria-expanded={open}
              aria-label={`${open ? 'Hide' : 'Show'} ${provider.name} models`}
              className="w-full justify-start text-muted-foreground"
              size="sm"
              type="button"
              variant="ghost"
              onClick={() => setOpen((value) => !value)}
            >
              {open ? <ChevronDown aria-hidden="true" size={14} /> : <ChevronRight aria-hidden="true" size={14} />}
              {open ? 'Hide models' : `Show ${formatNumber(matching.length)} model${matching.length === 1 ? '' : 's'}`}
            </Button>
          </div>

          {open ? (
            <div className="max-h-56 overflow-auto border-t border-border bg-muted/30 p-3" id={panelId}>
              {matching.length > 0 ? (
                <ul className="flex flex-wrap gap-1.5">
                  {matching.map((model) => (
                    <li key={model.id}>
                      <Badge
                        className={cn('font-mono text-[11px] font-normal', model.deprecated && 'text-warning')}
                        title={model.deprecated ? 'Deprecated in the model catalog' : undefined}
                        variant="outline"
                      >
                        {model.id}
                        {model.deprecated ? <span aria-label="deprecated"> ·&nbsp;deprecated</span> : null}
                      </Badge>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {provider.status === 'available'
                    ? query
                      ? 'No model on this provider matches the filter.'
                      : 'No models are currently advertised.'
                    : 'Check its credentials and restart the gateway.'}
                </p>
              )}
            </div>
          ) : null}
        </>
      ) : null}
    </li>
  )
}

export default function ProvidersPage() {
  const [tab, setTab] = useState<Tab>('providers')
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<SortKey>('status')

  const { data, loading, error, refresh } = useLoad(
    async (signal) => {
      const [roster, health, catalog, gateway] = await Promise.all([
        // The roster — every built-in provider, registered or not. This is what
        // makes the page a gallery of the whole catalog rather than only the
        // providers a credential happened to register.
        request<ProviderCatalogResponse>('/admin/providers/catalog', { signal }),
        // Status and message for the registered subset. A provider that failed
        // to resolve appears here as "unavailable" — a state the roster's
        // registered flag alone cannot express — so it decorates rather than
        // decides the roster.
        request<AdminHealth>('/admin/health', { signal }),
        // The models, from the same endpoint the playground and every
        // OpenAI-compatible client read.
        request<CatalogModelsResponse>('/v1/models', { signal }),
        // A probe, so the 503 an empty registry answers with is read as data
        // rather than thrown. Its failure is swallowed because it contributes
        // one column: an unreadable circuit is better shown as "Registered"
        // than as an empty page where the roster used to be.
        requestProbe<GatewayHealth>('/health', { signal }).catch(() => null),
      ])
      const healthByName = new Map(health.providers.map((provider) => [provider.name, provider]))
      // Grouped by owner, because /v1/models is a flat list across providers.
      const models = new Map<string, CatalogModel[]>()
      for (const model of catalog.data) {
        const owner = model.owned_by ?? ''
        const group = models.get(owner)
        if (group) group.push(model)
        else models.set(owner, [model])
      }
      const circuits = new Map((gateway?.providers ?? []).map((provider) => [provider.name, provider.circuit]))
      return roster.providers.map<ProviderRow>((entry) => {
        const entryHealth = healthByName.get(entry.id)
        // A name /admin/health lists is registered even when its Get() failed —
        // that is exactly the "unavailable" case, which the catalog's own
        // Get()-based flag reports as not registered. Health is the newer,
        // narrower evidence, so its presence wins.
        const registered = entry.registered || entryHealth !== undefined
        return {
          name: entry.id,
          registered,
          status: entryHealth ? entryHealth.status : registered ? 'available' : 'unregistered',
          message: entryHealth?.message,
          models: models.get(entry.id) ?? [],
          catalogModels: entry.catalog_models,
          circuit: circuits.get(entry.id) ?? null,
        }
      })
    },
    [],
    'Providers could not be loaded.',
    { refetchInterval: CIRCUIT_REFRESH_MS },
  )
  const providers: ProviderRow[] = useMemo(() => data ?? [], [data])

  const capabilities = useLoad(
    async (signal) => request<Capabilities>('/v1/capabilities', { signal }),
    [],
    'Provider capabilities could not be loaded.',
  )

  const query = filter.trim().toLowerCase()
  /*
   * One box searches both a provider id and the models it advertises, so
   * "gpt-4o" answers "who can serve this" without asking the operator to open
   * every card in turn. A provider matched by name keeps its whole catalog;
   * only a provider matched solely through a model is narrowed to it.
   */
  const visible = useMemo(() => {
    const matched = query
      ? providers.filter(
        (provider) =>
          provider.name.toLowerCase().includes(query) ||
            provider.models.some((model) => model.id.toLowerCase().includes(query)),
      )
      : providers
    return sortProviders(matched, sort)
  }, [providers, query, sort])

  const total = providers.length
  const connected = providers.filter((provider) => connectionOf(provider).live).length
  const configured = providers.filter((provider) => provider.registered).length
  const unavailable = providers.filter((provider) => provider.registered && provider.status !== 'available').length
  const totalModels = providers.reduce((sum, provider) => sum + (provider.registered ? liveModelCount(provider) : 0), 0)

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        actions={
          // Reloads both tabs, not the visible one: a control that appears to do
          // nothing because the other panel is showing is worse than one extra
          // request against two cheap reads.
          <Button
            aria-label="Refresh providers"
            size="icon"
            type="button"
            variant="outline"
            onClick={() => { refresh(); capabilities.refresh() }}
          >
            <RefreshCw aria-hidden="true" className={cn('size-4', (loading || capabilities.loading) && 'animate-spin')} size={18} />
          </Button>
        }
        description="Every provider the gateway supports — connected ones first, then the rest of the catalog. Each card shows the models it serves and, on the Capabilities tab, the chat parameters it accepts."
        title="Providers"
      />

      <Tabs value={tab} onValueChange={(value) => setTab(value as Tab)}>
        <TabsList aria-label="Provider views">
          <TabsTrigger value="providers">
            <Server aria-hidden="true" size={16} />
            Providers
            <TabCount value={providers.length} />
          </TabsTrigger>
          <TabsTrigger value="capabilities">
            <SlidersHorizontal aria-hidden="true" size={16} />
            Capabilities
          </TabsTrigger>
        </TabsList>

        <TabsContent className="mt-3 flex flex-col gap-4" value="providers">
          {error ? <Notice tone="error">{error}</Notice> : null}
          {loading && providers.length === 0 ? <LoadingState label="Loading providers" /> : null}
          {!loading && providers.length === 0 && !error ? (
            <EmptyState
              action={<ServerOff aria-hidden="true" size={24} />}
              description="The gateway reported no providers at all. Reload once it has finished starting."
              title="No providers to show"
            />
          ) : null}

          {providers.length > 0 ? (
            <>
              {/*
               * Announced, because the connection state refreshes on its own: a
               * breaker tripping while the operator reads another part of the
               * page is the one change worth interrupting for.
               */}
              <section aria-labelledby="provider-summary-heading">
                <h2 className="sr-only" id="provider-summary-heading">Provider summary</h2>
                <dl aria-live="polite" className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5">
                  <StatTile label="Providers" value={formatNumber(total)} />
                  <StatTile label="Connected" tone="success" value={formatNumber(connected)} />
                  <StatTile label="Configured" value={formatNumber(configured)} />
                  <StatTile label="Unavailable" tone={unavailable > 0 ? 'warning' : undefined} value={formatNumber(unavailable)} />
                  <StatTile label="Models advertised" value={formatNumber(totalModels)} />
                </dl>
              </section>

              <form
                className="flex flex-wrap items-center gap-2 rounded-xl border border-border bg-card p-2.5"
                onSubmit={(event) => event.preventDefault()}
              >
                <Label className="relative min-w-[220px] flex-1">
                  <span className="sr-only">Filter by provider or model</span>
                  <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    className="pl-8"
                    placeholder="Filter by provider or model, e.g. gpt-4o"
                    value={filter}
                    onChange={(event) => setFilter(event.target.value)}
                  />
                </Label>
                <Label className="flex items-center gap-2 text-sm text-muted-foreground">
                  <span className="whitespace-nowrap">Sort by</span>
                  <Select value={sort} onValueChange={(next) => setSort(next ?? sort)}>
                    <SelectTrigger aria-label="Sort providers by" className="w-45">
                      {/* Without this the trigger shows the raw value — "status"
                          rather than "Connection state". */}
                      <SelectValue>{(value: SortKey) => SORT_LABELS[value]}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {(Object.keys(SORT_LABELS) as SortKey[]).map((key) => (
                        <SelectItem key={key} value={key}>{SORT_LABELS[key]}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Label>
                {query ? (
                  <Button type="button" variant="outline" onClick={() => setFilter('')}>Clear filter</Button>
                ) : null}
                <p aria-live="polite" className="text-xs text-muted-foreground">
                  Showing {formatNumber(visible.length)} of {formatNumber(providers.length)} providers
                </p>
              </form>

              {visible.length === 0 ? (
                <EmptyState
                  description="No provider id and no advertised model matches that text. Clear the filter and try again."
                  title="Nothing matches that filter"
                />
              ) : (
                <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                  {visible.map((provider) => (
                    <ProviderCard key={provider.name} provider={provider} query={query} />
                  ))}
                </ul>
              )}
            </>
          ) : null}
        </TabsContent>

        <TabsContent className="mt-3 flex flex-col gap-4" value="capabilities">
          {capabilities.error ? <Notice tone="error">{capabilities.error}</Notice> : null}
          {capabilities.loading && !capabilities.data ? <LoadingState label="Loading provider capabilities" /> : null}
          {capabilities.data ? <CapabilityMatrix capabilities={capabilities.data} /> : null}
        </TabsContent>
      </Tabs>
    </div>
  )
}
