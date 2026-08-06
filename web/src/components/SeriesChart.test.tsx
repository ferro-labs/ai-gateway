import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import type { LogsSeries } from "../types"
import { SeriesChart } from "./SeriesChart"
import { TOKEN_BANDS, TRAFFIC_BANDS } from "../lib/chartBands"

function series(overrides: Partial<LogsSeries> = {}): LogsSeries {
  return {
    points: [
      { start: "2026-07-23T10:00:00Z", requests: 4, errors: 1, prompt_tokens: 40, completion_tokens: 10 },
      { start: "2026-07-23T11:00:00Z", requests: 6, errors: 0, prompt_tokens: 60, completion_tokens: 20 },
    ],
    truncated: false,
    start: "2026-07-23T10:00:00Z",
    end: "2026-07-23T12:00:00Z",
    ...overrides,
  }
}

describe("SeriesChart", () => {
  it("stacks successes under failures so the bar height stays the request count", () => {
    const { container } = render(<SeriesChart bands={TRAFFIC_BANDS} emptyLabel="none" series={series()} unit="requests" />)

    // 10 requests, of which 1 failed and 9 succeeded — the accessible name
    // carries the same story the bars do.
    expect(screen.getByRole("img", { name: /10 requests over 2 hours: 9 succeeded, 1 failed/i })).toBeInTheDocument()
    // Three rects, not four: the second bucket has no failures, and a zero-height
    // band must not be painted.
    expect(container.querySelectorAll('rect[data-slot="chart-bar"]')).toHaveLength(3)
  })

  it("reports the token split rather than a single total", () => {
    render(<SeriesChart bands={TOKEN_BANDS} emptyLabel="none" series={series()} unit="tokens" />)
    expect(screen.getByRole("img", { name: /130 tokens over 2 hours: 100 input, 30 output/i })).toBeInTheDocument()
  })

  it("says so when the range holds more events than one query returns", () => {
    render(<SeriesChart bands={TRAFFIC_BANDS} emptyLabel="none" series={series({ truncated: true })} unit="requests" />)
    // Silence here would present a partial window as the whole selected range.
    expect(screen.getByText(/holds more events than one query returns/i)).toBeInTheDocument()
  })

  it("renders an explanation rather than empty axes when nothing was recorded", () => {
    const empty = series({ points: [{ start: "2026-07-23T10:00:00Z", requests: 0, errors: 0, prompt_tokens: 0, completion_tokens: 0 }] })
    const { container } = render(<SeriesChart bands={TRAFFIC_BANDS} emptyLabel="No completed requests." series={empty} unit="requests" />)
    expect(screen.getByText("No completed requests.")).toBeInTheDocument()
    expect(container.querySelector("svg")).toBeNull()
  })
})
