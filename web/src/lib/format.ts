import { labelForPath } from './routes'
export function formatNumber(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  return new Intl.NumberFormat().format(value)
}

export function formatDateTime(value: string | null | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date)
}

/**
 * A log timestamp: no year, seconds included, 24-hour.
 *
 * `formatDateTime` is right for a key's expiry, where the year matters and the
 * value is read one at a time. It is wrong for a log table. It spent a column
 * on "2026" that is the same on every row, and it stopped at the minute — so
 * the twelve events a single burst produces all rendered as one indistinguishable
 * time, which is precisely when the order of two rows is the thing being read.
 *
 * `hourCycle` rather than `hour12: false`, which is specified to yield hour 24
 * for midnight in some locales.
 */
export function formatTimestamp(value: string | null | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(undefined, {
    day: 'numeric',
    hour: '2-digit',
    hourCycle: 'h23',
    minute: '2-digit',
    month: 'short',
    second: '2-digit',
  }).format(date)
}

export function timeAgo(value: string | null | undefined): string {
  if (!value) return 'Never'
  const timestamp = new Date(value).getTime()
  if (!Number.isFinite(timestamp)) return '—'
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000))
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

export function maskKey(value: string): string {
  if (!value) return '—'
  if (value.includes('...')) return value
  if (value.length <= 12) return `${value.slice(0, 4)}…`
  return `${value.slice(0, 8)}…${value.slice(-4)}`
}

export function errorMessage(error: unknown, fallback = 'Something went wrong.'): string {
  return error instanceof Error && error.message ? error.message : fallback
}

export function buildSince(hours: number): string {
  return new Date(Date.now() - hours * 60 * 60 * 1000).toISOString()
}

export function titleForPath(pathname: string): string {
  return labelForPath(pathname)
}

