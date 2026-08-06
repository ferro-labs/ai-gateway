import {
  CircleDollarSign,
  FlaskConical,
  GaugeCircle,
  GitBranch,
  ListOrdered,
  Scale,
  ScanText,
  Target,
  type LucideIcon,
} from 'lucide-react'

/**
 * The eight routing strategies the gateway ships — `StrategyMode` in
 * config/config.go.
 *
 * `id` is the value `strategy.mode` takes in the configuration document, so it
 * is a wire contract and not a label: changing one here stops the active mode
 * from matching its card. The descriptions state what each one does with a
 * request, because that is the question an operator comparing them is asking,
 * and each links to the reference the values themselves are documented in.
 */
export const STRATEGY_DOCS = 'https://docs.ferrolabs.ai/getting-started/configuration/#strategy'

export interface StrategyInfo {
  id: string
  label: string
  icon: LucideIcon
  /** One sentence: what happens to a request under this mode. */
  summary: string
  /** The configuration this mode reads beyond `targets`, or "" when it reads none. */
  configures: string
}

export const STRATEGIES: readonly StrategyInfo[] = [
  {
    id: 'single',
    label: 'Single',
    icon: Target,
    summary: 'Sends every request to one target. Nothing takes over when it refuses the model or fails.',
    configures: '',
  },
  {
    id: 'fallback',
    label: 'Fallback',
    icon: ListOrdered,
    summary: 'Tries targets in the configured order, exhausting each one’s retry policy before moving to the next.',
    configures: 'targets[].retry',
  },
  {
    id: 'loadbalance',
    label: 'Load balance',
    icon: Scale,
    summary: 'Spreads requests across targets by weighted random choice, skipping any that cannot serve the model.',
    configures: 'targets[].weight',
  },
  {
    id: 'least-latency',
    label: 'Least latency',
    icon: GaugeCircle,
    summary: 'Picks the capable target with the lowest observed p50. Unmeasured targets are chosen only when none has been measured.',
    configures: '',
  },
  {
    id: 'cost-optimized',
    label: 'Cost optimized',
    icon: CircleDollarSign,
    summary: 'Picks the cheapest capable target using model-catalog pricing, estimated on input tokens.',
    configures: 'strategy.unpriced_strategy',
  },
  {
    id: 'conditional',
    label: 'Conditional',
    icon: GitBranch,
    summary: 'Matches the requested model — exactly or by prefix — against rules in order and routes to the first that matches.',
    configures: 'strategy.conditions',
  },
  {
    id: 'content-based',
    label: 'Content based',
    icon: ScanText,
    summary: 'Reads the user messages themselves, by substring or regular expression, and routes to the first rule that matches.',
    configures: 'strategy.content_conditions',
  },
  {
    id: 'ab-test',
    label: 'A/B test',
    icon: FlaskConical,
    // The label names the variant in the configuration and nowhere else: the
    // strategy contributes a target order, and only the target a request lands
    // on reaches logs, metrics and traces. Saying otherwise sent operators
    // looking for a field no query can find.
    summary: 'Splits traffic between labelled variants by weight. Only the target a request lands on is recorded — the variant label is configuration, not telemetry.',
    configures: 'strategy.ab_variants',
  },
] as const

export function strategyInfo(mode: string): StrategyInfo | undefined {
  return STRATEGIES.find((strategy) => strategy.id === mode)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value != null && typeof value === 'object' && !Array.isArray(value)
}

function records(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value) ? value.filter(isRecord) : []
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/** One `targets[]` entry, reduced to what this dashboard renders. */
export interface StrategyTarget {
  virtualKey: string
  /** Undefined rather than 1 when unset: the strategy's own default applies. */
  weight?: number
  retryAttempts?: number
  maxConcurrency?: number
  queueSize?: number
  circuitBreaker: boolean
}

/**
 * One rule of whichever rule list the active mode reads.
 *
 * Conditional, content-based and A/B test each configure a differently shaped
 * list, and all three answer the same question on screen — what decides where
 * this request goes — so they are normalised to one row shape rather than three
 * near-identical tables.
 */
export interface StrategyRule {
  /** The match, e.g. `model_prefix = gpt-4` or `prompt_contains "invoice"`. */
  match: string
  target: string
  /** A variant label, present only for A/B test. */
  label?: string
  /** A traffic share in percent, present only for A/B test. */
  share?: number
}

export interface StrategyState {
  /** `strategy.mode` verbatim, "" when the document does not set one. */
  mode: string
  /** The catalog entry for `mode`, absent when the gateway reports a mode this build does not know. */
  info?: StrategyInfo
  targets: StrategyTarget[]
  rules: StrategyRule[]
  /** Names the rule list `rules` came from, for the panel heading. */
  ruleLabel: string
  /** `strategy.unpriced_strategy`, meaningful only under cost-optimized. */
  unpricedStrategy: string
}

function readTargets(config: Record<string, unknown>): StrategyTarget[] {
  return records(config.targets).map((target) => {
    const concurrency = isRecord(target.concurrency) ? target.concurrency : undefined
    const retry = isRecord(target.retry) ? target.retry : undefined
    return {
      virtualKey: text(target.virtual_key),
      weight: typeof target.weight === 'number' ? target.weight : undefined,
      retryAttempts: typeof retry?.attempts === 'number' ? retry.attempts : undefined,
      maxConcurrency: typeof concurrency?.max_concurrency === 'number' ? concurrency.max_concurrency : undefined,
      queueSize: typeof concurrency?.queue_size === 'number' ? concurrency.queue_size : undefined,
      circuitBreaker: isRecord(target.circuit_breaker),
    }
  })
}

function readRules(mode: string, strategy: Record<string, unknown>): { rules: StrategyRule[]; ruleLabel: string } {
  if (mode === 'conditional') {
    return {
      ruleLabel: 'Conditions',
      rules: records(strategy.conditions).map((rule) => ({
        match: `${text(rule.key) || 'model'} = ${text(rule.value)}`,
        target: text(rule.target_key),
      })),
    }
  }
  if (mode === 'content-based') {
    return {
      ruleLabel: 'Content conditions',
      rules: records(strategy.content_conditions).map((rule) => ({
        match: `${text(rule.type)} ${JSON.stringify(text(rule.value))}`,
        target: text(rule.target_key),
      })),
    }
  }
  if (mode === 'ab-test') {
    const variants = records(strategy.ab_variants)
    // Zero means zero. `weightedPick` (internal/strategies/dispatch.go) skips a
    // zero-weight element outright and `NewABTest` documents the same rule, so a
    // variant with no weight — set to 0, or simply absent, which decodes to 0 —
    // receives nothing. Normalising it to 1 here showed an equal share for a
    // variant the gateway had drained, which is the reading an operator drains a
    // variant to check.
    //
    // A negative weight is clamped rather than subtracted, mirroring
    // positiveWeight; ValidateConfig rejects one at load, as it rejects an
    // all-zero set, so neither reaches a running gateway.
    const weights = variants.map((variant) => (typeof variant.weight === 'number' ? Math.max(variant.weight, 0) : 0))
    const total = weights.reduce((sum, weight) => sum + weight, 0)
    return {
      ruleLabel: 'Variants',
      rules: variants.map((variant, index) => ({
        match: text(variant.label) || `variant ${index + 1}`,
        target: text(variant.target_key),
        label: text(variant.label),
        share: total > 0 ? ((weights[index] ?? 0) / total) * 100 : 0,
      })),
    }
  }
  return { rules: [], ruleLabel: '' }
}

/** Everything the strategy panels render, read from GET /admin/config. */
export function readStrategy(config: Record<string, unknown>): StrategyState {
  const strategy = isRecord(config.strategy) ? config.strategy : {}
  const mode = text(strategy.mode)
  return {
    mode,
    info: strategyInfo(mode),
    targets: readTargets(config),
    unpricedStrategy: text(strategy.unpriced_strategy),
    ...readRules(mode, strategy),
  }
}
