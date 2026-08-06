import type { Capabilities, ParamSupport } from '../types'

/**
 * A model as GET /v1/models actually reports it — `EnrichedModelInfo` in
 * internal/handler/models.go.
 *
 * Deliberately not `ModelInfo` from types.ts: that shape requires `created`,
 * which the handler never emits, and carries neither of the two fields this
 * surface routes on. `mode` decides which tab may offer a model; `owned_by` is
 * the provider id, and therefore the key into the capability matrix.
 *
 * Both are `omitempty` and both come from the model catalog, so a model the
 * catalog has no entry for arrives with neither. That is "unknown", not
 * "wrong mode" — see `modelsForMode`.
 */
export type ModelMode = 'chat' | 'embedding' | 'image' | 'audio_in' | 'audio_out'

export interface CatalogModel {
  id: string
  owned_by?: string
  mode?: ModelMode
  /**
   * True when the catalog marks the model deprecated or legacy.
   *
   * `GET /v1/models` reports it and keeps listing the model, because that
   * endpoint is OpenAI-compatible and its consumers are entitled to the full
   * inventory plus the flag to judge it by. This page applies a policy to that
   * inventory rather than asking the API to narrow it — see `modelsForMode`.
   */
  deprecated?: boolean
  status?: string
}

export interface CatalogModelsResponse {
  object: string
  data: CatalogModel[]
}

/**
 * The models a tab may offer, plus every model the catalog cannot classify.
 *
 * Hiding the unclassified ones would make a self-hosted or newly released model
 * unreachable from this page entirely, with nothing on screen to explain the
 * absence. Offering them costs one honest error from the gateway when the
 * choice was wrong, which is the outcome this page exists to surface.
 *
 * A model the catalog marks deprecated is excluded outright. Deprecation is not
 * ambiguity — the catalog is stating that the provider is retiring the model —
 * so there is no working choice to protect, and the cost of offering one is
 * paid on a bill or by a request that stops being answered. This is a policy of
 * this page, not of the gateway: `GET /v1/models` still lists every entry with
 * its `deprecated` flag, because an OpenAI-compatible endpoint that quietly
 * dropped rows would break consumers that legitimately want the whole
 * inventory, and would make the flag it already publishes pointless.
 */
export function modelsForMode(models: readonly CatalogModel[], mode: ModelMode): CatalogModel[] {
  const offered = models.filter(
    (model) => (!model.mode || model.mode === mode) && !model.deprecated,
  )
  // A model id is not unique across providers: groq and together both advertise
  // openai/gpt-oss-120b, so /v1/models returns it twice. Two identical rows in
  // the picker offer a choice that does not exist — the request carries the id
  // alone and the routing strategy, not this page, decides who serves it.
  const seen = new Set<string>()
  return offered.filter((model) => !seen.has(model.id) && seen.add(model.id))
}

/** The label a model with no `owned_by` is grouped under. */
export const UNKNOWN_OWNER = 'Unknown provider'

/**
 * Models grouped by the provider that owns them, providers and each group's
 * models sorted by name.
 *
 * One flat list of every model on every provider is the state this replaces: a
 * gateway with openai and gemini configured offers 240 rows in a single
 * scroller with no indication that a third of them belong to a different
 * provider.
 */
export function groupByOwner(models: readonly CatalogModel[]): [string, CatalogModel[]][] {
  const groups = new Map<string, CatalogModel[]>()
  for (const model of models) {
    const owner = model.owned_by || UNKNOWN_OWNER
    const group = groups.get(owner)
    if (group) group.push(model)
    else groups.set(owner, [model])
  }
  return [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([owner, group]) => [owner, [...group].sort((a, b) => a.id.localeCompare(b.id))])
}

/**
 * The provider the capability matrix should be read against, or "" when that
 * cannot be known.
 *
 * An id served by more than one provider has no single answer here, and the
 * strategy chooses between them per request. Naming one of them would warn
 * about limits the provider that actually answers may not have, so an ambiguous
 * id resolves to nothing and the caller falls back to assuming the parameter is
 * forwarded — the same default the matrix itself uses.
 */
export function providerForModel(models: readonly CatalogModel[], id: string): string {
  const owners = new Set(
    models.filter((model) => model.id === id).map((model) => model.owned_by ?? ''),
  )
  if (owners.size !== 1) return ''
  return [...owners][0] ?? ''
}

/**
 * How one provider handles one OpenAI chat parameter.
 *
 * An absent entry is `forward`. The matrix records exceptions — a provider with
 * nothing to declare forwards everything — so reading a missing key as
 * "unsupported" would invert the meaning of the whole table and warn about
 * every parameter on every well-behaved provider. An unregistered provider is
 * unknown, and unknown is not grounds for a warning either.
 */
export function paramSupport(
  capabilities: Capabilities | null,
  provider: string,
  param: string,
): ParamSupport {
  if (!capabilities || !provider) return 'forward'
  return capabilities.providers[provider]?.[param] ?? 'forward'
}

/** The subset of `params` the provider cannot express at all. */
export function unsupportedParams(
  capabilities: Capabilities | null,
  provider: string,
  params: readonly string[],
): string[] {
  return params.filter((param) => paramSupport(capabilities, provider, param) === 'unsupported')
}

/**
 * Euclidean length of an embedding.
 *
 * Worth a line of screen space because most providers return unit vectors: a
 * norm that is not ~1.0 means the caller has to normalise before a cosine
 * similarity, and that is invisible in a list of floats.
 */
export function l2Norm(vector: readonly number[]): number {
  return Math.sqrt(vector.reduce((sum, value) => sum + value * value, 0))
}

/** How many leading components of a vector are worth rendering. */
export const VECTOR_PREVIEW = 8

/**
 * The multi-line text input shared by all three tabs.
 *
 * There is no textarea primitive in components/ui, and three copies of this
 * string drift independently the first time the focus ring changes.
 */
export const TEXTAREA_CLASS =
  'w-full resize-y rounded-md border border-input bg-background p-2.5 text-sm text-foreground outline-none focus-visible:ring-3 focus-visible:ring-ring/50'

/**
 * Whether a provider-returned URL is safe to render as an `href`.
 *
 * An anchor executes rather than navigates when its href carries a
 * `javascript:` scheme — `rel="noopener"` is no defence, because the script
 * runs in this document, next to the session token. Remote data therefore only
 * becomes a link when it parses as http(s); anything else is shown copy-only.
 */
export function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'https:' || url.protocol === 'http:'
  } catch {
    return false
  }
}
