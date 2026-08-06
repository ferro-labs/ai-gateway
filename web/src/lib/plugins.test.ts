import { describe, expect, it } from 'vitest'
import type { PluginCatalogEntry } from '../types'
import { failurePolicy, groupPlugins, pluginIcon, pluginInfo, pluginStatus, readPlugins } from './plugins'

/*
 * The catalog as GET /admin/plugins/catalog serves it.
 *
 * This file used to hold the catalog itself and assert its contents, which is
 * how four fabricated config keys shipped with a passing suite behind them: a
 * test that restates what the code says proves only that the two agree. Which
 * plugins exist, and which keys each reads, is now asserted in Go against the
 * plugins themselves (plugin/catalog_test.go). What is left here is the part
 * that is genuinely this side's: how a served entry is read.
 */
const served: PluginCatalogEntry[] = [
  {
    name: 'rate-limit',
    type: 'ratelimit',
    summary: 'Bounds request rate.',
    settings: ['requests_per_second', 'burst'],
    fails_open: false,
  },
  {
    name: 'request-logger',
    type: 'logging',
    summary: 'Records each request.',
    settings: ['level', 'persist'],
    fails_open: true,
  },
]

describe('pluginInfo', () => {
  it('reports nothing for a plugin this build does not ship', () => {
    expect(pluginInfo(served, 'house-guardrail')).toBeUndefined()
  })

  it('reports nothing before the gateway has answered', () => {
    expect(pluginInfo(null, 'rate-limit')).toBeUndefined()
  })
})

describe('pluginIcon', () => {
  it('falls back rather than failing for a name it has no glyph for', () => {
    expect(pluginIcon('house-guardrail')).toBeDefined()
  })
})

describe('failurePolicy', () => {
  it('reads what the gateway reported, not the type the document declares', () => {
    /*
     * A configuration is free to write `type: guardrail` on rate-limit, and the
     * gateway ignores it: `plugin/manager.go` decides from `Type()`. The
     * catalog carries the decided answer, so the dashboard no longer keeps a
     * list of fail-open types to re-derive it from — a second copy of a rule
     * only the gateway applies.
     */
    expect(failurePolicy(served, 'rate-limit')).toBe('closed')
    expect(failurePolicy(served, 'request-logger')).toBe('open')
  })

  it('claims nothing about a plugin the gateway did not describe', () => {
    expect(failurePolicy(served, 'house-guardrail')).toBeUndefined()
    expect(failurePolicy(null, 'rate-limit')).toBeUndefined()
  })
})

describe('readPlugins', () => {
  it('survives a document with no plugins list, or one that is not a list', () => {
    expect(readPlugins({})).toEqual([])
    expect(readPlugins({ plugins: 'word-filter' })).toEqual([])
  })

  it('treats anything but an explicit true as disabled', () => {
    const [plugin] = readPlugins({ plugins: [{ name: 'word-filter', enabled: 'yes' }] })

    expect(plugin?.enabled).toBe(false)
  })
})

describe('groupPlugins', () => {
  const entries = readPlugins({
    plugins: [
      { name: 'response-cache', type: 'transform', stage: 'before_request', enabled: true, config: { ttl: 60 } },
      { name: 'response-cache', type: 'transform', stage: 'after_request', enabled: true, config: { ttl: 60 } },
    ],
  })

  it('collapses the entries of a stage-branching plugin into one group', () => {
    const [group] = groupPlugins(entries)

    expect(group?.stages).toEqual(['before_request', 'after_request'])
    expect(group?.entryCount).toBe(2)
    // Identical settings, listed once: repeating them would read as two
    // unrelated configurations.
    expect(group?.settings).toEqual([{ key: 'ttl', value: '60' }])
  })

  it('calls a group whose entries disagree partly enabled, not enabled', () => {
    // The stages resolve to different instances, so whatever the live one
    // recorded is silently dropped by the other.
    const split = readPlugins({
      plugins: [
        { name: 'budget', type: 'ratelimit', stage: 'before_request', enabled: true, config: {} },
        { name: 'budget', type: 'ratelimit', stage: 'after_request', enabled: false, config: {} },
      ],
    })

    expect(pluginStatus(groupPlugins(split)[0]!)).toEqual({ label: 'Partly enabled', tone: 'warning' })
  })

  it('names an entry with no name rather than grouping every such entry apart', () => {
    expect(groupPlugins(readPlugins({ plugins: [{ enabled: true }] }))[0]?.name).toBe('unnamed')
  })

  it('separates a redacted setting from one whose value survived', () => {
    const mixed = readPlugins({
      plugins: [{ name: 'budget', config: { limit_usd: 10, store_id: '[REDACTED]' } }],
    })
    const [group] = groupPlugins(mixed)

    expect(group?.settings).toEqual([{ key: 'limit_usd', value: '10' }])
    expect(group?.redactedKeys).toEqual(['store_id'])
  })

  it('truncates a setting too long to sit on one line', () => {
    const long = readPlugins({ plugins: [{ name: 'word-filter', config: { blocked_words: 'x'.repeat(200) } }] })

    expect(groupPlugins(long)[0]?.settings[0]?.value).toMatch(/…$/)
  })
})
