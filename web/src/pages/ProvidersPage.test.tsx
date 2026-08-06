import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CatalogModel } from '../lib/playground'
import type { AdminHealth, Capabilities, GatewayHealth, ProviderCatalogResponse } from '../types'
import ProvidersPage from './ProvidersPage'

const request = vi.fn()
const requestProbe = vi.fn()

vi.mock('../lib/api', () => ({
  request: (path: string, init?: RequestInit) => request(path, init) as Promise<unknown>,
  requestProbe: (path: string, init?: RequestInit) => requestProbe(path, init) as Promise<unknown>,
}))

interface Fixtures {
  roster: ProviderCatalogResponse
  health: AdminHealth
  /** /v1/models grouped by owner, mirroring how the page regroups the flat list. */
  models: Record<string, CatalogModel[]>
  gateway: GatewayHealth | Error
  capabilities: Capabilities | Error
}

function model(id: string, owner: string): CatalogModel {
  return { id, owned_by: owner }
}

const defaults: Fixtures = {
  roster: {
    providers: [
      { id: 'openai', registered: true, catalog_models: 40 },
      { id: 'anthropic', registered: true, catalog_models: 12 },
      // Its Get() failed, so the roster's own flag says not-registered; health
      // knows better (see the merge test).
      { id: 'mistral', registered: false, catalog_models: 6 },
      // Registered and healthy but advertises nothing — the catalog count is the
      // fallback the card must fall to.
      { id: 'ollama-cloud', registered: true, catalog_models: 5 },
      // Never configured — catalog-only, shown as a "Not configured" card.
      { id: 'gemini', registered: false, catalog_models: 8 },
      { id: 'groq', registered: false, catalog_models: 18 },
    ],
  },
  health: {
    status: 'degraded',
    providers: [
      { name: 'openai', status: 'available', models: 2 },
      { name: 'anthropic', status: 'available', models: 1 },
      { name: 'mistral', status: 'unavailable', models: 0, message: 'provider not found in registry' },
      { name: 'ollama-cloud', status: 'available', models: 0 },
    ],
  },
  models: {
    openai: [model('gpt-4o', 'openai'), model('gpt-4o-mini', 'openai')],
    anthropic: [model('claude-sonnet', 'anthropic')],
    'ollama-cloud': [],
  },
  gateway: {
    status: 'ok',
    providers: [
      { name: 'openai', status: 'available', circuit: 'closed', models: 2 },
      { name: 'anthropic', status: 'available', circuit: 'open', models: 1 },
    ],
  },
  capabilities: {
    providers: {
      gemini: { response_format: 'translate', logprobs: 'unsupported' },
      openai: {},
    },
  },
}

function arm(overrides: Partial<Fixtures> = {}) {
  const fixtures = { ...defaults, ...overrides }
  request.mockImplementation((path: string) => {
    if (path === '/admin/providers/catalog') return Promise.resolve(fixtures.roster)
    if (path === '/admin/health') return Promise.resolve(fixtures.health)
    if (path === '/v1/models') {
      return Promise.resolve({ object: 'list', data: Object.values(fixtures.models).flat() })
    }
    if (path === '/v1/capabilities') {
      return fixtures.capabilities instanceof Error ? Promise.reject(fixtures.capabilities) : Promise.resolve(fixtures.capabilities)
    }
    return Promise.reject(new Error(`unexpected path ${path}`))
  })
  requestProbe.mockImplementation((path: string) => {
    if (path === '/health') {
      return fixtures.gateway instanceof Error ? Promise.reject(fixtures.gateway) : Promise.resolve(fixtures.gateway)
    }
    return Promise.reject(new Error(`unexpected probe ${path}`))
  })
}

/*
 * Found by heading, not by text anywhere in the card: "openai" is also the
 * owner of its own models and the id of a capability row, so a text search
 * matches in three places. A list item takes no accessible name from its
 * content, so the heading is the only way in.
 */
function providerCard(provider: string): Promise<HTMLElement> {
  return waitFor(() => {
    const card = screen.getByRole('heading', { name: provider, level: 3 }).closest('li')
    if (!card) throw new Error(`no provider card for ${provider}`)
    return card
  })
}

function capabilitiesRow(provider: string): HTMLElement {
  const header = screen.getByRole('rowheader', { name: provider })
  const row = header.closest('tr')
  if (!row) throw new Error(`no capabilities row for ${provider}`)
  return row
}

async function openCapabilitiesTab() {
  await userEvent.click(screen.getByRole('tab', { name: /capabilities/i }))
  await screen.findByRole('heading', { name: /how to read this matrix/i })
}

beforeEach(() => {
  request.mockReset()
  requestProbe.mockReset()
})

describe('ProvidersPage — gallery', () => {
  it('shows every built-in provider, not only the registered ones', async () => {
    arm()
    render(<ProvidersPage />)

    // gemini and groq have no credential, so /admin/health never mentions them;
    // the catalog roster is what puts them on the page.
    await providerCard('gemini')
    await providerCard('groq')
    // Both are catalog-only, so both wear the "Not configured" pill.
    expect(await screen.findAllByText('Not configured')).toHaveLength(2)
  })

  it('orders connected providers ahead of unconfigured ones', async () => {
    arm()
    render(<ProvidersPage />)
    await providerCard('openai')

    const order = screen.getAllByRole('heading', { level: 3 }).map((h) => h.textContent)
    // openai is connected (rank 0); gemini/groq are catalog-only (rank last).
    expect(order.indexOf('openai')).toBeLessThan(order.indexOf('gemini'))
    expect(order.indexOf('openai')).toBeLessThan(order.indexOf('groq'))
  })

  it('shows an unconfigured provider its catalog model count and no model list', async () => {
    arm()
    render(<ProvidersPage />)
    const row = await providerCard('gemini')

    expect(within(row).getByText('Not configured')).toBeInTheDocument()
    expect(within(row).getByText('8 models in the catalog')).toBeInTheDocument()
    // Nothing routes to it, so there is no advertised list to expand.
    expect(within(row).queryByRole('button', { name: /Show gemini models/ })).not.toBeInTheDocument()
  })

  it('falls back to the catalog count when a configured provider advertises none', async () => {
    arm()
    render(<ProvidersPage />)
    const row = await providerCard('ollama-cloud')

    // Registered and healthy but advertising zero live models: the card names
    // the catalog size rather than a bare 0.
    expect(within(row).getByText('5 models')).toBeInTheDocument()
  })

  it('reconciles a registered-but-unavailable provider health knows and the roster does not', async () => {
    arm()
    render(<ProvidersPage />)
    const row = await providerCard('mistral')

    // Roster says registered:false (its Get failed); health reports it
    // unavailable. Health wins, so it is a broken-credential card, not a
    // "Not configured" one.
    expect(within(row).getByText('Unavailable')).toBeInTheDocument()
    expect(within(row).getByText('provider not found in registry')).toBeInTheDocument()
    expect(within(row).queryByText('Not configured')).not.toBeInTheDocument()
  })

  it('gives each circuit state its own tone', async () => {
    arm()
    render(<ProvidersPage />)

    const closed = within(await providerCard('openai')).getByText('Connected')
    const open = within(await providerCard('anthropic')).getByText('Circuit open')
    expect(closed.className).toContain('success')
    expect(open.className).toContain('danger')
  })

  it('keeps the roster when the unauthenticated health probe fails', async () => {
    arm({ gateway: new Error('probe unreachable') })
    render(<ProvidersPage />)

    const row = await providerCard('openai')
    expect(within(row).getByText('Registered')).toBeInTheDocument()
    expect(within(row).getByText('No circuit state reported')).toBeInTheDocument()
  })

  it('reports a failed roster load as an error, not an empty gallery', async () => {
    arm()
    request.mockImplementation((path: string) => {
      if (path === '/admin/providers/catalog') return Promise.reject(new Error('provider catalog is unreachable'))
      return Promise.resolve({ object: 'list', data: [] })
    })
    render(<ProvidersPage />)

    expect(await screen.findByRole('alert')).toHaveTextContent('provider catalog is unreachable')
    expect(screen.queryByText('No providers to show')).not.toBeInTheDocument()
  })

  it('expands a registered provider to name the models it advertises', async () => {
    arm()
    render(<ProvidersPage />)
    await providerCard('openai')

    await userEvent.click(screen.getByRole('button', { name: 'Show openai models' }))

    expect(await screen.findByText('gpt-4o')).toBeInTheDocument()
    expect(screen.getByText('gpt-4o-mini')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Hide openai models' }))
    expect(screen.queryByText('gpt-4o-mini')).not.toBeInTheDocument()
  })

  it('counts the whole catalog beside what is connected and configured', async () => {
    arm()
    render(<ProvidersPage />)

    await providerCard('openai')
    const summary = within(screen.getByRole('region', { name: 'Provider summary' }))
    const tile = (label: string) => summary.getByText(label).parentElement
    expect(tile('Providers')).toHaveTextContent('6')
    expect(tile('Connected')).toHaveTextContent('1')
    expect(tile('Configured')).toHaveTextContent('4')
    expect(tile('Unavailable')).toHaveTextContent('1')
    // openai advertises two, anthropic one; the rest advertise nothing live.
    expect(tile('Models advertised')).toHaveTextContent('3')
  })

  it('reloads both tabs, so the control is not silently inert on the one showing', async () => {
    arm()
    render(<ProvidersPage />)
    await providerCard('openai')
    const before = request.mock.calls.filter((call: unknown[]) => call[0] === '/v1/capabilities').length

    await userEvent.click(screen.getByRole('button', { name: 'Refresh providers' }))

    await waitFor(() => {
      const after = request.mock.calls.filter((call: unknown[]) => call[0] === '/v1/capabilities').length
      expect(after).toBeGreaterThan(before)
    })
    expect(request.mock.calls.filter((call: unknown[]) => call[0] === '/admin/providers/catalog').length).toBeGreaterThan(1)
  })
})

describe('ProvidersPage — capabilities', () => {
  it('defaults every absent cell to forward, so an empty profile forwards everything', async () => {
    arm()
    render(<ProvidersPage />)
    await openCapabilitiesTab()

    const openai = capabilitiesRow('openai')
    expect(within(openai).getAllByText('forward')).toHaveLength(2)
    expect(within(openai).queryByText('unsupported')).not.toBeInTheDocument()

    const gemini = capabilitiesRow('gemini')
    expect(within(gemini).getByText('translate')).toBeInTheDocument()
    expect(within(gemini).getByText('unsupported')).toBeInTheDocument()
    expect(within(gemini).queryByText('forward')).not.toBeInTheDocument()
  })

  it('narrows to one provider without dropping any parameter column', async () => {
    arm()
    render(<ProvidersPage />)
    await openCapabilitiesTab()

    await userEvent.type(screen.getByPlaceholderText(/filter by provider or parameter/i), 'gemini')

    await waitFor(() => expect(screen.queryByRole('rowheader', { name: 'openai' })).not.toBeInTheDocument())
    expect(screen.getByRole('rowheader', { name: 'gemini' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'logprobs' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'response_format' })).toBeInTheDocument()
  })

  it('surfaces a capabilities failure without disturbing the providers tab', async () => {
    arm({ capabilities: new Error('capabilities are unavailable') })
    render(<ProvidersPage />)

    await providerCard('openai')
    await userEvent.click(screen.getByRole('tab', { name: /capabilities/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent('capabilities are unavailable')
  })
})
