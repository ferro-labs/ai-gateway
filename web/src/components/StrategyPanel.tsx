import { ArrowUpRight, CircleSlash, Infinity as InfinityIcon, Route } from 'lucide-react'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { STRATEGIES, STRATEGY_DOCS, type StrategyState } from '../lib/strategies'
import { formatNumber } from '../lib/format'
import { ProviderLogo } from './ProviderLogo'
import { EmptyState, StatusPill } from './ui'

/**
 * What the gateway is doing with a request, and what else it could be doing.
 *
 * Shared by the Configuration page's Strategy tab and the Routing Strategies
 * page so the two cannot drift; the second passes `showCatalog` to add the
 * modes that are available but not in use.
 */

// Below `sm` a row becomes a labelled card via `data-label` and a generated
// `::before`. Documented in full in KeysPage.tsx.
const mobileRow = 'max-sm:mb-2 max-sm:block max-sm:rounded-lg max-sm:border max-sm:border-border max-sm:p-2.5 max-sm:last:mb-0'
const mobileCell =
  'max-sm:flex max-sm:items-baseline max-sm:justify-between max-sm:gap-3 max-sm:border-b max-sm:border-border/60 max-sm:py-1 max-sm:last:border-0 ' +
  'max-sm:before:shrink-0 max-sm:before:text-[11px] max-sm:before:font-semibold max-sm:before:tracking-wide max-sm:before:text-muted-foreground max-sm:before:uppercase max-sm:before:content-[attr(data-label)]'

function ActiveStrategy({ state }: { state: StrategyState }) {
  const Icon = state.info?.icon ?? Route
  return (
    <section
      aria-labelledby="active-strategy-heading"
      className="flex flex-col gap-3 rounded-xl border border-primary/30 bg-primary-soft/50 p-4"
    >
      <div className="flex flex-wrap items-start gap-3">
        <span className="inline-flex size-11 shrink-0 items-center justify-center rounded-xl border border-primary/30 bg-card text-primary">
          <Icon aria-hidden="true" size={22} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            {/*
              * `||`, not `??`: an unset mode arrives as "", which is a string
              * and so passes `??` straight through — leaving an empty heading
              * on the page and nothing naming what the gateway is doing.
              */}
            <h2 className="text-base font-semibold text-foreground" id="active-strategy-heading">
              {state.info?.label || state.mode || 'No strategy configured'}
            </h2>
            {state.mode ? <StatusPill tone="success">Active</StatusPill> : null}
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
              strategy.mode: {state.mode || 'unset'}
            </code>
          </div>
          <p className="mt-1 max-w-prose text-sm text-muted-foreground">
            {state.info?.summary ??
              'This gateway reports a routing mode the dashboard does not recognise. It is applied all the same; only the description here is missing.'}
          </p>
        </div>
      </div>
      {state.mode === 'cost-optimized' ? (
        <p className="text-xs text-muted-foreground">
          Unpriced candidates:{' '}
          <code className="font-mono">{state.unpricedStrategy || 'fallback'}</code> — a provider the model catalog cannot price is
          used only when no priced provider can serve the request, unless this says otherwise.
        </p>
      ) : null}
    </section>
  )
}

function TargetTable({ targets }: { targets: StrategyState['targets'] }) {
  return (
    <section aria-labelledby="strategy-targets-heading" className="rounded-xl border border-border bg-card">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 border-b border-border px-4 py-2.5">
        <h2 className="text-sm font-semibold text-foreground" id="strategy-targets-heading">
          Routing targets
        </h2>
        <p className="text-xs text-muted-foreground">
          In configured order — the order Fallback and Conditional evaluate them in.
        </p>
      </div>
      <Table className="max-sm:block">
        <TableHeader className="max-sm:hidden">
          <TableRow>
            <TableHead scope="col">Target</TableHead>
            <TableHead scope="col">Weight</TableHead>
            <TableHead scope="col">Retry</TableHead>
            <TableHead scope="col">Concurrency</TableHead>
            <TableHead scope="col">Circuit breaker</TableHead>
            <TableHead scope="col">Model map</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody className="max-sm:block">
          {targets.map((target, index) => (
            <TableRow className={mobileRow} key={`${target.virtualKey}-${index}`}>
              <TableCell className={mobileCell} data-label="Target">
                <span className="flex items-center gap-2">
                  <ProviderLogo provider={target.virtualKey} size="sm" />
                  <span className="font-medium text-foreground">{target.virtualKey || 'unnamed'}</span>
                </span>
              </TableCell>
              <TableCell className={cn(mobileCell, 'tabular-nums')} data-label="Weight">
                {target.weight === undefined ? <span className="text-muted-foreground">default</span> : target.weight}
              </TableCell>
              <TableCell className={cn(mobileCell, 'tabular-nums')} data-label="Retry">
                {target.retryAttempts === undefined ? (
                  <span className="text-muted-foreground">1 attempt</span>
                ) : (
                  `${formatNumber(target.retryAttempts)} attempt${target.retryAttempts === 1 ? '' : 's'}`
                )}
              </TableCell>
              <TableCell className={mobileCell} data-label="Concurrency">
                {target.maxConcurrency === undefined ? (
                  <span className="inline-flex items-center gap-1 text-muted-foreground">
                    <InfinityIcon aria-hidden="true" size={14} />
                    unbounded
                  </span>
                ) : (
                  <span className="tabular-nums">
                    {formatNumber(target.maxConcurrency)} in flight
                    {target.queueSize ? ` · ${formatNumber(target.queueSize)} queued` : ''}
                  </span>
                )}
              </TableCell>
              <TableCell className={mobileCell} data-label="Circuit breaker">
                {target.circuitBreaker ? (
                  <StatusPill tone="info">Configured</StatusPill>
                ) : (
                  <span className="inline-flex items-center gap-1 text-muted-foreground">
                    <CircleSlash aria-hidden="true" size={14} />
                    Gateway default
                  </span>
                )}
              </TableCell>
              <TableCell className={mobileCell} data-label="Model map">
                {Object.keys(target.modelMap).length === 0 ? (
                  <span className="text-muted-foreground">none</span>
                ) : (
                  <ul className="space-y-0.5 font-mono text-xs">
                    {Object.entries(target.modelMap).map(([visible, upstream]) => (
                      <li key={visible}>
                        {visible} <span className="text-muted-foreground">→</span> {upstream}
                      </li>
                    ))}
                  </ul>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  )
}

function RuleTable({ state }: { state: StrategyState }) {
  const showsShare = state.rules.some((rule) => rule.share !== undefined)
  return (
    <section aria-labelledby="strategy-rules-heading" className="rounded-xl border border-border bg-card">
      <div className="border-b border-border px-4 py-2.5">
        <h2 className="text-sm font-semibold text-foreground" id="strategy-rules-heading">
          {state.ruleLabel}
        </h2>
        <p className="text-xs text-muted-foreground">Evaluated top to bottom; the first match decides the target.</p>
      </div>
      <Table className="max-sm:block">
        <TableHeader className="max-sm:hidden">
          <TableRow>
            <TableHead scope="col">{showsShare ? 'Variant' : 'Match'}</TableHead>
            {showsShare ? <TableHead scope="col">Share</TableHead> : null}
            <TableHead scope="col">Target</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody className="max-sm:block">
          {state.rules.map((rule, index) => (
            <TableRow className={mobileRow} key={`${rule.match}-${index}`}>
              <TableCell className={cn(mobileCell, 'font-mono text-xs')} data-label={showsShare ? 'Variant' : 'Match'}>
                {rule.match}
              </TableCell>
              {showsShare ? (
                <TableCell className={cn(mobileCell, 'tabular-nums')} data-label="Share">
                  {rule.share === undefined ? '—' : `${rule.share.toFixed(rule.share % 1 === 0 ? 0 : 1)}%`}
                </TableCell>
              ) : null}
              <TableCell className={mobileCell} data-label="Target">
                <span className="flex items-center gap-2">
                  <ProviderLogo provider={rule.target} size="sm" />
                  <span className="font-medium text-foreground">{rule.target || 'unnamed'}</span>
                </span>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  )
}

function StrategyCatalog({ mode }: { mode: string }) {
  return (
    <section aria-labelledby="strategy-catalog-heading" className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold text-foreground" id="strategy-catalog-heading">
          Every routing strategy
        </h2>
        <p className="text-sm text-muted-foreground">
          Set <code className="font-mono text-xs">strategy.mode</code> in the gateway configuration to switch. Each links to the
          configuration reference.
        </p>
      </div>
      <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {STRATEGIES.map((strategy) => {
          const active = strategy.id === mode
          return (
            <li className="flex" key={strategy.id}>
              <a
                className={cn(
                  'group flex w-full flex-col gap-2 rounded-xl border p-3.5 transition-colors',
                  'focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none',
                  active
                    ? 'border-primary/40 bg-primary-soft/50 hover:border-primary/60'
                    : 'border-border bg-card hover:border-border-strong hover:bg-accent/40',
                )}
                href={STRATEGY_DOCS}
                rel="noreferrer"
                target="_blank"
              >
                <span className="flex items-start justify-between gap-2">
                  <span
                    className={cn(
                      'inline-flex size-9 shrink-0 items-center justify-center rounded-lg border',
                      active ? 'border-primary/30 bg-card text-primary' : 'border-border bg-muted/60 text-muted-foreground',
                    )}
                  >
                    <strategy.icon aria-hidden="true" size={18} />
                  </span>
                  {active ? (
                    <StatusPill tone="success">Active</StatusPill>
                  ) : (
                    <ArrowUpRight
                      aria-hidden="true"
                      className="mt-1 size-4 text-faint transition-colors group-hover:text-muted-foreground"
                    />
                  )}
                </span>
                <span className="text-sm font-semibold text-foreground">{strategy.label}</span>
                <span className="text-xs leading-relaxed text-muted-foreground">{strategy.summary}</span>
                <span className="mt-auto pt-1 font-mono text-[11px] text-muted-foreground">
                  {strategy.configures || `mode: ${strategy.id}`}
                </span>
              </a>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

export function StrategyPanel({ state, showCatalog = false }: { state: StrategyState; showCatalog?: boolean }) {
  return (
    <div className="flex flex-col gap-4">
      <ActiveStrategy state={state} />

      {state.targets.length === 0 ? (
        <EmptyState
          description="Add entries under targets in the gateway configuration. Without one, no strategy has anywhere to route."
          title="No routing targets configured"
        />
      ) : (
        <TargetTable targets={state.targets} />
      )}

      {state.rules.length > 0 ? <RuleTable state={state} /> : null}

      {showCatalog ? <StrategyCatalog mode={state.mode} /> : null}
    </div>
  )
}
