import { Ruler, Send } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button, EmptyState, Notice, StatusPill } from './ui'
import { ModelPicker } from './PlaygroundModelPicker'
import { rawRequest } from '../lib/api'
import { errorMessage, formatNumber } from '../lib/format'
import {
  l2Norm,
  modelsForMode,
  providerForModel,
  TEXTAREA_CLASS,
  VECTOR_PREVIEW,
  type CatalogModel,
} from '../lib/playground'
import type { EmbeddingsResponse } from '../types'

export function PlaygroundEmbeddings({
  models,
  loadingModels,
  onRefreshModels,
}: {
  models: readonly CatalogModel[]
  loadingModels: boolean
  onRefreshModels: () => void
}) {
  const [chosenModel, setChosenModel] = useState('')
  const [input, setInput] = useState('')
  const [dimensions, setDimensions] = useState('')
  const [result, setResult] = useState<EmbeddingsResponse | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controllerRef = useRef<AbortController | null>(null)

  useEffect(() => () => controllerRef.current?.abort(), [])

  const embeddingModels = useMemo(() => modelsForMode(models, 'embedding'), [models])
  const model = embeddingModels.some((entry) => entry.id === chosenModel) ? chosenModel : embeddingModels[0]?.id ?? ''
  const provider = providerForModel(models, model)

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const text = input.trim()
    if (!text || !model || busy) return
    const controller = new AbortController()
    controllerRef.current = controller
    setBusy(true)
    setError('')
    setResult(null)
    // Blank means "let the provider choose": sending 0 would ask for a
    // zero-length vector, which is a different request and a guaranteed 400.
    const requested = Number(dimensions)
    const body = {
      model,
      input: text,
      ...(dimensions.trim() && Number.isFinite(requested) && requested > 0 ? { dimensions: requested } : {}),
    }
    try {
      // No connect deadline, for the same reason the chat panel drops it: a
      // large batch is answered in one shot and the headers do not arrive until
      // the provider is done, so a 30-second bound reports slow as broken.
      const response = await rawRequest(
        '/v1/embeddings',
        { method: 'POST', body: JSON.stringify(body), signal: controller.signal },
        { connectTimeoutMs: 0 },
      )
      setResult((await response.json()) as EmbeddingsResponse)
    } catch (requestError) {
      if (requestError instanceof DOMException && requestError.name === 'AbortError') return
      setError(errorMessage(requestError, 'The embeddings request failed.'))
    } finally {
      setBusy(false)
      controllerRef.current = null
    }
  }

  if (!loadingModels && embeddingModels.length === 0) {
    return (
      <EmptyState
        description="No connected provider offers an embedding model. Connect one, or refresh the list if a provider was just added."
        title="No embedding models available"
      />
    )
  }

  return (
    <div className="flex flex-col gap-3">
      {error ? <Notice tone="error">{error}</Notice> : null}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,17rem)_minmax(0,1fr)]">
        <aside aria-labelledby="embeddings-settings-heading" className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
          <h2 className="text-sm font-semibold text-foreground" id="embeddings-settings-heading">Request settings</h2>

          <ModelPicker
            disabled={busy}
            id="embeddings-model"
            loading={loadingModels}
            models={embeddingModels}
            value={model}
            onChange={setChosenModel}
            onRefresh={onRefreshModels}
          />

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="embeddings-dimensions">
              Dimensions <span className="font-normal text-muted-foreground">(optional)</span>
            </Label>
            <Input
              id="embeddings-dimensions"
              min={1}
              placeholder="Provider default"
              type="number"
              value={dimensions}
              onChange={(event) => setDimensions(event.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Only some models can shorten their output. Others answer at their native width.
            </p>
          </div>
        </aside>

        <section aria-labelledby="embeddings-heading" className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border pb-3">
            <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
              <h2 className="text-sm font-semibold text-foreground" id="embeddings-heading">Vectors</h2>
              <code className="truncate font-mono text-xs text-muted-foreground">{model || 'Choose a model to begin'}</code>
            </div>
            {busy ? <StatusPill tone="info">Embedding</StatusPill> : null}
          </div>

          <div aria-live="polite" className="flex flex-col gap-2">
            {!result && !busy ? (
              <EmptyState
                action={<Ruler aria-hidden="true" size={22} />}
                description="Send text to see its vector width, leading components, and magnitude."
                title="No embedding yet"
              />
            ) : null}
            {result?.data.length === 0 ? (
              <EmptyState description="The request succeeded but returned no vectors." title="Nothing returned" />
            ) : null}
            {/*
             * Width, eight leading components, and magnitude — not the vector.
             * A single text-embedding-3-large answer is 3072 floats; rendering
             * them puts a six-figure character count into the DOM and tells an
             * operator nothing that these three numbers do not.
             */}
            {result?.data.map((vector) => (
              <article className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm" key={vector.index}>
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <span className="font-medium text-foreground">Vector {vector.index}</span>
                  <span className="tabular-nums">{formatNumber(vector.embedding.length)} dimensions</span>
                  <span className="tabular-nums">L2 norm {l2Norm(vector.embedding).toFixed(4)}</span>
                </div>
                <p className="mt-1 font-mono text-xs break-all text-foreground">
                  [{vector.embedding.slice(0, VECTOR_PREVIEW).map((value) => value.toFixed(4)).join(', ')}
                  {vector.embedding.length > VECTOR_PREVIEW ? ', …' : ''}]
                </p>
              </article>
            ))}
          </div>

          {result ? (
            <div className="flex flex-wrap gap-x-3 text-xs tabular-nums text-muted-foreground">
              <span>{provider ? `${provider} · ` : ''}{result.model}</span>
              <span>{formatNumber(result.usage.prompt_tokens)} input tokens</span>
              <span>{formatNumber(result.usage.total_tokens)} total tokens</span>
            </div>
          ) : null}

          <form className="flex flex-col gap-2 border-t border-border pt-3" onSubmit={(event) => void submit(event)}>
            <Label className="sr-only" htmlFor="embeddings-input">Text to embed</Label>
            <textarea
              className={TEXTAREA_CLASS}
              id="embeddings-input"
              placeholder="Text to embed…"
              rows={3}
              value={input}
              onChange={(event) => setInput(event.target.value)}
            />
            <div className="flex items-center justify-end gap-2">
              <Button disabled={!input.trim() || !model || busy} type="submit">
                <Send aria-hidden="true" size={16} />
                {busy ? 'Embedding…' : 'Create embedding'}
              </Button>
            </div>
          </form>
        </section>
      </div>
    </div>
  )
}
