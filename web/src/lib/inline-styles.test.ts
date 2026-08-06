import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * The Content-Security-Policy allows no inline styles, and a hash does not
 * cover the `style` attribute — only `<style>` elements. So a `style={{…}}`
 * prop is silently dropped in a real deployment.
 *
 * Silently is the problem. The onboarding progress bar set its fill width that
 * way; the width never applied, and an empty block element with `width: auto`
 * fills its parent, so the bar reported every step complete no matter how many
 * were. Nothing failed: the dev server and the preview server used by the
 * browser tests send no policy, so it looked correct everywhere it was checked
 * and was wrong only where it mattered.
 *
 * This fails at the source instead. Anything genuinely dynamic belongs in a
 * stylesheet rule, a utility class, or an element that takes a value as an
 * attribute — `<progress>` for a bar, for instance.
 */

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) return sourceFiles(path)
    return /\.tsx?$/.test(entry) && !/\.test\.tsx?$/.test(entry) ? [path] : []
  })
}

describe('content security policy', () => {
  it('has no inline style props, which the policy silently drops', () => {
    const offenders = sourceFiles('src')
      .filter((path) => /\bstyle=\{/.test(readFileSync(path, 'utf8')))
      .map((path) => path.replace(/^src\//, ''))

    expect(
      offenders,
      `These set a style attribute, which the policy drops without warning:\n` +
        offenders.map((f) => `  ${f}`).join('\n') +
        `\nUse a class, a stylesheet rule, or an element that takes the value as an attribute.`,
    ).toEqual([])
  })
})
