import { ExternalLink, ImageIcon, Send } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button, CopyButton, EmptyState, Notice, StatusPill } from './ui'
import { ModelPicker } from './PlaygroundModelPicker'
import { rawRequest } from '../lib/api'
import { errorMessage } from '../lib/format'
import { isHttpUrl, modelsForMode, TEXTAREA_CLASS, type CatalogModel } from '../lib/playground'
import type { ImageGenerationResponse } from '../types'

/**
 * Everything the gateway accepts on this route — `core.ImageRequest`. Which of
 * them a given provider honours is not in the capability matrix, which covers
 * chat parameters only, so each control offers "Provider default" and an
 * unsupported choice comes back as the provider's own error rather than being
 * hidden here on a guess.
 */
const SIZES = ['256x256', '512x512', '1024x1024', '1792x1024', '1024x1792'] as const
const QUALITIES = ['standard', 'hd'] as const
const STYLES = ['vivid', 'natural'] as const

const PROVIDER_DEFAULT = 'Provider default'

export function PlaygroundImages({
  models,
  loadingModels,
  onRefreshModels,
}: {
  models: readonly CatalogModel[]
  loadingModels: boolean
  onRefreshModels: () => void
}) {
  const [chosenModel, setChosenModel] = useState('')
  const [prompt, setPrompt] = useState('')
  const [count, setCount] = useState(1)
  const [size, setSize] = useState('')
  const [quality, setQuality] = useState('')
  const [style, setStyle] = useState('')
  const [responseFormat, setResponseFormat] = useState('')
  const [result, setResult] = useState<ImageGenerationResponse | null>(null)
  const [submittedPrompt, setSubmittedPrompt] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controllerRef = useRef<AbortController | null>(null)

  useEffect(() => () => controllerRef.current?.abort(), [])

  const imageModels = useMemo(() => modelsForMode(models, 'image'), [models])
  const model = imageModels.some((entry) => entry.id === chosenModel) ? chosenModel : imageModels[0]?.id ?? ''

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const text = prompt.trim()
    if (!text || !model || busy) return
    const controller = new AbortController()
    controllerRef.current = controller
    setBusy(true)
    setError('')
    setResult(null)
    setSubmittedPrompt(text)
    const body = {
      model,
      prompt: text,
      n: count,
      ...(size ? { size } : {}),
      ...(quality ? { quality } : {}),
      ...(style ? { style } : {}),
      ...(responseFormat ? { response_format: responseFormat } : {}),
    }
    try {
      // Image generation routinely runs past thirty seconds, so this drops the
      // shared JSON deadline the same way the other two panels do. Only the
      // abort signal ends the wait early.
      const response = await rawRequest(
        '/v1/images/generations',
        { method: 'POST', body: JSON.stringify(body), signal: controller.signal },
        { connectTimeoutMs: 0 },
      )
      setResult((await response.json()) as ImageGenerationResponse)
    } catch (requestError) {
      if (requestError instanceof DOMException && requestError.name === 'AbortError') return
      setError(errorMessage(requestError, 'The image request failed.'))
    } finally {
      setBusy(false)
      controllerRef.current = null
    }
  }

  const hasHostedURL = result?.data.some((image) => image.url) ?? false

  if (!loadingModels && imageModels.length === 0) {
    return (
      <EmptyState
        description="No connected provider offers an image model. Connect one, or refresh the list if a provider was just added."
        title="No image models available"
      />
    )
  }

  return (
    <div className="flex flex-col gap-3">
      {error ? <Notice tone="error">{error}</Notice> : null}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,17rem)_minmax(0,1fr)]">
        <aside aria-labelledby="images-settings-heading" className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
          <h2 className="text-sm font-semibold text-foreground" id="images-settings-heading">Request settings</h2>

          <ModelPicker
            disabled={busy}
            id="images-model"
            loading={loadingModels}
            models={imageModels}
            value={model}
            onChange={setChosenModel}
            onRefresh={onRefreshModels}
          />

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="images-count">Images</Label>
            <Input
              id="images-count"
              max={4}
              min={1}
              type="number"
              value={count}
              onChange={(event) => setCount(Math.min(4, Math.max(1, Number(event.target.value) || 1)))}
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="images-size">Size</Label>
            <Select value={size} onValueChange={(value) => setSize(value ?? size)}>
              <SelectTrigger aria-label="Size" className="w-full" id="images-size">
                <SelectValue placeholder={PROVIDER_DEFAULT} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">{PROVIDER_DEFAULT}</SelectItem>
                {SIZES.map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="images-quality">Quality</Label>
            <Select value={quality} onValueChange={(value) => setQuality(value ?? quality)}>
              <SelectTrigger aria-label="Quality" className="w-full" id="images-quality">
                <SelectValue placeholder={PROVIDER_DEFAULT} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">{PROVIDER_DEFAULT}</SelectItem>
                {QUALITIES.map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="images-style">Style</Label>
            <Select value={style} onValueChange={(value) => setStyle(value ?? style)}>
              <SelectTrigger aria-label="Style" className="w-full" id="images-style">
                <SelectValue placeholder={PROVIDER_DEFAULT} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">{PROVIDER_DEFAULT}</SelectItem>
                {STYLES.map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="images-format">Response format</Label>
            <Select value={responseFormat} onValueChange={(value) => setResponseFormat(value ?? responseFormat)}>
              <SelectTrigger aria-label="Response format" className="w-full" id="images-format">
                <SelectValue placeholder={PROVIDER_DEFAULT} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">{PROVIDER_DEFAULT}</SelectItem>
                <SelectItem value="url">Hosted URL</SelectItem>
                <SelectItem value="b64_json">Inline base64</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Only inline base64 renders here. A hosted URL is linked instead.
            </p>
          </div>
        </aside>

        <section aria-labelledby="images-heading" className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border pb-3">
            <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
              <h2 className="text-sm font-semibold text-foreground" id="images-heading">Generations</h2>
              <code className="truncate font-mono text-xs text-muted-foreground">{model || 'Choose a model to begin'}</code>
            </div>
            {busy ? <StatusPill tone="info">Generating</StatusPill> : null}
          </div>

          {/*
           * The dashboard's own policy is `img-src 'self' data:` — see
           * ContentSecurityPolicy in internal/middleware/securityheaders.go,
           * which the gateway serves with the page — so a provider-hosted image
           * cannot be embedded
           * on this page at all. A broken <img> would read as a failed
           * generation; a link is the honest rendering of a URL the policy will
           * not let this origin load.
           */}
          {hasHostedURL ? (
            <Notice>
              These images are hosted by the provider. The dashboard&apos;s content security policy
              allows images only from its own origin, so each one opens in a new tab. Choose inline
              base64 to see them here.
            </Notice>
          ) : null}

          <div aria-live="polite" className="flex flex-col gap-3">
            {!result && !busy ? (
              <EmptyState
                action={<ImageIcon aria-hidden="true" size={22} />}
                description="Describe an image and send it through the gateway routing path."
                title="No generations yet"
              />
            ) : null}
            {result?.data.length === 0 ? (
              <EmptyState description="The request succeeded but returned no images." title="Nothing returned" />
            ) : null}
            {/* Guarded rather than always rendered: an empty list still reaches
                the accessibility tree and is announced as a list of no items. */}
            {result && result.data.length > 0 ? (
            <ul className="grid gap-3 sm:grid-cols-2">
              {result.data.map((image, index) => (
                <li className="flex flex-col gap-2 rounded-lg border border-border bg-muted/40 p-3" key={image.url ?? image.b64_json?.slice(0, 32) ?? index}>
                  {image.b64_json ? (
                    <img
                      alt={image.revised_prompt || submittedPrompt}
                      className="w-full rounded-md border border-border"
                      // Base64 payloads on this route are PNG, and a data: URI
                      // is same-origin content the policy above permits.
                      src={`data:image/png;base64,${image.b64_json}`}
                    />
                  ) : null}
                  {image.url ? (
                    <div className="flex items-center gap-1">
                      {isHttpUrl(image.url) ? (
                        <a
                          className="inline-flex min-w-0 items-center gap-1.5 text-sm text-primary underline underline-offset-2"
                          href={image.url}
                          rel="noreferrer noopener"
                          target="_blank"
                        >
                          <ExternalLink aria-hidden="true" className="shrink-0" size={14} />
                          <span className="truncate">Open image {index + 1}</span>
                        </a>
                      ) : (
                        <span className="min-w-0 truncate text-sm text-muted-foreground">
                          Image {index + 1} URL (not a web link)
                        </span>
                      )}
                      <CopyButton label={`Copy image ${index + 1} URL`} value={image.url} />
                    </div>
                  ) : null}
                  {/*
                   * Several providers silently rewrite a prompt before
                   * generating. Showing the rewrite is the only way an operator
                   * can tell a bad model from a prompt they never sent.
                   */}
                  {image.revised_prompt ? (
                    <p className="text-xs text-muted-foreground">
                      <span className="font-medium text-foreground">Revised prompt: </span>
                      {image.revised_prompt}
                    </p>
                  ) : null}
                </li>
              ))}
            </ul>
            ) : null}
          </div>

          <form className="flex flex-col gap-2 border-t border-border pt-3" onSubmit={(event) => void submit(event)}>
            <Label className="sr-only" htmlFor="images-prompt">Image prompt</Label>
            <textarea
              className={TEXTAREA_CLASS}
              id="images-prompt"
              placeholder="A wide shot of a lighthouse at dusk…"
              rows={3}
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
            />
            <div className="flex items-center justify-end gap-2">
              <Button disabled={!prompt.trim() || !model || busy} type="submit">
                <Send aria-hidden="true" size={16} />
                {busy ? 'Generating…' : 'Generate'}
              </Button>
            </div>
          </form>
        </section>
      </div>
    </div>
  )
}
