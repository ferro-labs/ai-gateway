/**
 * `system` is a real choice, not the absence of one.
 *
 * Storage holds only `light` or `dark`, and only after the operator picks one.
 * An empty slot means `system`, which is why nothing writes on boot: applying
 * the operating system's current preference used to persist it as though it had
 * been chosen, and from then on the media query was never consulted again — the
 * console stayed light through every sunset, permanently.
 */
export type Theme = 'light' | 'dark' | 'system'

/** What actually reaches the document. `system` resolves to one of these. */
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'ferrogw.dashboard.theme'
const DARK_QUERY = '(prefers-color-scheme: dark)'

/** The stored choice, or `system` when the operator has never made one. */
export function preferredTheme(): Theme {
  const saved = localStorage.getItem(STORAGE_KEY)
  return saved === 'light' || saved === 'dark' ? saved : 'system'
}

export function resolveTheme(theme: Theme): ResolvedTheme {
  if (theme !== 'system') return theme
  return window.matchMedia(DARK_QUERY).matches ? 'dark' : 'light'
}

/**
 * The mode the document is currently displaying, so the media listener below
 * knows whether an operating-system change is its business.
 */
let active: Theme = 'system'
let watching = false

function watchSystem(): void {
  if (watching) return
  watching = true
  window.matchMedia(DARK_QUERY).addEventListener('change', () => {
    // A stored choice outranks the operating system; only `system` follows it.
    if (active === 'system') document.documentElement.dataset.theme = resolveTheme('system')
  })
}

/**
 * Writes the theme to the document, and nothing else.
 *
 * Applied once at startup before React mounts, so the sign-in page — which the
 * application shell does not wrap — honours the operator's theme and nothing
 * flashes in the wrong one. Storage is deliberately untouched here: this runs
 * on every boot, including boots where the operator has expressed no
 * preference, and a write on that path is what destroys `system`.
 */
export function applyTheme(theme: Theme): void {
  active = theme
  document.documentElement.dataset.theme = resolveTheme(theme)
  watchSystem()
}

/**
 * Records an explicit choice and applies it.
 *
 * Choosing `system` clears the slot rather than storing the word, so the next
 * boot reads "no preference" and follows the operating system again.
 */
export function saveTheme(theme: Theme): void {
  if (theme === 'system') localStorage.removeItem(STORAGE_KEY)
  else localStorage.setItem(STORAGE_KEY, theme)
  applyTheme(theme)
}
