import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../lib/api'
import type * as ApiModule from '../lib/api'
import ConfigPage from './ConfigPage'

const request = vi.hoisted(() => vi.fn())
const isAdmin = vi.hoisted(() => ({ value: true }))

vi.mock('../lib/api', async (importOriginal) => ({
  ...(await importOriginal<typeof ApiModule>()),
  request,
}))

vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({ isAdmin: isAdmin.value }),
}))

const activeConfig = {
  strategy: { mode: 'fallback' },
  targets: [{ virtual_key: 'openai' }],
  plugins: [
    // One plugin, two entries: response-cache branches on the stage, so this is
    // the required configuration rather than a duplicated block.
    { name: 'response-cache', type: 'cache', stage: 'before_request', enabled: true, config: { ttl_seconds: 60, redis_url: '[REDACTED]' } },
    { name: 'response-cache', type: 'cache', stage: 'after_request', enabled: true, config: { ttl_seconds: 60, redis_url: '[REDACTED]' } },
    { name: 'word-filter', type: 'guardrail', stage: 'before_request', enabled: false, config: { blocked_words: ['[REDACTED]'] } },
  ],
}

const olderConfig = {
  strategy: { mode: 'single' },
  targets: [{ virtual_key: 'openai' }],
}

const history = {
  data: [
    { version: 1, updated_at: '2026-07-20T10:00:00Z', config: olderConfig, actor: 'Ops laptop (key-7f2)' },
    { version: 2, updated_at: '2026-07-21T10:00:00Z', config: activeConfig },
  ],
  summary: { total_versions: 2 },
}

/** Answers the two loads the page issues; every mutation is opt-in per test. */
function respondToLoads(mutations: (path: string, init?: RequestInit) => Promise<unknown> = () => Promise.reject(new Error('unexpected call'))) {
  request.mockImplementation((path: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET' && path === '/admin/config') return Promise.resolve(structuredClone(activeConfig))
    if (method === 'GET' && path === '/admin/config/history') return Promise.resolve(structuredClone(history))
    return mutations(path, init)
  })
}

/** Serves one specific document, for the tests that are about its contents. */
function respondWithConfig(current: Record<string, unknown>) {
  request.mockImplementation((path: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET' && path === '/admin/config') return Promise.resolve(structuredClone(current))
    if (method === 'GET' && path === '/admin/config/history') return Promise.resolve({ data: [], summary: { total_versions: 0 } })
    return Promise.reject(new Error('unexpected call'))
  })
}

/**
 * The read-only viewer, by its accessible name.
 *
 * Its text is split across a span per syntax token, so a `getByText` for a
 * fragment of the document finds nothing — the assertion has to run against the
 * panel's whole text rather than against one node.
 */
function viewer(): HTMLElement {
  return screen.getByLabelText('Active gateway configuration')
}

/**
 * The card rendered for one plugin.
 *
 * A list item takes no accessible name from its content, so the plugin's
 * heading is the way in.
 */
function pluginCard(name: RegExp): HTMLElement {
  const card = screen.getByRole('heading', { name }).closest('li')
  if (!card) throw new Error(`no plugin card matching ${String(name)}`)
  return card
}

async function renderPage() {
  render(<ConfigPage />)
  await screen.findByRole('tab', { name: /current config/i })
}

/**
 * Opens the JSON editor and hands back the textarea.
 *
 * Its value is set with `fireEvent.change` rather than typed: a configuration
 * document is mostly braces, and `userEvent.type` reads `{` as the start of a
 * key descriptor. Nothing here is about keystrokes.
 */
async function openEditor(): Promise<HTMLTextAreaElement> {
  await renderPage()
  await userEvent.click(screen.getByRole('button', { name: 'Edit JSON' }))
  return screen.getByLabelText<HTMLTextAreaElement>('Gateway configuration JSON')
}

const saveButton = () => screen.getByRole('button', { name: /save and apply/i })

beforeEach(() => {
  isAdmin.value = true
  request.mockReset()
  respondToLoads()
})


describe('ConfigPage save', () => {
  it('sends the edited document and confirms the gateway applied it', async () => {
    const applied: string[] = []
    respondToLoads((path, init) => {
      if (init?.method === 'PUT' && path === '/admin/config') {
        applied.push(typeof init.body === 'string' ? init.body : '')
        return Promise.resolve({ status: 'ok' })
      }
      return Promise.reject(new Error('unexpected call'))
    })
    const editor = await openEditor()

    fireEvent.change(editor, { target: { value: JSON.stringify({ strategy: { mode: 'loadbalance' }, targets: [{ virtual_key: 'openai' }] }, null, 2) } })
    await userEvent.click(saveButton())

    expect(await screen.findByText('Configuration saved and applied.')).toBeInTheDocument()
    expect(JSON.parse(applied[0] ?? '{}')).toEqual({ strategy: { mode: 'loadbalance' }, targets: [{ virtual_key: 'openai' }] })
    // Back to the read-only view: leaving the textarea open would invite a
    // second Save of a document the gateway has already moved past.
    expect(screen.getByRole('button', { name: 'Edit JSON' })).toBeInTheDocument()
  })

  it('refuses malformed JSON here rather than sending it for the gateway to reject', async () => {
    const editor = await openEditor()

    fireEvent.change(editor, { target: { value: '{ "strategy": ' } })
    await userEvent.click(saveButton())

    expect(await screen.findByRole('alert')).toHaveTextContent(/JSON/)
    // respondToLoads rejects anything that is not one of the two reads, so a
    // PUT would have surfaced as an unexpected-call failure. The editor stays
    // open with the text that needs fixing still in it.
    expect(screen.getByLabelText('Gateway configuration JSON')).toHaveValue('{ "strategy": ')
  })

  it('refuses a document that parses but is not a configuration object', async () => {
    const editor = await openEditor()

    // Valid JSON, and the shape the gateway's decoder would reject after the
    // round trip — an array of targets pasted in place of the whole document.
    fireEvent.change(editor, { target: { value: '[{"virtual_key": "openai"}]' } })
    await userEvent.click(saveButton())

    expect(await screen.findByRole('alert')).toHaveTextContent('Configuration must be a JSON object.')
  })

  it('names every field still holding the placeholder the last read wrote, and keeps Save out of reach', async () => {
    const editor = await openEditor()

    // One of each path the guard knows, because a saved "[REDACTED]" is not a
    // failed save: it is a tracing header, an MCP token or a plugin secret
    // replaced with the literal string, i.e. an outage the operator caused.
    fireEvent.change(editor, {
      target: {
        value: JSON.stringify({
          plugins: [{ name: 'budget', config: { api_key: '[REDACTED]' } }],
          mcp_servers: [{ name: 'search', headers: { Authorization: '[REDACTED]' }, env: { TOKEN: '[REDACTED]' } }],
          observability: {
            tracing: { headers: { 'x-api-key': '[REDACTED]' } },
            exporters: [{ name: 'langsmith', config: { api_key: '[REDACTED]' } }],
          },
        }),
      },
    })

    const warning = await screen.findByRole('status')
    for (const field of [
      'observability.tracing.headers',
      'mcp_servers[0].headers',
      'mcp_servers[0].env',
      'observability.exporters[0].config',
      'plugins[0].config',
    ]) {
      expect(warning).toHaveTextContent(field)
    }
    expect(saveButton()).toBeDisabled()

    // A `${VAR}` reference is the answer the message asks for, and it clears
    // the guard — the gateway resolves it at construction, so the document
    // never carries the secret itself.
    fireEvent.change(editor, {
      target: { value: JSON.stringify({ mcp_servers: [{ name: 'search', headers: { Authorization: 'Bearer ${SEARCH_TOKEN}' } }] }) },
    })
    await waitFor(() => expect(saveButton()).toBeEnabled())
  })
})

describe('ConfigPage unsaved edits', () => {
  it('asks before a refresh throws away an in-progress edit', async () => {
    const editor = await openEditor()
    fireEvent.change(editor, { target: { value: '{"strategy": {"mode": "single"}}' } })

    await userEvent.click(screen.getByRole('button', { name: 'Refresh configuration' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(dialog).toHaveTextContent('Discard unsaved changes?')

    // Backing out of the question keeps the work: an operator who reaches for
    // Refresh out of habit has not asked to lose anything.
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.getByLabelText('Gateway configuration JSON')).toHaveValue('{"strategy": {"mode": "single"}}'))

    await userEvent.click(screen.getByRole('button', { name: 'Refresh configuration' }))
    const second = await screen.findByRole('alertdialog')
    await userEvent.click(within(second).getByRole('button', { name: 'Discard and refresh' }))

    await waitFor(() => expect(screen.queryByLabelText('Gateway configuration JSON')).toBeNull())
    expect(viewer()).toHaveTextContent(/"mode": "fallback"/)
  })

  it('replaces what the gateway shows on a reload, not what the operator has open', async () => {
    let served: Record<string, unknown> = activeConfig
    let loads = 0
    request.mockImplementation((path: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      if (method === 'GET' && path === '/admin/config') {
        loads++
        return Promise.resolve(structuredClone(served))
      }
      if (method === 'GET' && path === '/admin/config/history') return Promise.resolve(structuredClone(history))
      return Promise.reject(new Error('unexpected call'))
    })
    const editor = await openEditor()
    expect(editor.value).toContain('"mode": "fallback"')

    // Somebody else applied a change while this editor was open.
    served = { ...activeConfig, strategy: { mode: 'loadbalance' } }
    await userEvent.click(screen.getByRole('button', { name: 'Refresh configuration' }))
    await waitFor(() => expect(loads).toBe(2))

    // Reseeding the textarea from a load is how an edit gets silently thrown
    // away, so the document under the cursor is left exactly as it was.
    expect(screen.getByLabelText<HTMLTextAreaElement>('Gateway configuration JSON').value).toContain('"mode": "fallback"')

    // The load did land, though: closing the editor shows what the gateway is
    // actually running now.
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(viewer()).toHaveTextContent(/"mode": "loadbalance"/)
  })
})

describe('ConfigPage history attribution', () => {
  it('names the credential that applied each version', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /history/i }))

    expect(screen.getByRole('columnheader', { name: 'Actor' })).toBeInTheDocument()
    expect(screen.getByText('Ops laptop (key-7f2)')).toBeInTheDocument()
    // An entry written before actors were recorded says so; a bare dash would
    // read as "nobody" rather than "not known".
    expect(screen.getByText('Not recorded')).toBeInTheDocument()
  })
})

describe('ConfigPage version diff', () => {
  it('shows what rolling back to a version would change', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /history/i }))

    const compare = screen.getByRole('button', { name: /compare v1/i })
    expect(compare).toHaveAttribute('aria-expanded', 'false')
    await userEvent.click(compare)

    expect(compare).toHaveAttribute('aria-expanded', 'true')
    // v1 routes with the single strategy and configures no plugins; the active
    // config does both. Each line carries its direction as an off-screen word,
    // so the diff still reads correctly without colour.
    const lines = within(screen.getByRole('list'))
      .getAllByRole('listitem')
      .map((item) => item.textContent ?? '')
    expect(lines.some((line) => /^Removed: -\s+"mode": "fallback"/.test(line))).toBe(true)
    expect(lines.some((line) => /^Added: \+\s+"mode": "single"/.test(line))).toBe(true)
    expect(lines.some((line) => /^Removed: -\s+"name": "response-cache"/.test(line))).toBe(true)
  })

  it('says a version matches rather than rendering an empty diff', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /history/i }))
    await userEvent.click(screen.getByRole('button', { name: /compare v2/i }))

    expect(screen.getByText('Version 2 is identical to the active configuration.')).toBeInTheDocument()
  })

  it('names the sections a rollback changes in the confirmation', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /history/i }))
    const rows = screen.getAllByRole('row')
    const versionOneRow = rows.find((row) => within(row).queryByText('v1')) as HTMLElement
    await userEvent.click(within(versionOneRow).getByRole('button', { name: /rollback/i }))

    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/changes 2 sections: plugins, strategy/i)).toBeInTheDocument()
  })
})

describe('ConfigPage rollback', () => {
  it('restores a recorded version and names the one it restored', async () => {
    const rollbacks: string[] = []
    respondToLoads((path, init) => {
      if (init?.method === 'POST' && path.startsWith('/admin/config/rollback/')) {
        rollbacks.push(path)
        return Promise.resolve({ status: 'ok' })
      }
      return Promise.reject(new Error('unexpected call'))
    })
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /history/i }))
    const versionOneRow = screen.getAllByRole('row').find((row) => within(row).queryByText('v1')) as HTMLElement
    await userEvent.click(within(versionOneRow).getByRole('button', { name: /rollback/i }))

    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Rollback configuration' }))

    // Which version is now live is the only thing worth saying afterwards: the
    // history table renumbers, so "rolled back" alone answers nothing.
    expect(await screen.findByText('Configuration rolled back to version 1.')).toBeInTheDocument()
    expect(rollbacks).toEqual(['/admin/config/rollback/1'])
    expect(screen.queryByRole('alertdialog')).toBeNull()
  })

  it('leaves the active configuration alone when the operator backs out', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /history/i }))
    const versionOneRow = screen.getAllByRole('row').find((row) => within(row).queryByText('v1')) as HTMLElement
    await userEvent.click(within(versionOneRow).getByRole('button', { name: /rollback/i }))

    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))

    // respondToLoads rejects every call that is not one of the two reads, so a
    // POST issued here would have surfaced as a failure notice.
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('ConfigPage rollback failure', () => {
  it('reports a refused rollback inside the dialog rather than behind it', async () => {
    respondToLoads((path, init) => {
      if (init?.method === 'POST' && path === '/admin/config/rollback/1') {
        return Promise.reject(new ApiError('rollback target is not a valid configuration', 400))
      }
      return Promise.reject(new Error('unexpected call'))
    })
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /history/i }))
    const rows = screen.getAllByRole('row')
    const versionOneRow = rows.find((row) => within(row).queryByText('v1')) as HTMLElement
    await userEvent.click(within(versionOneRow).getByRole('button', { name: /rollback/i }))

    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Rollback configuration' }))

    // The dialog's backdrop covers the page, so the failure has to live inside
    // it. Asserting the containment, not just the text, is the whole point.
    await waitFor(() => expect(within(dialog).getByRole('alert')).toHaveTextContent('rollback target is not a valid configuration'))
  })
})

describe('ConfigPage plugins tab', () => {
  it('renders one row per plugin with every configured stage', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /plugins/i }))

    const cacheRow = pluginCard(/response-cache/i)
    // Two config entries, one row: two rows would read as a misconfiguration
    // when it is the setup a stage-branching plugin requires.
    expect(within(cacheRow).getByText('before_request')).toBeInTheDocument()
    expect(within(cacheRow).getByText('after_request')).toBeInTheDocument()
    expect(within(cacheRow).getByText('cache')).toBeInTheDocument()
    expect(within(cacheRow).getByText('Enabled')).toBeInTheDocument()
    // A number survives the Admin API's scrubbing; a string does not.
    expect(within(cacheRow).getByText('ttl_seconds: 60')).toBeInTheDocument()
    expect(within(cacheRow).getByText('redis_url: redacted')).toBeInTheDocument()

    const filterRow = pluginCard(/word-filter/i)
    expect(within(filterRow).getByText('Disabled')).toBeInTheDocument()
    expect(within(filterRow).getByText('guardrail')).toBeInTheDocument()
  })

  it('flags a stage-branching plugin whose entries disagree, instead of calling it enabled', async () => {
    respondWithConfig({
      plugins: [
        // request-logger checks before the request and records after it, so the
        // enabled entry and the disabled one resolve to different instances and
        // whatever the live stage recorded is silently dropped by the other.
        { name: 'request-logger', type: 'logging', stage: 'before_request', enabled: true, config: { persist: true } },
        { name: 'request-logger', type: 'logging', stage: 'after_request', enabled: false, config: { persist: true } },
      ],
    })
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /plugins/i }))

    const row = pluginCard(/request-logger/i)
    expect(within(row).getByText('Partly enabled')).toBeInTheDocument()
    expect(within(row).getByText('before_request')).toBeInTheDocument()
    expect(within(row).getByText('after_request')).toBeInTheDocument()
    // Still one row, still one settings list: the grouping is what makes the
    // disagreement legible instead of looking like two unrelated entries.
    expect(within(row).getByText('persist: true')).toBeInTheDocument()
  })

  it('says a gateway runs no middleware rather than showing an empty table', async () => {
    respondWithConfig({ strategy: { mode: 'single' }, targets: [{ virtual_key: 'openai' }] })
    await renderPage()
    await userEvent.click(screen.getByRole('tab', { name: /plugins/i }))

    expect(screen.getByText('No plugins configured')).toBeInTheDocument()
  })
})

describe('ConfigPage revert to startup', () => {
  it('restores the configuration loaded from disk and records it as a new version', async () => {
    const reverts: string[] = []
    respondToLoads((path, init) => {
      if (init?.method === 'DELETE' && path === '/admin/config') {
        reverts.push(path)
        return Promise.resolve({ status: 'ok' })
      }
      return Promise.reject(new Error('unexpected call'))
    })
    await renderPage()

    await userEvent.click(screen.getByRole('button', { name: /revert to startup configuration/i }))
    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revert configuration' }))

    // Not a silent restore: the startup document is not itself a rollback
    // target, so saying it was recorded is what tells the operator they can get
    // back to what was running a moment ago.
    expect(await screen.findByText(/running the configuration it loaded at startup/)).toBeInTheDocument()
    expect(reverts).toEqual(['/admin/config'])
    expect(screen.queryByRole('alertdialog')).toBeNull()
    // The control is still there: this deployment can reset, it just did.
    expect(screen.getByRole('button', { name: /revert to startup configuration/i })).toBeInTheDocument()
  })

  it('withdraws the control when the gateway cannot reset its configuration', async () => {
    respondToLoads((path, init) => {
      if (init?.method === 'DELETE' && path === '/admin/config') {
        return Promise.reject(new ApiError('config reset is not enabled', 501))
      }
      return Promise.reject(new Error('unexpected call'))
    })
    await renderPage()

    await userEvent.click(screen.getByRole('button', { name: /revert to startup configuration/i }))
    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revert configuration' }))

    // Not a red failure: a 501 here is a deployment choice, so the button goes
    // away and the page explains itself instead of offering one that can only fail.
    await waitFor(() => expect(screen.queryByRole('button', { name: /revert to startup configuration/i })).toBeNull())
    expect(screen.getByText(/cannot restore the startup configuration/i)).toBeInTheDocument()
  })

  it('hides the control from a read-only session', async () => {
    isAdmin.value = false
    await renderPage()
    expect(screen.queryByRole('button', { name: /revert to startup configuration/i })).toBeNull()
  })
})
