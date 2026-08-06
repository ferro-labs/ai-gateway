import { Search } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { formatNumber } from '../lib/format'
import { ProviderLogo } from './ProviderLogo'
import { Button, EmptyState } from './ui'
import type { Capabilities, ParamSupport } from '../types'

/**
 * Which OpenAI chat parameters each provider can express — the matrix
 * `providers/capabilities/matrix.go` is the source of, served by
 * GET /v1/capabilities.
 *
 * Symbol first, colour second. Three tones alone would leave this unreadable to
 * a red/green colour blind operator and to anyone printing it. The glyph
 * carries the meaning, the legend defines the glyph, and the tone only
 * reinforces both.
 */
const SUPPORT_MARKS: Record<ParamSupport, { glyph: string; label: string; className: string }> = {
  forward: { glyph: '→', label: 'forward', className: 'text-muted-foreground' },
  translate: { glyph: '⇄', label: 'translate', className: 'text-info' },
  unsupported: { glyph: '✕', label: 'unsupported', className: 'text-danger' },
}

function Legend() {
  return (
    <section aria-labelledby="capabilities-legend-heading" className="flex flex-col gap-2 rounded-xl border border-border bg-card p-3.5">
      <h2 className="text-sm font-semibold text-foreground" id="capabilities-legend-heading">How to read this matrix</h2>
      <dl className="flex flex-col gap-1.5 text-sm text-muted-foreground sm:flex-row sm:flex-wrap sm:gap-x-6">
        <div className="flex items-baseline gap-2">
          <dt className={cn('font-semibold', SUPPORT_MARKS.forward.className)}>{SUPPORT_MARKS.forward.glyph} Forward</dt>
          <dd>sent to the provider unchanged.</dd>
        </div>
        <div className="flex items-baseline gap-2">
          <dt className={cn('font-semibold', SUPPORT_MARKS.translate.className)}>{SUPPORT_MARKS.translate.glyph} Translate</dt>
          <dd>the gateway maps it onto a provider-native field.</dd>
        </div>
        <div className="flex items-baseline gap-2">
          <dt className={cn('font-semibold', SUPPORT_MARKS.unsupported.className)}>{SUPPORT_MARKS.unsupported.glyph} Unsupported</dt>
          <dd>
            the provider cannot express it; <code className="font-mono text-xs">compatibility.on_unsupported_param</code> decides
            whether it is warned, dropped, or rejected.
          </dd>
        </div>
      </dl>
      <p className="text-xs text-muted-foreground">
        A profile records only what the gateway knows about a provider. A parameter it does not record is forwarded, so a provider
        with nothing to declare forwards every parameter listed here.
      </p>
    </section>
  )
}

export function CapabilityMatrix({ capabilities }: { capabilities: Capabilities }) {
  const [filter, setFilter] = useState('')
  const profiles = capabilities.providers

  /*
   * The column set is the union of parameter names across every provider,
   * because a profile records what the gateway knows about that provider and
   * nothing more. An absent cell therefore means "forwarded", not "unsupported"
   * — a provider with no exceptions at all answers `{}` and forwards
   * everything. Reading the empty object as "supports nothing" would invert the
   * meaning of the whole table.
   */
  const allParams = useMemo(() => {
    const names = new Set<string>()
    for (const profile of Object.values(profiles)) {
      for (const param of Object.keys(profile)) names.add(param)
    }
    return [...names].sort()
  }, [profiles])
  const allProviderIds = useMemo(() => Object.keys(profiles).sort(), [profiles])

  const query = filter.trim().toLowerCase()
  const matchedProviders = useMemo(
    () => (query ? allProviderIds.filter((id) => id.toLowerCase().includes(query)) : allProviderIds),
    [allProviderIds, query],
  )
  const matchedParams = useMemo(
    () => (query ? allParams.filter((param) => param.toLowerCase().includes(query)) : allParams),
    [allParams, query],
  )

  /*
   * One box narrows either axis. A query that matches a provider but no
   * parameter keeps every column, and vice versa, so "gemini" and "logprobs"
   * both do the obvious thing without asking the operator to say which kind of
   * name they typed. A query matching neither narrows to nothing, which is the
   * honest answer rather than silently showing the full matrix.
   */
  const bothEmpty = matchedProviders.length === 0 && matchedParams.length === 0
  const visibleProviders = bothEmpty ? [] : matchedProviders.length > 0 ? matchedProviders : allProviderIds
  const visibleParams = bothEmpty ? [] : matchedParams.length > 0 ? matchedParams : allParams

  if (allProviderIds.length === 0) {
    return (
      <EmptyState
        description="No provider is registered, so the gateway reports no parameter profiles."
        title="No capabilities to show"
      />
    )
  }

  return (
    <>
      <Legend />

      <form
        className="flex flex-wrap items-center gap-2 rounded-xl border border-border bg-card p-2.5"
        onSubmit={(event) => event.preventDefault()}
      >
        <Label className="relative min-w-[220px] flex-1">
          <span className="sr-only">Filter by provider or parameter</span>
          <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="pl-8"
            placeholder="Filter by provider or parameter"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
          />
        </Label>
        {query ? (
          <Button type="button" variant="outline" onClick={() => setFilter('')}>Clear filter</Button>
        ) : null}
        <p aria-live="polite" className="text-xs text-muted-foreground">
          Showing {formatNumber(visibleProviders.length)} of {formatNumber(allProviderIds.length)} providers and{' '}
          {formatNumber(visibleParams.length)} of {formatNumber(allParams.length)} parameters
        </p>
      </form>

      {allParams.length === 0 ? (
        <EmptyState
          description="Every registered provider forwards every chat parameter the gateway models, so there is nothing to compare."
          title="No parameter exceptions"
        />
      ) : bothEmpty ? (
        <EmptyState
          description="Nothing matches that text. Clear the filter, or try a provider id such as “gemini” or a parameter such as “logprobs”."
          title="No provider or parameter matches"
        />
      ) : (
        <section aria-labelledby="capabilities-heading" className="rounded-xl border border-border bg-card">
          <div className="border-b border-border px-4 py-2.5">
            <h2 className="text-sm font-semibold text-foreground" id="capabilities-heading">Chat parameter support</h2>
          </div>
          {/* The table primitive already scrolls its own container, which is
              what keeps a 19-column matrix from pushing the page body
              sideways. */}
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="sticky left-0 z-10 bg-card" scope="col">Provider</TableHead>
                {visibleParams.map((param) => (
                  <TableHead className="font-mono text-[11px] normal-case" key={param} scope="col">{param}</TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {visibleProviders.map((providerId) => {
                const profile = profiles[providerId] ?? {}
                return (
                  <TableRow key={providerId}>
                    <TableHead className="sticky left-0 z-10 bg-card font-medium normal-case text-foreground" scope="row">
                      <span className="flex items-center gap-2">
                        <ProviderLogo provider={providerId} size="sm" />
                        {providerId}
                      </span>
                    </TableHead>
                    {visibleParams.map((param) => {
                      const mark = SUPPORT_MARKS[profile[param] ?? 'forward']
                      return (
                        <TableCell className={cn('text-center', mark.className)} key={param}>
                          <span aria-hidden="true">{mark.glyph}</span>
                          <span className="sr-only">{mark.label}</span>
                        </TableCell>
                      )
                    })}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </section>
      )}
    </>
  )
}
