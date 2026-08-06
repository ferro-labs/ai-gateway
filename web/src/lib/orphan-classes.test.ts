import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * A class name that is neither a Tailwind utility nor a rule in styles.css
 * styles nothing, and says so nowhere.
 *
 * Six of them shipped: `.fatal-error`, `.eyeless-label`, `.button`,
 * `.button-primary`, `.route-loading` and `.session-check` survived the move to
 * Tailwind as markup with no stylesheet behind it. They belong to the screens
 * shown when the application cannot start, so the only way to see the damage
 * was to break the application on purpose — and every one of them still looked
 * like styling to a reader of the JSX.
 *
 * Deciding which side of the line a token falls on needs a rule, and the two
 * candidates fail differently. A naming convention cannot be applied here: the
 * names above live in files this stylesheet does not own and cannot be renamed
 * to carry a prefix. So the rule is an explicit vocabulary of Tailwind utility
 * roots, derived by scanning the sources. Anything outside it must resolve in
 * styles.css. A genuinely new utility root fails this test once, and the fix is
 * one line here — cheap, and far cheaper than the failure it replaces, which is
 * an operator staring at an unstyled error page.
 */

/**
 * First hyphen-segment of every Tailwind utility the dashboard uses, variants
 * and arbitrary values stripped. Add to this when a new utility family appears.
 */
const TAILWIND_ROOTS = new Set([
  'absolute', 'accent', 'align', 'animate', 'aspect', 'backdrop', 'bg', 'block', 'border',
  'bottom', 'box', 'break', 'caption', 'capitalize', 'caret', 'col', 'columns', 'cursor', 'delay', 'divide',
  'duration', 'ease', 'fade', 'fill', 'fixed', 'flex', 'float', 'font', 'gap', 'grid', 'group', 'grow',
  'h', 'hidden', 'inline', 'inset', 'invisible', 'isolate', 'italic', 'items', 'justify',
  'leading', 'left', 'line', 'list', 'm', 'max', 'mb', 'me', 'min', 'ml', 'mr', 'ms', 'mt', 'mx',
  'my', 'no', 'normal', 'not', 'object', 'opacity', 'order', 'origin', 'outline', 'overflow', 'overscroll',
  'p', 'pb', 'pe', 'pl', 'place', 'pointer', 'pr', 'ps', 'pt', 'px', 'py', 'relative', 'resize',
  'right', 'ring', 'rotate', 'rounded', 'row', 'scale', 'scroll', 'select', 'self', 'shadow',
  'shrink', 'size', 'skew', 'slide', 'space', 'spin', 'sr', 'static', 'sticky', 'stroke',
  'tabular', 'text', 'top', 'touch', 'tracking', 'transform', 'transition', 'translate',
  'truncate', 'underline', 'uppercase', 'visible', 'w', 'whitespace', 'will', 'z', 'zoom',
])

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) return sourceFiles(path)
    return /\.tsx$/.test(entry) && !/\.test\.tsx$/.test(entry) ? [path] : []
  })
}

/**
 * Every string literal that reaches a `className`, including the ones passed as
 * arguments to `cn(...)` inside a `className={…}` expression.
 *
 * A class list held in a module constant and referenced by identifier is not
 * covered. That is a deliberate limit rather than an oversight: chasing an
 * identifier needs a parser, and the classes this guard exists for are written
 * inline at the element.
 */
function classNameLiterals(source: string): string[] {
  const literals: string[] = []
  for (const match of source.matchAll(/className=/g)) {
    const start = (match.index ?? 0) + match[0].length
    const opener = source[start]
    if (opener === '"' || opener === "'") {
      const end = source.indexOf(opener, start + 1)
      if (end > start) literals.push(source.slice(start + 1, end))
      continue
    }
    if (opener !== '{') continue
    let depth = 0
    for (let i = start; i < source.length; i++) {
      const char = source[i]
      if (char === '{') {
        depth++
      } else if (char === '}') {
        depth--
        if (depth === 0) break
      } else if (char === '"' || char === "'" || char === '`') {
        const end = source.indexOf(char, i + 1)
        if (end < 0) break
        // `message.role === 'user' ? 'ml-auto' : 'border-border'` puts a value
        // in the same expression as the classes it chooses between. The
        // operand is not a class and must not be judged as one.
        if (!/(?:===|!==|==|!=)\s*$/.test(source.slice(Math.max(0, i - 8), i))) {
          literals.push(source.slice(i + 1, end))
        }
        i = end
      }
    }
  }
  return literals
}

/** `focus:not-sr-only` → `sr`, `hover:bg-danger/30` → `bg`, `h-(--x)` → `h`. */
function utilityRoot(token: string): string {
  const bare = token.replace(/^!/, '').replace(/^-/, '')
  return bare.split(/[-[(/]/, 1)[0] ?? ''
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function classTokens(literal: string): string[] {
  return literal
    // A `${…}` placeholder is a value, not a class name.
    .replace(/\$\{[^}]*\}/g, ' ')
    .split(/\s+/)
    .filter(Boolean)
    // Strip every variant prefix: `sm:focus:underline` → `underline`.
    .map((token) => token.replace(/^(?:[\w-]+:)+/, ''))
    .filter(Boolean)
    /*
     * Anything carrying a bracket, an ampersand, a star or a parenthesis is
     * Tailwind's arbitrary-value or arbitrary-variant syntax — `[&_svg]:size-4`,
     * `data-[side=top]:slide-in-from-bottom-2`, `h-(--spacing-topbar)`. None of
     * it can be a plain class name, and the generated primitives in
     * components/ui are written almost entirely in it.
     */
    .filter((token) => !/[[\]&*()]/.test(token))
}

describe('class names', () => {
  const css = readFileSync('src/styles.css', 'utf8')

  it('resolve either as a Tailwind utility or as a rule in styles.css', () => {
    const orphans = new Map<string, string>()
    for (const path of sourceFiles('src')) {
      for (const literal of classNameLiterals(readFileSync(path, 'utf8'))) {
        for (const token of classTokens(literal)) {
          if (TAILWIND_ROOTS.has(utilityRoot(token))) continue
          if (new RegExp(`\\.${escapeRegExp(token)}(?![\\w-])`).test(css)) continue
          orphans.set(token, path.replace(/^src\//, ''))
        }
      }
    }

    expect(
      [...orphans].map(([token, path]) => `${token} (${path})`),
      'These class names style nothing: styles.css defines no rule for them and ' +
        'they are not Tailwind utilities. Add the rule to src/styles.css, or — if ' +
        'this is a Tailwind utility family the dashboard had not used before — add ' +
        'its root to TAILWIND_ROOTS in this file.',
    ).toEqual([])
  })
})
