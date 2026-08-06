import { describe, expect, it } from 'vitest'
import { groupByOwner, isHttpUrl, modelsForMode, providerForModel, UNKNOWN_OWNER, type CatalogModel } from './playground'

/*
 * A model id is unique per provider, not across the gateway. Two providers
 * serving the same upstream model — groq and together both advertise
 * openai/gpt-oss-120b — make GET /v1/models return that id twice, which is what
 * these cases are about.
 */
const SHARED_ID = 'openai/gpt-oss-120b'

const CATALOG: CatalogModel[] = [
  { id: 'gpt-4o', owned_by: 'openai', mode: 'chat' },
  { id: SHARED_ID, owned_by: 'groq', mode: 'chat' },
  { id: SHARED_ID, owned_by: 'together', mode: 'chat' },
  { id: 'text-embedding-3-small', owned_by: 'openai', mode: 'embedding' },
  { id: 'dall-e-3', owned_by: 'openai', mode: 'image' },
  { id: 'house-tuned-llama', owned_by: 'ollama' },
  { id: 'gpt-3.5-turbo', owned_by: 'openai', mode: 'chat', deprecated: true },
  { id: 'text-embedding-ada-002', owned_by: 'openai', mode: 'embedding', deprecated: true },
  { id: 'dall-e-2', owned_by: 'openai', mode: 'image', deprecated: true },
  { id: 'retired-untyped', owned_by: 'ollama', deprecated: true },
]

describe('modelsForMode', () => {
  it('offers a shared model once, because the picker cannot choose who serves it', () => {
    const ids = modelsForMode(CATALOG, 'chat').map((model) => model.id)

    expect(ids.filter((id) => id === SHARED_ID)).toHaveLength(1)
  })

  it('keeps a model the catalog cannot classify, rather than hiding it with no explanation', () => {
    const ids = modelsForMode(CATALOG, 'chat').map((model) => model.id)

    expect(ids).toContain('house-tuned-llama')
  })

  it('excludes a model belonging to another surface', () => {
    const ids = modelsForMode(CATALOG, 'chat').map((model) => model.id)

    expect(ids).not.toContain('dall-e-3')
    expect(ids).not.toContain('text-embedding-3-small')
  })

  it.each(['chat', 'embedding', 'image'] as const)(
    'never offers a deprecated model on the %s surface',
    (mode) => {
      const offered = modelsForMode(CATALOG, mode)

      expect(offered.filter((model) => model.deprecated)).toEqual([])
    },
  )

  // The "keep what the catalog cannot classify" rule exists because an absent
  // mode means unknown. Deprecated is not unknown — it is the catalog stating
  // the model is being retired — so it loses to the exclusion.
  it('drops a deprecated model even when the catalog gives it no mode', () => {
    const ids = modelsForMode(CATALOG, 'chat').map((model) => model.id)

    expect(ids).toContain('house-tuned-llama')
    expect(ids).not.toContain('retired-untyped')
  })
})

describe('providerForModel', () => {
  it('names the provider when exactly one serves the model', () => {
    expect(providerForModel(CATALOG, 'gpt-4o')).toBe('openai')
  })

  it('names none when several serve it, so no warning claims the wrong limits', () => {
    // The strategy picks between groq and together per request. Reporting
    // either one would warn about parameters the provider that actually
    // answers may forward perfectly well.
    expect(providerForModel(CATALOG, SHARED_ID)).toBe('')
  })

  it('names none for a model the catalog does not carry', () => {
    expect(providerForModel(CATALOG, 'no-such-model')).toBe('')
  })
})

describe('groupByOwner', () => {
  it('groups by provider and sorts providers and their models by name', () => {
    const models: CatalogModel[] = [
      { id: 'gpt-4o', owned_by: 'openai' },
      { id: 'claude-sonnet-5', owned_by: 'anthropic' },
      { id: 'claude-haiku-5', owned_by: 'anthropic' },
      { id: 'gpt-4.1', owned_by: 'openai' },
    ]

    expect(groupByOwner(models)).toEqual([
      ['anthropic', [
        { id: 'claude-haiku-5', owned_by: 'anthropic' },
        { id: 'claude-sonnet-5', owned_by: 'anthropic' },
      ]],
      ['openai', [
        { id: 'gpt-4.1', owned_by: 'openai' },
        { id: 'gpt-4o', owned_by: 'openai' },
      ]],
    ])
  })

  // A self-hosted or newly released model can arrive with no owner. Dropping it
  // would make it unreachable from the picker with nothing on screen to explain
  // the absence, which is the failure modelsForMode already guards against.
  it('keeps a model the gateway reported without an owner', () => {
    expect(groupByOwner([{ id: 'local-model' }])).toEqual([[UNKNOWN_OWNER, [{ id: 'local-model' }]]])
  })
})

describe('isHttpUrl', () => {
  it('accepts the http(s) URLs an image provider normally returns', () => {
    expect(isHttpUrl('https://cdn.example.com/img/1.png')).toBe(true)
    expect(isHttpUrl('http://localhost:8080/img.png')).toBe(true)
  })

  // A compromised or malicious upstream can put any string in `url`, and a
  // javascript: href executes in this document — next to the session token —
  // when clicked. Nothing but http(s) may become a link.
  it('rejects every non-http scheme', () => {
    expect(isHttpUrl('javascript:alert(document.cookie)')).toBe(false)
    expect(isHttpUrl('JavaScript:alert(1)')).toBe(false)
    expect(isHttpUrl('data:text/html,<script>alert(1)</script>')).toBe(false)
    expect(isHttpUrl('vbscript:msgbox(1)')).toBe(false)
    expect(isHttpUrl('file:///etc/passwd')).toBe(false)
  })

  it('rejects strings that do not parse as a URL at all', () => {
    expect(isHttpUrl('')).toBe(false)
    expect(isHttpUrl('not a url')).toBe(false)
    expect(isHttpUrl('//protocol-relative.example.com/x.png')).toBe(false)
  })
})
