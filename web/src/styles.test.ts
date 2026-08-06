import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * Tailwind v4 resolves an uncoloured `border-b` to `currentColor`.
 *
 * v3's preflight defaulted every border to a grey, and the component library's
 * primitives are written against that: a table row, a table footer and a dialog
 * footer all say `border-b`/`border-t` and name no colour. Without the base
 * reset below, each of those hairlines is drawn in the foreground colour —
 * near-black on a light page, near-white on a dark one — in exactly the places
 * the design wants the faintest line on the screen.
 *
 * Nothing else catches it. It is not a contrast failure, so the accessibility
 * sweep passes; it compiles, so the build passes; and the visual baselines were
 * first captured while it was broken, so they recorded the fault as the
 * intended appearance. That is the whole reason for asserting it here.
 */
describe('stylesheet', () => {
  const css = readFileSync('src/styles.css', 'utf8')

  it('resets border-color to the token, not the text colour', () => {
    expect(
      css,
      'src/styles.css must reset border-color for the universal selector, or every ' +
        'uncoloured border utility falls back to currentColor.',
    ).toMatch(/\*[^{]*\{[^}]*border-color:\s*var\(--color-border\)/s)
  })
})
