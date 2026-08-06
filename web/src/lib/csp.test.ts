import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * The Content-Security-Policy allows one inline stylesheet by digest.
 *
 * The component library injects a fixed stylesheet to hide the native scrollbar
 * inside an open dropdown. Allowing it by digest keeps `'unsafe-inline'` out of
 * a dashboard that manages credentials — but a digest is pinned to an exact
 * string, and upgrading the library can change that string with no build error
 * and no test failure. The dropdown would simply show a scrollbar, and a
 * console violation nobody reads would be the only signal.
 *
 * This recomputes the digest from the installed library and compares it to the
 * one the server actually sends, so a mismatch fails here instead.
 */

const STYLES_MODULE = 'node_modules/@base-ui/react/utils/styles.js'
// The gateway serves the dashboard and its policy header, so the Go constant is
// where the digest lives.
const POLICY_SOURCE = '../internal/middleware/securityheaders.go'

function injectedStylesheet(): string {
  const source = readFileSync(STYLES_MODULE, 'utf8')
  // The template literal in `getElement`, with its two interpolations of the
  // class-name constant resolved.
  const className = /const DISABLE_SCROLLBAR_CLASS_NAME = '([^']+)'/.exec(source)?.[1]
  expect(className, `class name not found in ${STYLES_MODULE}`).toBeTruthy()
  const body = /children: `([^`]+)`/.exec(source)?.[1]
  expect(body, `injected stylesheet not found in ${STYLES_MODULE}`).toBeTruthy()
  return body!.replaceAll('${DISABLE_SCROLLBAR_CLASS_NAME}', className!)
}

/**
 * The `style-src` directive as the Go constant spells it.
 *
 * The match is anchored to the quoted Go string literal rather than to the
 * bare directive name, because the constant's doc comment discusses `style-src`
 * and `'unsafe-inline'` in prose and a looser pattern reads the comment instead
 * of the policy.
 */
function styleSrc(): string {
  const source = readFileSync(POLICY_SOURCE, 'utf8')
  const directive = /"style-src ([^";]+); "/.exec(source)?.[1]
  expect(directive, `style-src not found in ${POLICY_SOURCE}`).toBeTruthy()
  return directive!
}

describe('content security policy', () => {
  it('allows exactly the stylesheet the installed library injects', () => {
    const css = injectedStylesheet()
    const digest = `sha256-${createHash('sha256').update(css, 'utf8').digest('base64')}`

    const served = /'(sha256-[^']+)'/.exec(styleSrc())?.[1]
    expect(
      served,
      `The policy allows no stylesheet digest.\n` +
        `  stylesheet: ${css}\n` +
        `  expected:   'style-src' to include '${digest}'\n` +
        `Add the digest to ContentSecurityPolicy in ${POLICY_SOURCE}.`,
    ).toBeTruthy()
    expect(
      served,
      `The policy does not allow the stylesheet the library injects.\n` +
        `  stylesheet: ${css}\n` +
        `Update the digest in ${POLICY_SOURCE}.`,
    ).toBe(digest)
  })

  it('does not permit arbitrary inline styles', () => {
    // A digest is a narrow exception. `'unsafe-inline'` would additionally
    // disable every digest in the same directive, silently widening the policy
    // rather than adding to it.
    expect(styleSrc()).not.toContain('unsafe-inline')
    expect(styleSrc()).toContain("'self'")
  })
})
