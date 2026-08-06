import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { resetPluginCatalog } from '../lib/plugins'
import PluginsPage from './PluginsPage'

const request = vi.fn()

vi.mock('../lib/api', () => ({
  request: (path: string, init?: RequestInit) => request(path, init) as Promise<unknown>,
}))

const config = {
  plugins: [
    { name: 'response-cache', type: 'transform', stage: 'before_request', enabled: true, config: { max_age: 60 } },
    { name: 'response-cache', type: 'transform', stage: 'after_request', enabled: true, config: { max_age: 60 } },
    { name: 'request-logger', type: 'logging', stage: 'after_request', enabled: true, config: { persist: true } },
    { name: 'word-filter', type: 'guardrail', stage: 'before_request', enabled: false, config: {} },
  ],
}

/**
 * What GET /admin/plugins/catalog serves, spelled as the gateway spells it.
 *
 * These are the real config keys, taken from plugin/catalog.go — which
 * plugin/catalog_test.go holds equal to the keys each plugin's Init actually
 * reads. Nothing in this file may invent one: that is precisely the bug this
 * suite previously encoded, asserting `requests_per_minute`, `limit_usd`,
 * `window`, `ttl_seconds` and `log_level`, none of which any plugin reads.
 */
const catalog = {
  data: [
    {
      name: 'word-filter',
      type: 'guardrail',
      summary: 'Rejects a request whose prompt contains any blocked word.',
      settings: ['blocked_words', 'case_sensitive'],
      fails_open: false,
    },
    {
      name: 'max-token',
      type: 'guardrail',
      summary: 'Rejects a request over a token, message-count, or input-length ceiling before it reaches a provider.',
      settings: ['max_tokens', 'max_messages', 'max_input_length'],
      fails_open: false,
    },
    {
      name: 'rate-limit',
      type: 'ratelimit',
      summary: 'Bounds request rate globally and per API key or user, independently of the per-IP HTTP limiter.',
      settings: ['requests_per_second', 'burst', 'key_rpm', 'user_rpm'],
      fails_open: false,
    },
    {
      name: 'budget',
      type: 'ratelimit',
      summary: 'Tracks estimated spend per API key and refuses requests once the configured budget is exhausted.',
      settings: ['spend_limit_usd', 'input_per_m_tokens', 'output_per_m_tokens', 'max_keys', 'store_id'],
      fails_open: false,
    },
    {
      name: 'response-cache',
      type: 'transform',
      summary: 'Serves an identical request from memory instead of calling a provider again.',
      settings: ['max_age', 'max_entries'],
      fails_open: false,
    },
    {
      name: 'request-logger',
      type: 'logging',
      summary: 'Records each request for the Request Logs page, and persists it when a request-log store is configured.',
      settings: ['level', 'persist'],
      fails_open: true,
    },
  ],
}

function arm(document: Record<string, unknown> = config, servesCatalog = true) {
  request.mockImplementation((path: string) => {
    if (path === '/admin/config') return Promise.resolve(document)
    if (path === '/admin/plugins/catalog') {
      return servesCatalog ? Promise.resolve(catalog) : Promise.reject(new Error('no such route'))
    }
    return Promise.reject(new Error(`unexpected path ${path}`))
  })
}

function card(name: RegExp): HTMLElement {
  const found = screen.getByRole('heading', { name }).closest('li')
  if (!found) throw new Error(`no plugin card matching ${String(name)}`)
  return found
}

beforeEach(() => {
  request.mockReset()
  // The catalog is cached for the session, so a case would otherwise inherit
  // whatever an earlier one armed.
  resetPluginCatalog()
})

describe('PluginsPage', () => {
  it('collapses a stage-branching plugin to one card carrying both stages', async () => {
    arm()
    render(<PluginsPage />)
    const cache = await screen.findByRole('heading', { name: 'response-cache' })

    const panel = cache.closest('li')
    expect(panel).not.toBeNull()
    expect(within(panel as HTMLElement).getByText('before_request')).toBeInTheDocument()
    expect(within(panel as HTMLElement).getByText('after_request')).toBeInTheDocument()
    expect(within(panel as HTMLElement).getByText('Enabled')).toBeInTheDocument()
  })

  it('says what a plugin whose failure aborts the request would cost', async () => {
    // Nothing in the configuration document says this, and it is the difference
    // between a broken plugin costing a log line and a broken plugin returning
    // 500 to every caller. Read from the catalog's fails_open, which the gateway
    // derives from the type the plugin reports — not from the `type` the
    // document declares, which the gateway does not consult.
    arm()
    render(<PluginsPage />)
    await screen.findByRole('heading', { name: 'response-cache' })

    expect(within(card(/response-cache/)).getByText(/fails with 500/)).toBeInTheDocument()
    expect(within(card(/request-logger/)).getByText(/request continues/)).toBeInTheDocument()
  })

  it('describes a built-in plugin the configuration document cannot describe itself', async () => {
    arm()
    render(<PluginsPage />)
    await screen.findByRole('heading', { name: 'word-filter' })

    expect(within(card(/word-filter/)).getByText(/Rejects a request whose prompt contains any blocked word/)).toBeInTheDocument()
  })

  it('offers the config keys the plugins actually read', async () => {
    // The reproduction. This panel tells an operator to add one of these to the
    // configuration, and an unknown key is accepted in silence: following the
    // old `requests_per_minute: 1` left the 100/second default in place, and
    // `limit_usd` left the budget unlimited.
    arm()
    render(<PluginsPage />)
    const available = within(await screen.findByRole('region', { name: 'Built in, not configured' }))

    expect(available.getByText(/requests_per_second/)).toBeInTheDocument()
    expect(available.getByText(/spend_limit_usd/)).toBeInTheDocument()
    expect(available.queryByText(/requests_per_minute/)).toBeNull()
    expect(available.queryByText(/limit_usd · window/)).toBeNull()
  })

  it('lists the built-in plugins that are not configured, so they can be found at all', async () => {
    arm()
    render(<PluginsPage />)
    await screen.findByRole('heading', { name: 'Built in, not configured' })

    expect(screen.getByRole('heading', { name: 'max-token' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'budget' })).toBeInTheDocument()
    // Configured ones belong to the section above, not to both.
    const available = within(screen.getByRole('region', { name: 'Built in, not configured' }))
    expect(available.queryByRole('heading', { name: 'response-cache' })).toBeNull()
  })

  it('offers nothing when the gateway did not say what it ships', async () => {
    // A gateway too old to serve the catalog. Showing a remembered list would
    // put this dashboard back in the business of guessing another build's
    // config keys, which is what made the panel wrong.
    arm(config, false)
    render(<PluginsPage />)
    await screen.findByRole('heading', { name: 'response-cache' })

    expect(screen.queryByRole('region', { name: 'Built in, not configured' })).toBeNull()
  })

  it('says a plugin this build does not ship has no description, rather than inventing one', async () => {
    arm({ plugins: [{ name: 'house-guardrail', type: 'guardrail', stage: 'before_request', enabled: true, config: {} }] })
    render(<PluginsPage />)

    expect(await screen.findByText(/Not a plugin this build ships/)).toBeInTheDocument()
  })

  it('says a gateway runs no middleware rather than showing an empty grid', async () => {
    arm({ strategy: { mode: 'single' } })
    render(<PluginsPage />)

    expect(await screen.findByText('No plugins configured')).toBeInTheDocument()
  })

  it('reports a failed load as an error rather than as a gateway with no plugins', async () => {
    request.mockImplementation(() => Promise.reject(new Error('admin config is unreachable')))
    render(<PluginsPage />)

    expect(await screen.findByRole('alert')).toHaveTextContent('admin config is unreachable')
    expect(screen.queryByText('No plugins configured')).toBeNull()
  })
})
