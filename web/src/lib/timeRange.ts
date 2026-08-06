/**
 * The time window every trailing-window page offers, and the URL reader for it.
 *
 * One list, because it is both what a range menu renders and what a
 * URL-supplied range is validated against. Two lists let an option be added to
 * the menu and then silently rejected back to the default when it is chosen —
 * which is exactly what Request Logs and the Audit Trail carried, each holding
 * a bare `[1, 6, 24, 168]` for validation beside four hand-written menu items.
 */
export interface TimeRange {
  hours: number
  label: string
}

export const TIME_RANGES: readonly TimeRange[] = [
  { hours: 1, label: 'Last hour' },
  { hours: 6, label: 'Last 6 hours' },
  { hours: 24, label: 'Last 24 hours' },
  { hours: 168, label: 'Last 7 days' },
]

export const DEFAULT_HOURS = 24

/**
 * The selected range, rejecting anything the control cannot represent.
 *
 * The value arrives from the URL, so it is caller-supplied: `?hours=nonsense`
 * would otherwise reach `buildSince` as NaN and ask the gateway for a window
 * with no start.
 */
export function hoursFrom(params: URLSearchParams): number {
  const hours = Number(params.get('hours'))
  return TIME_RANGES.some((range) => range.hours === hours) ? hours : DEFAULT_HOURS
}

/** A page offset from the URL, floored at the first page. */
export function offsetFrom(params: URLSearchParams): number {
  const offset = Number(params.get('offset'))
  return Number.isFinite(offset) && offset > 0 ? Math.floor(offset) : 0
}
