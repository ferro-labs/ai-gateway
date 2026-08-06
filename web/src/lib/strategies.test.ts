import { describe, expect, it } from 'vitest'
import { readStrategy, STRATEGIES, strategyInfo } from './strategies'

describe('STRATEGIES', () => {
  it('carries every mode config/config.go defines, spelled the way the wire spells it', () => {
    // These ids are matched against `strategy.mode` from the gateway, so a
    // rename here silently stops the active mode from being recognised.
    expect(STRATEGIES.map((strategy) => strategy.id)).toEqual([
      'single',
      'fallback',
      'loadbalance',
      'least-latency',
      'cost-optimized',
      'conditional',
      'content-based',
      'ab-test',
    ])
  })

  it('names no mode it cannot describe', () => {
    for (const strategy of STRATEGIES) {
      expect(strategy.summary.length, `${strategy.id} has no summary`).toBeGreaterThan(0)
      expect(strategy.label.length, `${strategy.id} has no label`).toBeGreaterThan(0)
    }
  })

  it('reports nothing for a mode this build does not know', () => {
    expect(strategyInfo('quantum-routing')).toBeUndefined()
  })
})

describe('readStrategy targets', () => {
  it('distinguishes an unset field from a configured one', () => {
    // `weight: undefined` and `weight: 0` are different configurations, and so
    // are an absent retry block and `attempts: 1`. Defaulting either on the way
    // in would show an operator a value they never set.
    const state = readStrategy({
      strategy: { mode: 'fallback' },
      targets: [
        { virtual_key: 'openai', weight: 1, retry: { attempts: 3 }, concurrency: { max_concurrency: 32, queue_size: 500 } },
        { virtual_key: 'anthropic', circuit_breaker: { failure_threshold: 5 } },
      ],
    })

    expect(state.targets[0]).toEqual({
      virtualKey: 'openai',
      weight: 1,
      retryAttempts: 3,
      maxConcurrency: 32,
      queueSize: 500,
      circuitBreaker: false,
    })
    expect(state.targets[1]).toEqual({
      virtualKey: 'anthropic',
      weight: undefined,
      retryAttempts: undefined,
      maxConcurrency: undefined,
      queueSize: undefined,
      circuitBreaker: true,
    })
  })

  it('survives a document with no targets at all', () => {
    expect(readStrategy({}).targets).toEqual([])
    expect(readStrategy({ targets: 'not-a-list' }).targets).toEqual([])
  })
})

describe('readStrategy rules', () => {
  it('reads conditional rules as key/value matches', () => {
    const state = readStrategy({
      strategy: {
        mode: 'conditional',
        conditions: [{ key: 'model_prefix', value: 'gpt-4', target_key: 'openai' }],
      },
    })

    expect(state.ruleLabel).toBe('Conditions')
    expect(state.rules).toEqual([{ match: 'model_prefix = gpt-4', target: 'openai' }])
  })

  it('quotes a content-based value, so trailing space in a pattern is visible', () => {
    const state = readStrategy({
      strategy: {
        mode: 'content-based',
        content_conditions: [{ type: 'prompt_contains', value: 'invoice ', target_key: 'anthropic' }],
      },
    })

    expect(state.rules).toEqual([{ match: 'prompt_contains "invoice "', target: 'anthropic' }])
  })

  it('gives a zero-weight variant no traffic, as the strategy does', () => {
    // internal/strategies/dispatch.go skips a zero-weight element outright, so
    // a drained variant receives nothing. Normalising it to 1 here reported an
    // equal share for the variant an operator had just drained.
    const state = readStrategy({
      strategy: {
        mode: 'ab-test',
        ab_variants: [
          { target_key: 'openai', weight: 0, label: 'control' },
          { target_key: 'anthropic', weight: 3, label: 'challenger' },
        ],
      },
    })

    expect(state.ruleLabel).toBe('Variants')
    expect(state.rules[0]).toMatchObject({ label: 'control', share: 0 })
    expect(state.rules[1]).toMatchObject({ label: 'challenger', share: 100 })
  })

  it('reads an absent weight as zero, the value the decoder produces', () => {
    // `ab_variants[].weight` is a float64 with no default, so an omitted field
    // decodes to 0 and the variant is never picked. ValidateConfig refuses a set
    // where every weight is zero, so this document does not start a gateway —
    // the panel still has to describe it honestly rather than invent a split.
    const state = readStrategy({
      strategy: { mode: 'ab-test', ab_variants: [{ target_key: 'openai' }, { target_key: 'anthropic' }] },
    })

    expect(state.rules.map((rule) => rule.share)).toEqual([0, 0])
  })

  it('names an unlabelled variant by position rather than leaving it blank', () => {
    const state = readStrategy({ strategy: { mode: 'ab-test', ab_variants: [{ target_key: 'openai', weight: 1 }] } })

    expect(state.rules[0]?.match).toBe('variant 1')
    expect(state.rules[0]?.share).toBe(100)
  })

  it('does not claim the variant label reaches telemetry', () => {
    // Only the resolved target is recorded: SelectTargets returns target keys,
    // and no label travels with them into logs, metrics or spans.
    const summary = strategyInfo('ab-test')?.summary ?? ''

    expect(summary).not.toMatch(/records the variant/i)
    expect(summary).toMatch(/target/i)
  })

  it('reads no rules for a mode that configures none', () => {
    const state = readStrategy({ strategy: { mode: 'loadbalance', conditions: [{ key: 'model', value: 'x' }] } })

    expect(state.rules).toEqual([])
    expect(state.ruleLabel).toBe('')
  })
})

describe('readStrategy mode', () => {
  it('keeps a mode it has no card for, so the page reports what is running', () => {
    const state = readStrategy({ strategy: { mode: 'quantum-routing' } })

    expect(state.mode).toBe('quantum-routing')
    expect(state.info).toBeUndefined()
  })

  it('reads an unset mode as unset rather than inventing a default', () => {
    expect(readStrategy({}).mode).toBe('')
  })

  it('carries the unpriced strategy through for the cost-optimized card', () => {
    expect(readStrategy({ strategy: { mode: 'cost-optimized', unpriced_strategy: 'skip' } }).unpricedStrategy).toBe('skip')
  })
})
