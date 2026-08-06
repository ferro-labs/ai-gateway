import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * The icon has to be both declared and present.
 *
 * It was neither: the file sat in `src/assets/` where nothing imported it, so
 * it was never bundled, and the page declared no icon at all — leaving the
 * browser's implicit request for `/favicon.ico` to 404 silently. Nothing fails
 * when a favicon is missing, which is exactly why it went unnoticed.
 *
 * `public/` is the right home for it: files there ship at a stable, unhashed
 * path, and the implicit request goes to the root regardless of what the build
 * would otherwise rename it to.
 */
describe('document head', () => {
  const html = readFileSync('index.html', 'utf8')

  it('declares an icon that exists at the path it names', () => {
    const href = /<link[^>]+rel="icon"[^>]*href="([^"]+)"/.exec(html)?.[1]
      ?? /<link[^>]+href="([^"]+)"[^>]*rel="icon"/.exec(html)?.[1]
    expect(href, 'index.html declares no <link rel="icon">').toBeTruthy()
    expect(href!.startsWith('/'), `icon href ${href!} should be an absolute path`).toBe(true)

    const onDisk = join('public', href!.slice(1))
    expect(
      existsSync(onDisk),
      `index.html points at ${href!}, but ${onDisk} does not exist, so the build ships no icon.`,
    ).toBe(true)
  })
})
