import type { SeriesPoint } from '../types'

/**
 * The stacked bands a `SeriesChart` draws.
 *
 * Separate from the component so the chart file exports a component and nothing
 * else, which is what keeps fast refresh working.
 */
export interface Band {
  key: 'success' | 'errors' | 'input' | 'output'
  label: string
  /** SVG fill utility. */
  fill: string
  /** Legend swatch utility, matching `fill`. */
  swatch: string
  value: (point: SeriesPoint) => number
}

/**
 * Traffic, drawn as successes stacked under failures.
 *
 * Not "requests" with "errors" overlaid: an overlay invites reading the second
 * series as additional traffic. Stacked, the pair sums to the request count and
 * cannot be misread that way.
 */
export const TRAFFIC_BANDS: Band[] = [
  {
    key: 'success',
    label: 'Succeeded',
    fill: 'fill-primary',
    swatch: 'bg-primary',
    value: (point) => Math.max(0, point.requests - point.errors),
  },
  { key: 'errors', label: 'Failed', fill: 'fill-danger', swatch: 'bg-danger', value: (point) => point.errors },
]

/**
 * Token throughput, split by direction.
 *
 * Output tokens bill at a multiple of input tokens on every major provider, so
 * the ratio between these two bands is the shape of the invoice.
 */
export const TOKEN_BANDS: Band[] = [
  { key: 'input', label: 'Input', fill: 'fill-info', swatch: 'bg-info', value: (point) => point.prompt_tokens },
  { key: 'output', label: 'Output', fill: 'fill-primary', swatch: 'bg-primary', value: (point) => point.completion_tokens },
]
