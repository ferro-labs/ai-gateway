import { cn } from '@/lib/utils'
import { BRAND_TEXT, PROVIDER_MARKS } from './provider-marks'

/**
 * A provider's brand mark on a neutral tile, or a lettermark when the gateway
 * registers a provider this build carries no artwork for.
 *
 * Always decorative: every use sits beside the provider's name, so announcing
 * the mark as well would read the name twice. Callers that need it standalone
 * must supply their own label.
 */

const TILE: Record<'sm' | 'md' | 'lg', string> = {
  sm: 'size-7 rounded-md',
  md: 'size-9 rounded-lg',
  lg: 'size-11 rounded-xl',
}

const GLYPH: Record<'sm' | 'md' | 'lg', string> = {
  sm: 'size-4',
  md: 'size-5',
  lg: 'size-6',
}

const LETTERS: Record<'sm' | 'md' | 'lg', string> = {
  sm: 'text-[10px]',
  md: 'text-xs',
  lg: 'text-sm',
}

/**
 * Two characters, skipping the separators in ids like `azure-openai` and
 * `nvidia-nim` so the mark reads as initials rather than as a truncation.
 */
function initials(provider: string): string {
  const words = provider.split(/[-_\s]+/).filter(Boolean)
  if (words.length === 0) return '?'
  if (words.length === 1) return (words[0] ?? '').slice(0, 2).toUpperCase()
  return `${words[0]?.[0] ?? ''}${words[1]?.[0] ?? ''}`.toUpperCase()
}

export function ProviderLogo({
  provider,
  size = 'md',
  className,
}: {
  provider: string
  size?: 'sm' | 'md' | 'lg'
  className?: string
}) {
  const mark = PROVIDER_MARKS[provider]
  // A light tile in both themes so a vendor's own colours — and the near-black
  // single-colour marks — stay legible; a dark tile swallowed both in dark mode.
  const tile = cn(
    'inline-flex shrink-0 items-center justify-center border border-border/70 bg-white',
    TILE[size],
    className,
  )

  if (!mark) {
    return (
      <span aria-hidden="true" className={cn(tile, 'font-semibold tracking-tight text-muted-foreground', LETTERS[size])}>
        {initials(provider)}
      </span>
    )
  }

  return (
    <span aria-hidden="true" className={tile}>
      <svg
        className={cn(GLYPH[size], !mark.brand && (BRAND_TEXT[provider] ?? 'text-foreground'))}
        fill={mark.brand ? undefined : 'currentColor'}
        viewBox={mark.viewBox}
        xmlns="http://www.w3.org/2000/svg"
      >
        {mark.art}
      </svg>
    </span>
  )
}
