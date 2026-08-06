import { vi } from 'vitest'

/**
 * A real `Storage`, for suites that touch one.
 *
 * Node ships a global `localStorage` that shadows jsdom's and, with no backing
 * file configured, has none of the methods — `localStorage.clear is not a
 * function`. jsdom also serves the suite from an opaque origin, where both
 * Web Storage areas are inert. Stubbing this in makes storage behave.
 */
export function memoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() { return values.size },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => { values.delete(key) },
    setItem: (key, value) => { values.set(key, value) },
  }
}

/** The operating system preference a test chose, and a way to change it. */
export interface FakeMedia {
  matches: boolean
  /** Notifies every subscriber that `matches` changed. */
  emit: () => void
}

/**
 * Answers `matchMedia` with a preference the test controls.
 *
 * jsdom implements no `matchMedia` at all, and the real one cannot be told to
 * change, so "the operating system prefers dark" is otherwise untestable.
 */
export function stubMatchMedia(dark = false): FakeMedia {
  const listeners: Array<() => void> = []
  const media: FakeMedia = {
    matches: dark,
    emit: () => listeners.forEach((listener) => listener()),
  }
  vi.stubGlobal('matchMedia', vi.fn((query: string) => ({
    get matches() { return media.matches },
    media: query,
    onchange: null,
    addEventListener: (_event: string, listener: () => void) => { listeners.push(listener) },
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })))
  return media
}

/** One request the dashboard made, as the gateway would have received it. */
export interface Call {
  url: string
  method: string
  body: string
  authorization: string
}

export interface Reply {
  status?: number
  body?: unknown
}

/**
 * Stubs the gateway at `fetch`, so the real request builder, the real session
 * handling and the real query strings stay under test, and records every call.
 */
export function stubGateway(route: (call: Call) => Reply | Promise<Reply>): Call[] {
  const calls: Call[] = []
  // `input` is a string, not a RequestInfo: the api module builds every URL
  // from a relative path and refuses anything else before it reaches fetch.
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const call: Call = {
      url: input,
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? init.body : '',
      authorization: new Headers(init?.headers).get('Authorization') ?? '',
    }
    calls.push(call)
    const reply = await route(call)
    const status = reply.status ?? 200
    return new Response(status === 204 ? null : JSON.stringify(reply.body ?? {}), {
      status,
      headers: { 'Content-Type': 'application/json' },
    })
  }))
  return calls
}
