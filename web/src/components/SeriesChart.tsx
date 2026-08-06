import type { LogsSeries, SeriesPoint } from '../types'
import type { Band } from '../lib/chartBands'
import { formatNumber } from '../lib/format'

/**
 * A stacked bar chart over a server-computed time series.
 *
 * The buckets arrive from the gateway rather than being derived here from raw
 * rows. Client-side bucketing could only ever see one page of the request log —
 * two hundred events — which on any busy gateway is a few seconds of traffic
 * being drawn as if it were the selected range.
 *
 * Two bands, always stacked so the pair sums to the bar's real total. A chart
 * of "requests" with "errors" drawn over it invites reading the overlay as
 * additional traffic; successes and failures side by side cannot be misread
 * that way, and the bar height stays the request count either way.
 *
 * No chart library: this is a landing-page-sized bundle and two rects per
 * bucket does not justify one.
 */

const TIME_LABEL = new Intl.DateTimeFormat(undefined, { hour: '2-digit', hourCycle: 'h23', minute: '2-digit' })
const DATE_TIME_LABEL = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  hour: '2-digit',
  hourCycle: 'h23',
  minute: '2-digit',
  month: 'short',
})

const DAY_MS = 24 * 60 * 60 * 1000

/** Hour/minute reads fine within a day, but is ambiguous once the axis spans more. */
function axisLabel(timestamp: number, spanMs: number): string {
  return (spanMs > DAY_MS ? DATE_TIME_LABEL : TIME_LABEL).format(timestamp)
}

/** A human span ("5 minutes", "3 hours", "2 days") for captioning the axis. */
function formatSpan(ms: number): string {
  const minutes = Math.max(1, Math.round(ms / 60_000))
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'}`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'}`
  const days = Math.round(hours / 24)
  return `${days} day${days === 1 ? '' : 's'}`
}

const WIDTH = 760
const HEIGHT = 200
const BASELINE = 164
const PLOT_HEIGHT = 140

export function SeriesChart({
  series,
  bands,
  unit,
  emptyLabel,
}: {
  series: LogsSeries
  bands: Band[]
  /** Pluralised noun for the accessible summary — "requests", "tokens". */
  unit: string
  emptyLabel: string
}) {
  const points = series.points ?? []
  const totals = bands.map((band) => points.reduce((sum, point) => sum + band.value(point), 0))
  const grandTotal = totals.reduce((sum, value) => sum + value, 0)

  if (points.length === 0 || grandTotal === 0) {
    return (
      <p className="flex h-40 items-center justify-center rounded-md border border-dashed border-border text-sm text-muted-foreground">
        {emptyLabel}
      </p>
    )
  }

  const stackTotal = (point: SeriesPoint) => bands.reduce((sum, band) => sum + band.value(point), 0)
  const peak = Math.max(1, ...points.map(stackTotal))
  const step = WIDTH / points.length
  const barWidth = Math.max(1.5, step * 0.62)

  const startMs = Date.parse(series.start ?? points[0]!.start)
  const endMs = Date.parse(series.end ?? points[points.length - 1]!.start)
  const spanMs = Math.max(1, endMs - startMs)

  // The visible legend is decorative; the accessible name carries the same
  // story a sighted reader takes off the bars.
  const summary = bands.map((band, index) => `${formatNumber(totals[index] ?? 0)} ${band.label.toLowerCase()}`).join(', ')
  const ariaLabel = `${formatNumber(grandTotal)} ${unit} over ${formatSpan(spanMs)}: ${summary}. Peak ${formatNumber(peak)} in one interval.`

  const labelIndices = [...new Set([0, Math.floor(points.length / 3), Math.floor((points.length * 2) / 3), points.length - 1])]

  return (
    <div className="flex flex-col gap-2">
      <div aria-hidden="true" className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {bands.map((band, index) => (
          <span className="inline-flex items-center gap-1.5" key={band.key}>
            <span className={`inline-block size-2.5 rounded-full ${band.swatch}`} />
            {band.label}
            <span className="tabular-nums text-foreground">{formatNumber(totals[index] ?? 0)}</span>
          </span>
        ))}
      </div>

      <svg aria-label={ariaLabel} className="w-full" role="img" viewBox={`0 0 ${WIDTH} ${HEIGHT}`}>
        {[0, 0.5, 1].map((ratio) => (
          <line className="stroke-border" key={ratio} x1="0" x2={WIDTH} y1={BASELINE - ratio * PLOT_HEIGHT} y2={BASELINE - ratio * PLOT_HEIGHT} />
        ))}

        {points.map((point, index) => {
          let cursor = BASELINE
          return bands.map((band) => {
            const value = band.value(point)
            if (value <= 0) return null
            const height = (value / peak) * PLOT_HEIGHT
            cursor -= height
            return (
              <rect
                className={band.fill}
                data-slot="chart-bar"
                height={height}
                key={`${point.start}-${band.key}`}
                width={barWidth}
                x={index * step + (step - barWidth) / 2}
                y={cursor}
              />
            )
          })
        })}

        {labelIndices.map((index) => {
          const point = points[index]
          if (!point) return null
          return (
            <text
              className="fill-muted-foreground text-[10px]"
              key={index}
              textAnchor={index === 0 ? 'start' : index === points.length - 1 ? 'end' : 'middle'}
              x={index * step + step / 2}
              y={HEIGHT - 10}
            >
              {axisLabel(Date.parse(point.start), spanMs)}
            </text>
          )
        })}
      </svg>

      {series.truncated ? (
        <p className="text-xs text-muted-foreground">
          Showing the last {formatSpan(spanMs)} — the selected range holds more events than one query returns, so
          earlier activity is not plotted.
        </p>
      ) : null}
    </div>
  )
}
