import { RefreshCw } from 'lucide-react'
import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import { PluginPanel } from '../components/PluginPanel'
import { Button, LoadingState, Notice, PageHeader } from '../components/ui'
import { request } from '../lib/api'
import { groupPlugins, readPlugins, usePluginCatalog } from '../lib/plugins'
import { useLoad } from '../lib/useLoad'

/**
 * The middleware running on the request path.
 *
 * Read from GET /admin/config, not GET /admin/plugins: the latter reports name,
 * type and enabled only, and the stage a plugin runs at and the settings it was
 * given are the two things an operator opens this page to check.
 *
 * What each plugin is for, and which config keys it accepts, comes from GET
 * /admin/plugins/catalog — the gateway describing its own build. This page used
 * to hold that list itself, and four of its six entries named a key no plugin
 * reads, which an operator following the panel would have written into their
 * configuration and had silently ignored.
 */
export default function PluginsPage() {
  const { data, loading, error, refresh } = useLoad(
    async (signal) => request<Record<string, unknown>>('/admin/config', { signal }),
    [],
    'The configured plugins could not be loaded.',
  )
  const catalog = usePluginCatalog()

  const groups = useMemo(() => (data ? groupPlugins(readPlugins(data)) : null), [data])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        actions={
          <Button aria-label="Refresh plugins" size="icon" type="button" variant="outline" onClick={refresh}>
            <RefreshCw aria-hidden="true" className={cn('size-4', loading && 'animate-spin')} />
          </Button>
        }
        description="Middleware the gateway runs before, after, and on failure of every routed request."
        title="Plugins"
      />

      {error ? <Notice tone="error">{error}</Notice> : null}
      {loading && !groups ? <LoadingState label="Loading plugins" /> : null}
      {groups ? <PluginPanel catalog={catalog} groups={groups} showCatalog /> : null}
    </div>
  )
}
