import { afterEach, describe, expect, it, vi } from "vitest"
import { buildSince, formatNumber, formatTimestamp, maskKey, timeAgo, titleForPath } from "./format"

describe("dashboard formatting", () => {
  afterEach(() => vi.useRealTimers())

  it("formats values and already-masked keys without hiding their suffix twice", () => {
    expect(formatNumber(24891)).toBe("24,891")
    expect(formatNumber(undefined)).toBe("—")
    expect(maskKey("fgw_1234...abcd")).toBe("fgw_1234...abcd")
    expect(maskKey("fgw_1234567890abcdef")).toBe("fgw_1234…cdef")
  })

  it("formats relative time deterministically", () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-07-21T12:00:00Z"))
    expect(timeAgo("2026-07-21T11:58:30Z")).toBe("1m ago")
    expect(buildSince(1)).toBe("2026-07-21T11:00:00.000Z")
  })

  /*
   * Seconds are the point. Every event in one burst shares a minute, so a log
   * timestamp that stops there renders a dozen rows identically — exactly when
   * their order is the thing being read.
   */
  it("keeps seconds and drops the year in a log timestamp", () => {
    // Asserted by shape, not by literal: the output is rendered in the
    // runner's own zone, so a hard-coded "16:34:21" only passes under UTC.
    const rendered = formatTimestamp("2026-07-23T16:34:21Z")
    expect(rendered).toMatch(/\d{2}:\d{2}:\d{2}/)
    expect(rendered).not.toContain("2026")
    expect(formatTimestamp("not a date")).toBe("—")
    expect(formatTimestamp(null)).toBe("—")
  })

  it("maps dashboard paths to user-facing titles", () => {
    expect(titleForPath("/getting-started")).toBe("Getting Started")
    expect(titleForPath("/config")).toBe("Configuration")
    expect(titleForPath("/unknown")).toBe("Dashboard")
  })
})
