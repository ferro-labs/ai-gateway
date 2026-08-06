import { BookOpen, RefreshCw } from 'lucide-react'
import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import { StrategyPanel } from '../components/StrategyPanel'
import { Button, LoadingState, Notice, PageHeader } from '../components/ui'
import { request } from '../lib/api'
import { readStrategy, STRATEGY_DOCS } from '../lib/strategies'
import { useLoad } from '../lib/useLoad'

/**
 * Which routing strategy the gateway is running, and what the alternatives are.
 *
 * Read from GET /admin/config rather than from a dedicated endpoint: the
 * configuration document is where the mode, the targets and every rule list
 * live together, and the gateway exposes no other view of the routing decision.
 */
export default function StrategyPage() {
  const { data, loading, error, refresh } = useLoad(
    async (signal) => request<Record<string, unknown>>('/admin/config', { signal }),
    [],
    'The routing strategy could not be loaded.',
  )

  const state = useMemo(() => (data ? readStrategy(data) : null), [data])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        actions={
          <>
            <Button aria-label="Refresh routing strategy" size="icon" type="button" variant="outline" onClick={refresh}>
              <RefreshCw aria-hidden="true" className={cn('size-4', loading && 'animate-spin')} />
            </Button>
            <a
              className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm font-medium transition-colors hover:bg-accent"
              href={STRATEGY_DOCS}
              rel="noreferrer"
              target="_blank"
            >
              <BookOpen aria-hidden="true" size={16} />
              Configuration reference
            </a>
          </>
        }
        description="How the gateway chooses which provider serves a request, and what each target is allowed to do."
        title="Routing Strategies"
      />

      {error ? <Notice tone="error">{error}</Notice> : null}
      {loading && !state ? <LoadingState label="Loading routing strategy" /> : null}
      {state ? <StrategyPanel showCatalog state={state} /> : null}
    </div>
  )
}
