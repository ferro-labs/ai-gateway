import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, request } from '../lib/api'
import type * as ApiModule from '../lib/api'
import type { APIKey, LogsResponse, RequestLog } from '../types'
import LogsPage from './LogsPage'

vi.mock('../auth/AuthProvider', () => ({ useAuth: () => ({ isAdmin: true }) }))

vi.mock('../lib/api', async (importOriginal) => ({
  // `ApiError` stays real: the page decides a 501 is a deployment choice with
  // an `instanceof` check, which a stubbed class would silently fail.
  ...(await importOriginal<typeof ApiModule>()),
  request: vi.fn(),
}))

const requestMock = vi.mocked(request)

function log(overrides: Partial<RequestLog> = {}): RequestLog {
  return {
    trace_id: 'trace-abc',
    stage: 'after_request',
    model: 'gpt-4o-mini',
    provider: 'openai',
    prompt_tokens: 40,
    completion_tokens: 10,
    total_tokens: 50,
    error_message: '',
    created_at: '2026-07-23T10:00:00Z',
    ...overrides,
  }
}

function logsResponse(rows: RequestLog[], total = rows.length): LogsResponse {
  return { data: rows, summary: { total_entries: total, returned_entries: rows.length }, filters: {} }
}

function apiKey(overrides: Partial<APIKey> = {}): APIKey {
  return {
    id: 'key-ops',
    key: 'fgw_••••abcd',
    name: 'Ops laptop',
    scopes: ['admin'],
    created_at: '2026-07-01T09:00:00Z',
    usage_count: 3,
    active: true,
    ...overrides,
  }
}

/**
 * Routes `/admin/keys` to the key list and everything else to `rest`.
 *
 * The page issues two independent requests: the page of logs, and the key list
 * it resolves recorded credential ids to names through. A mock answering both
 * with one body would hand the page a logs envelope where it expects an array
 * of keys.
 */
function route(rest: (path: string, init?: RequestInit) => Promise<unknown>, keys: APIKey[] = []) {
  requestMock.mockImplementation((path: string, init?: RequestInit) =>
    String(path).startsWith('/admin/keys')
      ? Promise.resolve(keys)
      : rest(String(path), init))
}

/** Answers every log request with `body`, and the key list with `keys`. */
function answerLogs(body: unknown, keys: APIKey[] = []) {
  route(() => Promise.resolve(body), keys)
}

/** Surfaces the query string so URL state can be asserted, not inferred. */
function LocationProbe() {
  const { search } = useLocation()
  return <span data-testid="query-string">{search}</span>
}

function renderPage(entry = '/logs') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route
          element={
            <>
              <LogsPage />
              <LocationProbe />
            </>
          }
          path="/logs"
        />
      </Routes>
    </MemoryRouter>,
  )
}

/** The request paths the page issued, in order. */
function paths(): string[] {
  return requestMock.mock.calls.map((call) => String(call[0]))
}

beforeEach(() => {
  requestMock.mockReset()
  answerLogs(logsResponse([log()]))
})


describe('LogsPage', () => {
  it('reports prompt and completion tokens separately', async () => {
    renderPage()

    const input = await screen.findByText('40')
    const output = screen.getByText('10')
    // A single total would have hidden which of the two moved, and the
    // `data-label` is the column name a phone prints in place of the header.
    expect(input).toHaveAttribute('data-label', 'Tokens in')
    expect(output).toHaveAttribute('data-label', 'Tokens out')
    expect(screen.getByRole('columnheader', { name: 'Tokens in' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Tokens out' })).toBeInTheDocument()
  })

  it('builds the query from the URL, so a filtered view survives a reload', async () => {
    renderPage('/logs?provider=openai&stage=on_error&hours=1&offset=50')

    await waitFor(() => expect(requestMock).toHaveBeenCalled())
    const [path] = paths()
    expect(path).toContain('provider=openai')
    expect(path).toContain('stage=on_error')
    expect(path).toContain('offset=50')
  })

  it('ignores a stage the gateway does not have, which would return nothing and look like an idle gateway', async () => {
    renderPage('/logs?stage=made_up')

    await waitFor(() => expect(requestMock).toHaveBeenCalled())
    expect(paths()[0]).not.toContain('stage=')
  })

  it('forwards stage=all, which is how the raw logged events are asked for', async () => {
    // Not a stage: the gateway lists one row per request unless told otherwise,
    // and this is the way back to every row a request wrote. Dropping it from
    // the accepted set would silently fall back to the default and look like
    // the option had simply stopped working.
    renderPage('/logs?stage=all')

    await waitFor(() => expect(requestMock).toHaveBeenCalled())
    expect(paths()[0]).toContain('stage=all')
  })

  it('writes an applied filter back to the URL and returns to the first page', async () => {
    const user = userEvent.setup()
    renderPage('/logs?provider=openai&offset=50')
    await screen.findByText('40')

    await user.clear(screen.getByPlaceholderText('Provider'))
    await user.type(screen.getByPlaceholderText('Provider'), 'groq')
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    const search = screen.getByTestId('query-string').textContent ?? ''
    expect(search).toContain('provider=groq')
    // Staying on page 3 of the previous filter's results would show an empty
    // page for a filter that has matches.
    expect(search).not.toContain('offset=')
  })

  it('deletes before the chosen date, narrowed to the filters the operator opted into', async () => {
    const user = userEvent.setup()
    route((path) => Promise.resolve(path.startsWith('/admin/logs?before=') ? { deleted: 12 } : logsResponse([log()])))
    renderPage('/logs?provider=openai')
    await screen.findByText('40')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    const dateInput = await screen.findByLabelText('Delete entries before')
    await user.clear(dateInput)
    await user.type(dateInput, '2026-07-01')
    await user.click(screen.getByLabelText(/Limit the deletion to the current filters/))
    // The dialog must name what is about to happen — it cannot be undone.
    expect(screen.getByText(/limited to provider openai/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Delete logs' }))

    await waitFor(() => expect(paths().some((path) => path.includes('before='))).toBe(true))
    const purge = paths().find((path) => path.includes('before=')) ?? ''
    const params = new URLSearchParams(purge.slice(purge.indexOf('?') + 1))
    // Local midnight, not a UTC slice: the date picked is the operator's date.
    expect(params.get('before')).toBe(new Date('2026-07-01T00:00:00').toISOString())
    expect(params.get('provider')).toBe('openai')
    expect(await screen.findByText(/12 request-log entries before/)).toBeInTheDocument()
  })

  it('purges everything when the filters are not opted into', async () => {
    const user = userEvent.setup()
    route((path) => Promise.resolve(path.startsWith('/admin/logs?before=') ? { deleted: 0 } : logsResponse([log()])))
    renderPage('/logs?provider=openai')
    await screen.findByText('40')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    await screen.findByLabelText('Delete entries before')
    expect(screen.getByText(/across every provider, model, and stage/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Delete logs' }))

    await waitFor(() => expect(paths().some((path) => path.includes('before='))).toBe(true))
    const purge = paths().find((path) => path.includes('before=')) ?? ''
    expect(purge).not.toContain('provider=')
  })

  it('refuses a cutoff in the future, which would delete the incident being investigated', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('40')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    const dateInput = await screen.findByLabelText('Delete entries before')
    const today = new Date()
    const pad = (value: number) => String(value).padStart(2, '0')
    expect(dateInput).toHaveAttribute('max', `${today.getFullYear()}-${pad(today.getMonth() + 1)}-${pad(today.getDate())}`)

    await user.clear(dateInput)
    await user.type(dateInput, '2099-01-01')
    // Submitted directly rather than through the button: `max` makes the form
    // invalid, so a click never reaches the handler, and the handler is exactly
    // what has to hold when a value arrives past the picker's guard.
    fireEvent.submit(screen.getByRole<HTMLButtonElement>('button', { name: 'Delete logs' }).form as HTMLFormElement)

    expect(await screen.findByRole('alert')).toHaveTextContent('no later than today')
    expect(paths().some((path) => path.includes('before='))).toBe(false)
  })

  it('explains a 501 instead of reporting a fault, and hides the delete control', async () => {
    route(() => Promise.reject(new ApiError('request logging is not configured', 501)))
    renderPage()

    expect(await screen.findByText('Request logging is disabled')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('button', { name: /Delete old logs/ })).toBeNull()
  })

  it('reports a store that is configured but broken as a failure', async () => {
    // Only a 501 is a deployment choice. A 500 is something to go and fix, and
    // dressing it as "logging is disabled" sends the operator nowhere.
    route(() => Promise.reject(new ApiError('the request log store is unreachable', 500)))
    renderPage()

    expect(await screen.findByRole('alert')).toHaveTextContent('the request log store is unreachable')
    expect(screen.queryByText('Request logging is disabled')).toBeNull()
  })

  it('says the gateway recorded nothing, rather than showing a bare table header', async () => {
    answerLogs(logsResponse([], 0))
    renderPage()

    expect(await screen.findByText('No request logs')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Next' })).toBeNull()
  })

  it('keeps the pagination when a search matches nothing on the page it is on', async () => {
    const user = userEvent.setup()
    const rows = Array.from({ length: 50 }, (_, index) => log({ trace_id: `trace-${index}` }))
    answerLogs(logsResponse(rows, 120))
    renderPage('/logs?offset=50&q=zzzz')

    // The rows the server sent decide whether the table and its controls exist;
    // the search only decides which of them are drawn. Gating on the filtered
    // rows stranded an operator on page 3 with no way back to page 1.
    expect(await screen.findByText('No matches on this page')).toBeInTheDocument()
    expect(screen.getByText(/Showing 51–100 of 120/)).toBeInTheDocument()
    expect(screen.getByText(/0 match the search on this page/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Previous' }))

    await waitFor(() => expect(paths().some((path) => path.includes('offset=0'))).toBe(true))
    // The search survives the page turn: it is what the operator is looking for.
    expect(screen.getByTestId('query-string')).toHaveTextContent('q=zzzz')
  })

  it('narrows the rows on screen without asking the gateway for them again', async () => {
    const rows = [log({ trace_id: 'trace-keep', provider: 'openai' }), log({ trace_id: 'trace-drop', provider: 'groq' })]
    answerLogs(logsResponse(rows))
    renderPage('/logs?q=groq')

    expect(await screen.findByText('trace-drop')).toBeInTheDocument()
    expect(screen.queryByText('trace-keep')).toBeNull()
    // A request per keystroke would cost the gateway a query for a filter it
    // never sees, so `q` is deliberately absent from every path issued.
    expect(paths().every((path) => !path.includes('q='))).toBe(true)
  })

  it('renders a row that never reached a provider without inventing values for it', async () => {
    answerLogs(logsResponse([log({ trace_id: '', provider: '', model: '', stage: '', error_message: 'no provider available' })]))
    renderPage()

    const row = (await screen.findByText('no provider available')).closest('tr')
    expect(row).not.toBeNull()
    // Five: provider, model, stage, trace id — and the API key, which a row
    // rejected before it reached a provider has no more of than the rest.
    expect(within(row as HTMLElement).getAllByText('—')).toHaveLength(5)
    expect(within(row as HTMLElement).queryByRole('button', { name: /Copy trace ID/ })).toBeNull()
  })

  it('keeps a page-local search in the URL beside the filters the gateway answers', async () => {
    const user = userEvent.setup()
    const rows = Array.from({ length: 50 }, (_, index) => log({ trace_id: `trace-${index}` }))
    answerLogs(logsResponse(rows, 120))
    renderPage()
    await screen.findByText('trace-0')

    await user.type(screen.getByPlaceholderText('Search trace, provider, model, or error'), 'trace-4')
    await user.type(screen.getByPlaceholderText('Model'), 'gpt-4o-mini')
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    const search = screen.getByTestId('query-string').textContent ?? ''
    // Both live in the URL so the view survives a reload, but only the model
    // reaches the gateway — the search runs over the rows already on screen.
    expect(search).toContain('q=trace-4')
    expect(search).toContain('model=gpt-4o-mini')
    await waitFor(() => expect(paths().some((path) => path.includes('model=gpt-4o-mini'))).toBe(true))

    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(screen.getByTestId('query-string').textContent).toContain('offset=50')
  })

  it('sends a chosen stage and range to the gateway', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('40')

    await user.click(screen.getByRole('combobox', { name: 'Stage' }))
    await user.click(await screen.findByRole('option', { name: 'On error' }))
    await user.click(screen.getByRole('combobox', { name: 'Time range' }))
    await user.click(await screen.findByRole('option', { name: 'Last hour' }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(paths().some((path) => path.includes('stage=on_error'))).toBe(true))
    const search = screen.getByTestId('query-string').textContent ?? ''
    expect(search).toContain('stage=on_error')
    expect(search).toContain('hours=1')
  })
})

describe('LogsPage — request attribution', () => {
  it('names the key that served a request rather than printing its id', async () => {
    // The whole point of the column: a uuid an operator cannot map to a person
    // answers "which key spent this" no better than no column at all.
    answerLogs(logsResponse([log({ api_key_id: 'key-ops' })]), [apiKey()])
    renderPage()

    expect(await screen.findByText('Ops laptop')).toBeInTheDocument()
    expect(screen.queryByText('key-ops')).toBeNull()
    expect(screen.getByRole('columnheader', { name: 'API key' })).toBeInTheDocument()
  })

  it('says a key was revoked, which is when its past traffic is most often read', async () => {
    answerLogs(
      logsResponse([log({ api_key_id: 'key-ops' })]),
      [apiKey({ revoked_at: '2026-07-22T08:00:00Z', active: false })],
    )
    renderPage()

    expect(await screen.findByText('Ops laptop')).toBeInTheDocument()
    expect(screen.getByText('revoked')).toBeInTheDocument()
  })

  it('shows the recorded id when the key store can no longer name it', async () => {
    // A deleted key, or the synthetic id the master credential carries. The row
    // still records who served it, so the id is shown rather than dropped —
    // and no claim is made about what became of the key.
    answerLogs(logsResponse([log({ api_key_id: 'master-key:9f3c1a2b' })]), [apiKey()])
    renderPage()

    expect(await screen.findByText('master-key:9f3c1a2b')).toBeInTheDocument()
  })

  it('shows no key for a request that carried no credential', async () => {
    answerLogs(logsResponse([log({ api_key_id: '' })]), [apiKey()])
    renderPage()

    const row = (await screen.findByText('gpt-4o-mini')).closest('tr') as HTMLElement
    expect(within(row).queryByText('Ops laptop')).toBeNull()
    // Shown as absent rather than attributed to anyone: a gateway without key
    // auth records every row this way, as does a row written before the
    // gateway recorded credentials at all.
    expect(within(row).getByText('—').closest('td')).toHaveAttribute('data-label', 'API key')
  })

  it('falls back to the recorded ids when the key list cannot be loaded', async () => {
    // A key list that fails costs the name and nothing else. Raising it as a
    // failure banner over a request log that loaded fine would send an operator
    // after the wrong thing.
    requestMock.mockImplementation((path: string) =>
      String(path).startsWith('/admin/keys')
        ? Promise.reject(new ApiError('key store unreachable', 500))
        : Promise.resolve(logsResponse([log({ api_key_id: 'key-ops' })])))
    renderPage()

    expect(await screen.findByText('key-ops')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('sends the chosen key to the gateway and keeps it in the URL', async () => {
    const user = userEvent.setup()
    answerLogs(logsResponse([log({ api_key_id: 'key-ops' })]), [apiKey()])
    renderPage()
    await screen.findByText('Ops laptop')

    await user.click(screen.getByRole('combobox', { name: 'API key' }))
    await user.click(await screen.findByRole('option', { name: 'Ops laptop' }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(paths().some((path) => path.includes('api_key_id=key-ops'))).toBe(true))
    // In the URL as well as in the request, so one key's traffic is a link an
    // operator can paste into an incident channel.
    expect(screen.getByTestId('query-string').textContent).toContain('api_key_id=key-ops')
  })

  it('asks for the unattributed rows by the reserved value', async () => {
    const user = userEvent.setup()
    answerLogs(logsResponse([log({ api_key_id: '' })]), [apiKey()])
    renderPage()
    await screen.findByText('40')

    await user.click(screen.getByRole('combobox', { name: 'API key' }))
    await user.click(await screen.findByRole('option', { name: 'No API key' }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))

    // A blank value is how "no filter" is spelled, so the rows naming no
    // credential need a value of their own to be reachable at all.
    await waitFor(() => expect(paths().some((path) => path.includes('api_key_id=none'))).toBe(true))
  })

  it('names the selected stage and time range in their controls', async () => {
    // Same defect as the API key control below, on the two beside it: a select
    // trigger renders its raw value unless told otherwise, so these read
    // "on_error" and "1" while the open lists read "On error" and "Last hour".
    answerLogs(logsResponse([log()]), [apiKey()])
    renderPage('/logs?stage=on_error&hours=1')

    const stage = await screen.findByRole('combobox', { name: 'Stage' })
    await waitFor(() => expect(stage).toHaveTextContent('On error'))
    expect(stage).not.toHaveTextContent('on_error')

    const range = await screen.findByRole('combobox', { name: 'Time range' })
    await waitFor(() => expect(range).toHaveTextContent('Last hour'))
  })

  it('does not narrow a purge to the all-stages sentinel', async () => {
    // `all` is the listing's "every stage" value, not a stage any row carries.
    // Offering it as a purge scope promised a narrowed deletion that matched
    // nothing; with it excluded there is no scope left to offer.
    const user = userEvent.setup()
    answerLogs(logsResponse([log()]), [apiKey()])
    renderPage('/logs?stage=all')

    await screen.findByRole('combobox', { name: 'Stage' })
    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    await screen.findByRole('button', { name: 'Delete logs' })

    expect(screen.queryByText(/Limit the deletion/)).not.toBeInTheDocument()
  })

  it('names the filtered key in the control rather than showing its id', async () => {
    // The control's values are ids, and a select trigger shows its value unless
    // told otherwise — which would put a uuid in the one place an operator is
    // choosing a person.
    answerLogs(logsResponse([log({ api_key_id: 'key-ops' })]), [apiKey()])
    renderPage('/logs?api_key_id=key-ops')

    const control = await screen.findByRole('combobox', { name: 'API key' })
    await waitFor(() => expect(control).toHaveTextContent('Ops laptop'))
    expect(control).not.toHaveTextContent('key-ops')
  })

  it('keeps a filter on a key the gateway no longer holds', async () => {
    // A deleted key's rows outlive it. Dropping the id — the way an unknown
    // stage is dropped — would silently widen the listing to every key.
    answerLogs(logsResponse([log({ api_key_id: 'key-gone' })]), [apiKey()])
    renderPage('/logs?api_key_id=key-gone')

    await waitFor(() => expect(paths()[0]).toContain('api_key_id=key-gone'))
    // No name to show, so the recorded id stands in for one. A blank control
    // beside a narrowed table would read as "all keys".
    expect(await screen.findByRole('combobox', { name: 'API key' })).toHaveTextContent('key-gone')
  })
})

describe('LogsPage — purge dialog', () => {
  it('closes on Cancel without deleting anything', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('40')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    await screen.findByLabelText('Delete entries before')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(paths().some((path) => path.includes('before='))).toBe(false)
  })

  it('refuses an empty cutoff rather than deleting from the beginning of time', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('40')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    const dateInput = await screen.findByLabelText('Delete entries before')
    await user.clear(dateInput)
    expect(screen.getByText('Choose a date to see what will be deleted.')).toBeInTheDocument()
    // Submitted directly: `required` stops the button, and the handler is what
    // has to hold when a value arrives past the picker's guard.
    fireEvent.submit(screen.getByRole<HTMLButtonElement>('button', { name: 'Delete logs' }).form as HTMLFormElement)

    expect(await screen.findByRole('alert')).toHaveTextContent('Choose the date to delete entries before.')
    expect(paths().some((path) => path.includes('before='))).toBe(false)
  })

  it('reports a refused deletion inside the dialog rather than behind it', async () => {
    const user = userEvent.setup()
    route((_path, init) =>
      init?.method === 'DELETE'
        ? Promise.reject(new ApiError('the request log store is read-only', 500))
        : Promise.resolve(logsResponse([log()])))
    renderPage()
    await screen.findByText('40')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    await screen.findByLabelText('Delete entries before')
    await user.click(screen.getByRole('button', { name: 'Delete logs' }))

    const dialog = await screen.findByRole('dialog')
    // The dialog stays open: a notice rendered by the page sits under the
    // backdrop where it cannot be read.
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('the request log store is read-only')
  })

  it('stays open while the deletion is in flight, so its outcome has somewhere to go', async () => {
    const user = userEvent.setup()
    let settle: (value: { deleted: number }) => void = () => {}
    route((_path, init) =>
      init?.method === 'DELETE'
        ? new Promise<{ deleted: number }>((resolve) => { settle = resolve })
        : Promise.resolve(logsResponse([log()])))
    renderPage()
    await screen.findByText('40')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    await screen.findByLabelText('Delete entries before')
    await user.click(screen.getByRole('button', { name: 'Delete logs' }))

    // Escape while a permanent deletion is running would leave the operator
    // with no report of what it did.
    await screen.findByRole('button', { name: 'Deleting…' })
    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    settle({ deleted: 3 })
    expect(await screen.findByText(/3 request-log entries before/)).toBeInTheDocument()
  })

  it('returns to the first page after deleting from a later one', async () => {
    const user = userEvent.setup()
    const rows = Array.from({ length: 50 }, (_, index) => log({ trace_id: `trace-${index}` }))
    route((_path, init) => Promise.resolve(init?.method === 'DELETE' ? { deleted: 400 } : logsResponse(rows, 120)))
    renderPage('/logs?offset=50')
    await screen.findByText('trace-0')

    await user.click(screen.getByRole('button', { name: /Delete old logs/ }))
    await screen.findByLabelText('Delete entries before')
    await user.click(screen.getByRole('button', { name: 'Delete logs' }))

    // Page 2 of a log that just lost 400 entries is usually past the end of it.
    await waitFor(() => expect(screen.getByTestId('query-string').textContent).not.toContain('offset='))
    expect(await screen.findByText(/400 request-log entries before/)).toBeInTheDocument()
  })
})
