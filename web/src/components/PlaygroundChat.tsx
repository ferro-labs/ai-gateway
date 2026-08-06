import { CircleStop, Play, RotateCcw, Send } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'
import { Button, EmptyState, Notice, StatusPill } from './ui'
import { ModelPicker, UnsupportedParams } from './PlaygroundModelPicker'
import { rawRequest } from '../lib/api'
import { errorMessage, formatNumber } from '../lib/format'
import {
  modelsForMode,
  providerForModel,
  TEXTAREA_CLASS,
  unsupportedParams,
  type CatalogModel,
} from '../lib/playground'
import type { Capabilities, ChatCompletionResponse, ChatMessage, ChatStreamChunk } from '../types'

interface Usage {
  // null means the provider never reported a figure. stream_options below asks
  // for one, but a provider that ignores the option leaves these unset, and an
  // unmeasured 0 must not be rendered as though it were a measured one.
  prompt: number | null
  completion: number | null
  elapsed: number
}

/**
 * The gateway's mid-stream failure shape (see internal/sse/sse.go `Write`).
 * Once the response is already a 200 and streaming, a provider error or an
 * idle timeout is reported as one more `data:` frame —
 * `{"error":{"message","type","code"}}` — and the connection then closes
 * without a final `[DONE]`. `ChatStreamChunk` only models the success shape;
 * this extends it locally rather than teaching the shared type about a frame
 * most callers never see.
 *
 * `provider` is read defensively: `core.StreamChunk` carries no such field
 * today, so a streamed answer cannot say which target served it. Reading it
 * anyway costs nothing and means the badge below starts working the day the
 * gateway starts sending it, rather than the day someone notices.
 */
type StreamFrame = ChatStreamChunk & { error?: { message?: string }; provider?: string }

/**
 * One turn of the conversation, plus which target answered it.
 *
 * Provenance belongs to the turn and not to the panel: under fallback or
 * load-balance routing two consecutive answers can come from two different
 * targets, and a single value for "the last one" overwrites the first the
 * moment the second arrives. `servedBy` is stripped before the transcript is
 * sent — it is the gateway's answer about this conversation, not part of it.
 */
type Turn = ChatMessage & { servedBy?: string }

/** The controls this panel exposes, as the capability matrix names them. */
const ADJUSTED_PARAMS = ['temperature', 'max_tokens'] as const

export function PlaygroundChat({
  models,
  loadingModels,
  capabilities,
  onRefreshModels,
}: {
  models: readonly CatalogModel[]
  loadingModels: boolean
  capabilities: Capabilities | null
  onRefreshModels: () => void
}) {
  const [chosenModel, setChosenModel] = useState('')
  const [system, setSystem] = useState('')
  const [prompt, setPrompt] = useState('')
  const [temperature, setTemperature] = useState(0.7)
  const [maxTokens, setMaxTokens] = useState(1024)
  const [streamEnabled, setStreamEnabled] = useState(true)
  const [messages, setMessages] = useState<Turn[]>([])
  const [response, setResponse] = useState('')
  const [usage, setUsage] = useState<Usage | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const requestController = useRef<AbortController | null>(null)

  useEffect(() => () => requestController.current?.abort(), [])

  const chatModels = useMemo(() => modelsForMode(models, 'chat'), [models])
  // Derived rather than stored: a refresh that retires the chosen model falls
  // back to the first available one on the next render, instead of leaving a
  // stale id in state for the next send to fail on.
  const model = chatModels.some((entry) => entry.id === chosenModel) ? chosenModel : chatModels[0]?.id ?? ''
  const provider = providerForModel(models, model)
  const unsupported = useMemo(
    () => unsupportedParams(capabilities, provider, streamEnabled ? [...ADJUSTED_PARAMS, 'stream'] : ADJUSTED_PARAMS),
    [capabilities, provider, streamEnabled],
  )

  const stop = () => requestController.current?.abort()

  const send = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const userText = prompt.trim()
    if (!userText || !model || busy) return
    const controller = new AbortController()
    requestController.current = controller
    const started = performance.now()
    const outgoing: ChatMessage[] = [
      ...(system.trim() ? [{ role: 'system' as const, content: system.trim() }] : []),
      ...messages.map(({ role, content }) => ({ role, content })),
      { role: 'user' as const, content: userText },
    ]
    setMessages((current) => [...current, { role: 'user', content: userText }])
    setPrompt('')
    setResponse('')
    setUsage(null)
    setError('')
    setBusy(true)
    let fullText = ''
    let promptTokens = 0
    let completionTokens = 0
    let usageReceived = false
    let answeredBy = ''
    try {
      if (streamEnabled) {
        const stream = await rawRequest('/v1/chat/completions', {
          method: 'POST',
          body: JSON.stringify({
            model,
            messages: outgoing,
            temperature,
            max_tokens: maxTokens,
            stream: true,
            // Without this, providers never emit a usage object on a streamed
            // response and the counters below never move off their initial zero.
            stream_options: { include_usage: true },
          }),
          signal: controller.signal,
        })
        if (!stream.body) throw new Error('The gateway returned an empty stream.')
        const reader = stream.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        let frameError: string | null = null

        const consumeLine = (line: string) => {
          if (!line.startsWith('data:')) return
          const payload = line.slice(5).trim()
          if (!payload || payload === '[DONE]') return
          try {
            const chunk = JSON.parse(payload) as StreamFrame
            if (chunk.error) {
              // No [DONE] follows this frame — the server has already closed the
              // connection (sse.go returns right after writing it) — so record
              // the failure and let the read loop end on its own next pass.
              frameError = chunk.error.message || 'The stream ended with an upstream error.'
              return
            }
            if (chunk.provider) answeredBy = chunk.provider
            const content = chunk.choices?.[0]?.delta?.content
            if (content) {
              fullText += content
              setResponse(fullText)
            }
            if (chunk.usage) {
              promptTokens = chunk.usage.prompt_tokens ?? promptTokens
              completionTokens = chunk.usage.completion_tokens ?? completionTokens
              usageReceived = true
            }
          } catch {
            // An incomplete frame remains buffered; malformed complete events are ignored.
          }
        }

        while (true) {
          const chunk = await reader.read()
          if (chunk.done) break
          buffer += decoder.decode(chunk.value, { stream: true })
          const lines = buffer.split(/\r?\n/)
          buffer = lines.pop() ?? ''
          for (const line of lines) consumeLine(line)
        }
        buffer += decoder.decode()
        if (buffer.trim()) consumeLine(buffer.trim())

        // An error frame means the answer never finished — it must not be
        // committed as though it had. Throwing routes it through the same
        // rollback as any other failed send, below.
        if (frameError) throw new Error(frameError)
      } else {
        /*
         * `rawRequest` with no connect deadline, not `request`: this path
         * receives no response headers at all until the provider has finished
         * producing the whole answer, so the shared 30-second JSON deadline
         * would report every long generation as a gateway that stopped
         * answering. The abort signal above is still what stops it.
         */
        const single = await rawRequest(
          '/v1/chat/completions',
          {
            method: 'POST',
            body: JSON.stringify({ model, messages: outgoing, temperature, max_tokens: maxTokens, stream: false }),
            signal: controller.signal,
          },
          { connectTimeoutMs: 0 },
        )
        const body = (await single.json()) as ChatCompletionResponse
        fullText = body.choices?.[0]?.message?.content ?? ''
        answeredBy = body.provider ?? ''
        if (body.usage) {
          promptTokens = body.usage.prompt_tokens
          completionTokens = body.usage.completion_tokens
          usageReceived = true
        }
      }

      if (fullText) {
        setMessages((current) => [...current, { role: 'assistant', content: fullText, servedBy: answeredBy }])
      }
      setResponse('')
      setUsage({
        prompt: usageReceived ? promptTokens : null,
        completion: usageReceived ? completionTokens : null,
        elapsed: performance.now() - started,
      })
    } catch (streamError) {
      if (streamError instanceof DOMException && streamError.name === 'AbortError') {
        // The prompt really did reach the model; only the reply is
        // incomplete. Clear the unfinished reply so the visible transcript
        // matches what a retry would actually send — nothing, since it was
        // never committed to messages.
        setError('Generation stopped.')
        setResponse('')
      } else {
        // Nothing usable came back — undo the optimistic user turn and hand
        // the prompt back. Otherwise a retry builds on an orphaned "You"
        // bubble and sends two consecutive user turns, which several
        // providers reject outright.
        setMessages((current) => current.slice(0, -1))
        setPrompt(userText)
        setResponse('')
        setError(errorMessage(streamError, 'The completion request failed.'))
      }
    } finally {
      setBusy(false)
      requestController.current = null
    }
  }

  const reset = () => {
    stop()
    setMessages([])
    setResponse('')
    setUsage(null)
    setError('')
  }

  if (!loadingModels && chatModels.length === 0) {
    return (
      <EmptyState
        description="No connected provider offers a chat model. Connect one, or refresh the list if a provider was just added."
        title="No chat models available"
      />
    )
  }

  return (
    <div className="flex flex-col gap-3">
      {error ? <Notice tone={error === 'Generation stopped.' ? 'warning' : 'error'}>{error}</Notice> : null}
      <UnsupportedParams params={unsupported} provider={provider} />

      {/*
       * A fixed settings rail, not a third of the width. Its widest control is
       * a model name; on a wide screen a proportional column left it padded
       * with empty card while the conversation — the part that actually grows —
       * was held to two thirds.
       */}
      <div className="grid gap-4 lg:grid-cols-[minmax(0,17rem)_minmax(0,1fr)]">
        <aside aria-labelledby="chat-settings-heading" className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
          <h2 className="text-sm font-semibold text-foreground" id="chat-settings-heading">Request settings</h2>

          <ModelPicker
            disabled={busy}
            id="playground-model"
            loading={loadingModels}
            models={chatModels}
            value={model}
            onChange={setChosenModel}
            onRefresh={onRefreshModels}
          />

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="playground-system">
              System prompt <span className="font-normal text-muted-foreground">(optional)</span>
            </Label>
            <textarea
              className={TEXTAREA_CLASS}
              id="playground-system"
              placeholder="You are a concise assistant."
              rows={4}
              value={system}
              onChange={(event) => setSystem(event.target.value)}
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="playground-temperature">
              Temperature <output className="font-normal text-muted-foreground">{temperature.toFixed(1)}</output>
            </Label>
            <input
              className="accent-primary"
              id="playground-temperature"
              max={2}
              min={0}
              step={0.1}
              type="range"
              value={temperature}
              onChange={(event) => setTemperature(Number(event.target.value))}
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="playground-max-tokens">Max tokens</Label>
            <Input
              id="playground-max-tokens"
              max={32768}
              min={1}
              type="number"
              value={maxTokens}
              onChange={(event) => setMaxTokens(Math.max(1, Number(event.target.value)))}
            />
          </div>

          {/*
           * Streaming and non-streaming are two different paths through the
           * gateway — one through internal/sse, one through the JSON handler —
           * and a deployment can have exactly one of them broken. Exercising
           * only the streamed path leaves the other untested from here.
           */}
          <div className="flex items-center justify-between gap-3">
            <Label htmlFor="playground-stream">Stream the response</Label>
            <input
              checked={streamEnabled}
              className="accent-primary size-4"
              disabled={busy}
              id="playground-stream"
              type="checkbox"
              onChange={(event) => setStreamEnabled(event.target.checked)}
            />
          </div>
          {/*
           * Said out loud rather than left to be noticed. The "Served by" badge
           * is this page's whole reason for existing, streaming is on by
           * default, and a streamed answer cannot carry the target that served
           * it: the SSE frames are the OpenAI wire format every client reads,
           * and a gateway-specific field does not belong in them for the sake
           * of one dashboard panel. Without this note the badge silently never
           * appears, which reads as a broken feature rather than a choice.
           */}
          {streamEnabled ? (
            <p className="text-xs text-muted-foreground">
              A streamed answer cannot report which target served it. Turn streaming off to see the
              “Served by” badge on each reply.
            </p>
          ) : null}
        </aside>

        <section aria-labelledby="chat-heading" className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border pb-3">
            <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
              <h2 className="text-sm font-semibold text-foreground" id="chat-heading">Conversation</h2>
              <code className="truncate font-mono text-xs text-muted-foreground">{model || 'Choose a model to begin'}</code>
            </div>
            <div className="flex items-center gap-2">
              {busy ? <StatusPill tone="info">{streamEnabled ? 'Streaming' : 'Waiting for the answer'}</StatusPill> : null}
              <Button size="sm" type="button" variant="outline" onClick={reset}>
                <RotateCcw aria-hidden="true" size={15} />
                Reset
              </Button>
            </div>
          </div>

          <div aria-live="polite" className="flex min-h-64 flex-col gap-2 overflow-y-auto">
            {messages.length === 0 && !response ? (
              <EmptyState action={<Play aria-hidden="true" size={22} />} description="Choose a model, enter a prompt, and send the request." title="Start a conversation" />
            ) : null}
            {messages.map((message, index) =>
              message.role === 'system' ? null : (
                <article
                  className={cn(
                    'max-w-[85%] rounded-lg border px-3 py-1.5 text-sm',
                    message.role === 'user' ? 'ml-auto border-primary/30 bg-primary/10' : 'border-border bg-muted/40',
                  )}
                  key={`${message.role}-${index}`}
                >
                  <span className="mb-0.5 flex flex-wrap items-center gap-2 text-xs font-medium text-muted-foreground">
                    {message.role === 'user' ? 'You' : 'Assistant'}
                    {/*
                     * Which target actually served this turn. Under fallback or
                     * load-balance routing the chosen model says nothing about
                     * where the request ended up, and that is exactly the
                     * behaviour this page exists to make visible.
                     *
                     * Per turn, because consecutive turns can be answered by
                     * different targets. Absent on a streamed turn — see the
                     * note beside the toggle.
                     */}
                    {message.servedBy ? (
                      <StatusPill tone="neutral">Served by {message.servedBy}</StatusPill>
                    ) : null}
                  </span>
                  <p className="whitespace-pre-wrap text-foreground">{message.content}</p>
                </article>
              ),
            )}
            {busy || response ? (
              <article className="max-w-[85%] rounded-lg border border-border bg-muted/40 px-3 py-1.5 text-sm">
                <span className="mb-0.5 block text-xs font-medium text-muted-foreground">Assistant</span>
                <p className="whitespace-pre-wrap text-foreground">{response || 'Waiting for the first token…'}</p>
              </article>
            ) : null}
          </div>

          {usage ? (
            <div className="flex flex-wrap gap-x-3 text-xs tabular-nums text-muted-foreground">
              <span>{(usage.elapsed / 1000).toFixed(2)}s</span>
              {usage.prompt != null ? <span>{formatNumber(usage.prompt)} input tokens</span> : null}
              {usage.completion != null ? <span>{formatNumber(usage.completion)} output tokens</span> : null}
            </div>
          ) : null}

          <form className="flex flex-col gap-2 border-t border-border pt-3" onSubmit={(event) => void send(event)}>
            <Label className="sr-only" htmlFor="playground-prompt">Message</Label>
            <textarea
              className={TEXTAREA_CLASS}
              id="playground-prompt"
              placeholder="Ask the gateway…"
              rows={3}
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) event.currentTarget.form?.requestSubmit()
              }}
            />
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs text-muted-foreground">⌘ Enter to send</span>
              {busy ? (
                <Button type="button" variant="destructive" onClick={stop}>
                  <CircleStop aria-hidden="true" size={16} />
                  Stop
                </Button>
              ) : (
                <Button disabled={!prompt.trim() || !model} type="submit">
                  <Send aria-hidden="true" size={16} />
                  Send
                </Button>
              )}
            </div>
          </form>
        </section>
      </div>
    </div>
  )
}
