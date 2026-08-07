import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import PlaygroundPage from './PlaygroundPage'
import type { CatalogModelsResponse } from '../lib/playground'
import type { Capabilities } from '../types'

const { requestMock, rawRequestMock } = vi.hoisted(() => ({
  requestMock: vi.fn(),
  rawRequestMock: vi.fn(),
}))

vi.mock('../lib/api', () => ({ request: requestMock, rawRequest: rawRequestMock }))
vi.mock('../auth/AuthProvider', () => ({ useAuth: () => ({ isAdmin: true }) }))

const catalog: CatalogModelsResponse = {
  object: 'list',
  data: [
    { id: 'gpt-4o-mini', owned_by: 'openai', mode: 'chat' },
    { id: 'text-embedding-3-small', owned_by: 'openai', mode: 'embedding' },
    { id: 'dall-e-3', owned_by: 'openai', mode: 'image' },
  ],
}

/** No exceptions recorded — which means "forwards everything", not "supports nothing". */
const forwardsEverything: Capabilities = { providers: { openai: {} } }

function mockLoads(capabilities: Capabilities = forwardsEverything): void {
  requestMock.mockImplementation((path: string) => {
    if (path === '/v1/models') return Promise.resolve(catalog)
    if (path === '/v1/capabilities') return Promise.resolve(capabilities)
    return Promise.reject(new Error(`unexpected path ${path}`))
  })
}

/** A JSON answer, as `rawRequest` hands it back: the raw Response. */
function jsonResponse(body: unknown): Response {
  return { json: () => Promise.resolve(body) } as unknown as Response
}

/** An SSE body the panel's reader can drain, one chunk per `read()`. */
function streamResponse(frames: readonly string[]): Response {
  const encoder = new TextEncoder()
  let index = 0
  return {
    body: {
      getReader: () => ({
        read: () =>
          Promise.resolve(
            index < frames.length
              ? { done: false, value: encoder.encode(frames[index++]) }
              : { done: true, value: undefined },
          ),
      }),
    },
  } as unknown as Response
}

function bodyOf(call: unknown[]): Record<string, unknown> {
  const init = call[1] as { body: string }
  return JSON.parse(init.body) as Record<string, unknown>
}

function renderPage() {
  return render(
    <MemoryRouter>
      <PlaygroundPage />
    </MemoryRouter>,
  )
}

async function openTab(user: ReturnType<typeof userEvent.setup>, name: RegExp): Promise<void> {
  await user.click(await screen.findByRole('tab', { name }))
}

describe('PlaygroundPage', () => {

  beforeEach(() => {
    requestMock.mockReset()
    rawRequestMock.mockReset()
    mockLoads()
  })

  it('streams by default and shows the tokens as they arrive', async () => {
    const user = userEvent.setup()
    rawRequestMock.mockResolvedValue(
      streamResponse([
        'data: {"choices":[{"delta":{"content":"Hello"}}]}\n',
        'data: {"choices":[{"delta":{"content":" there"}}]}\n',
        'data: {"usage":{"prompt_tokens":11,"completion_tokens":2}}\n',
        'data: [DONE]\n',
      ]),
    )
    renderPage()

    await user.type(await screen.findByLabelText('Message'), 'hi')
    await user.click(screen.getByRole('button', { name: /send/i }))

    expect(await screen.findByText('Hello there')).toBeInTheDocument()
    expect(bodyOf(rawRequestMock.mock.calls[0] as unknown[]).stream).toBe(true)
    expect(screen.getByText('11 input tokens')).toBeInTheDocument()
  })

  it('sends a single non-streamed completion when the toggle is off', async () => {
    const user = userEvent.setup()
    rawRequestMock.mockResolvedValue(
      jsonResponse({
        id: 'chatcmpl-1',
        model: 'gpt-4o-mini',
        choices: [{ index: 0, message: { role: 'assistant', content: 'One shot.' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 7, completion_tokens: 3, total_tokens: 10 },
      }),
    )
    renderPage()

    await user.click(await screen.findByLabelText('Stream the response'))
    await user.type(screen.getByLabelText('Message'), 'hi')
    await user.click(screen.getByRole('button', { name: /send/i }))

    expect(await screen.findByText('One shot.')).toBeInTheDocument()
    const call = rawRequestMock.mock.calls[0] as unknown[]
    expect(bodyOf(call).stream).toBe(false)
    // The whole-response deadline is dropped: a slow generation is not a hang.
    expect(call[2]).toEqual({ connectTimeoutMs: 0 })
    expect(screen.getByText('7 input tokens')).toBeInTheDocument()
  })

  it('names the target that actually served the answer', async () => {
    const user = userEvent.setup()
    rawRequestMock.mockResolvedValue(
      jsonResponse({
        id: 'chatcmpl-2',
        model: 'gpt-4o-mini',
        provider: 'anthropic',
        choices: [{ index: 0, message: { role: 'assistant', content: 'Fell back.' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      }),
    )
    renderPage()

    await user.click(await screen.findByLabelText('Stream the response'))
    await user.type(screen.getByLabelText('Message'), 'hi')
    await user.click(screen.getByRole('button', { name: /send/i }))

    // The request named gpt-4o-mini; a fallback strategy answered from
    // somewhere else entirely, and that is invisible without this badge.
    expect(await screen.findByText('Served by anthropic')).toBeInTheDocument()
  })

  it('keeps each turn\'s own provider when a later turn is served by another target', async () => {
    const user = userEvent.setup()
    const answer = (provider: string, content: string) => jsonResponse({
      id: `chatcmpl-${provider}`,
      model: 'gpt-4o-mini',
      provider,
      choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    })
    rawRequestMock
      .mockResolvedValueOnce(answer('anthropic', 'First answer.'))
      .mockResolvedValueOnce(answer('openai', 'Second answer.'))
    renderPage()

    await user.click(await screen.findByLabelText('Stream the response'))
    await user.type(screen.getByLabelText('Message'), 'one')
    await user.click(screen.getByRole('button', { name: /send/i }))
    expect(await screen.findByText('First answer.')).toBeInTheDocument()

    await user.type(screen.getByLabelText('Message'), 'two')
    await user.click(screen.getByRole('button', { name: /send/i }))
    expect(await screen.findByText('Second answer.')).toBeInTheDocument()

    // Both, not just the newest: under fallback or load-balance the target can
    // change between turns, and a single badge for "the last one" erases the
    // change it exists to show.
    expect(screen.getByText('Served by anthropic')).toBeInTheDocument()
    expect(screen.getByText('Served by openai')).toBeInTheDocument()

    // The provider the gateway reported is the panel's own annotation, not part
    // of the conversation — sending it back would put a field the OpenAI schema
    // does not define into every follow-up request.
    const followUp = bodyOf(rawRequestMock.mock.calls[1] as unknown[])
    expect(followUp.messages).toEqual([
      { role: 'user', content: 'one' },
      { role: 'assistant', content: 'First answer.' },
      { role: 'user', content: 'two' },
    ])
  })

  it('says that a streamed answer cannot report its target, and stops saying it when streaming is off', async () => {
    const user = userEvent.setup()
    renderPage()

    // Streaming is the default, so this is what an operator sees first: the
    // badge is absent by choice, not because the page is broken.
    const note = await screen.findByText(/streamed answer cannot report which target served it/i)
    expect(note).toBeInTheDocument()

    await user.click(screen.getByLabelText('Stream the response'))
    expect(screen.queryByText(/streamed answer cannot report which target served it/i)).not.toBeInTheDocument()
  })

  it('warns when a control the operator is adjusting cannot reach the provider', async () => {
    mockLoads({ providers: { openai: { temperature: 'unsupported' } } })
    renderPage()

    const warning = await screen.findByText(/cannot express/i)
    expect(warning).toHaveTextContent('openai')
    expect(warning).toHaveTextContent('temperature')
  })

  it('stays quiet for a provider that records no exceptions', async () => {
    renderPage()

    await screen.findByLabelText('Message')
    // An empty profile means the provider forwards everything. Reading it as
    // "supports nothing" would warn about every parameter on every provider.
    expect(screen.queryByText(/cannot express/i)).not.toBeInTheDocument()
  })

  it('reports an embedding by width and magnitude rather than by dumping the vector', async () => {
    const user = userEvent.setup()
    const embedding: number[] = Array.from({ length: 1536 }, (_, index) => (index === 0 ? 1 : 0))
    embedding[9] = 0.7777
    const expectedNorm = Math.sqrt(embedding.reduce((sum, value) => sum + value * value, 0)).toFixed(4)
    rawRequestMock.mockResolvedValue(
      jsonResponse({
        object: 'list',
        model: 'text-embedding-3-small',
        data: [{ object: 'embedding', index: 0, embedding }],
        usage: { prompt_tokens: 4, total_tokens: 4 },
      }),
    )
    renderPage()

    await openTab(user, /embeddings/i)
    await user.type(screen.getByLabelText('Text to embed'), 'measure me')
    await user.click(screen.getByRole('button', { name: /create embedding/i }))

    expect(await screen.findByText('1,536 dimensions')).toBeInTheDocument()
    expect(screen.getByText(`L2 norm ${expectedNorm}`)).toBeInTheDocument()

    const preview = screen.getByText(/^\[/)
    expect(preview.textContent).toMatch(/, …\]$/)
    // The tenth component is past the preview, so its value proves the DOM did
    // not receive all 1536 floats.
    expect(preview.textContent).not.toContain('0.7777')
  })

  it('renders an inline image and links a provider-hosted one', async () => {
    const user = userEvent.setup()
    rawRequestMock.mockResolvedValue(
      jsonResponse({
        created: 1,
        data: [
          { url: 'https://images.example/one.png', revised_prompt: 'A lighthouse, rewritten' },
          { b64_json: 'aGVsbG8=' },
        ],
      }),
    )
    renderPage()

    await openTab(user, /images/i)
    await user.type(screen.getByLabelText('Image prompt'), 'a lighthouse')
    await user.click(screen.getByRole('button', { name: /generate/i }))

    expect(await screen.findByRole('link', { name: /open image 1/i })).toHaveAttribute(
      'href',
      'https://images.example/one.png',
    )
    // The rewrite is the only signal that the provider did not use the prompt
    // the operator typed.
    expect(screen.getByText(/A lighthouse, rewritten/)).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'a lighthouse' })).toHaveAttribute(
      'src',
      'data:image/png;base64,aGVsbG8=',
    )
  })
})
