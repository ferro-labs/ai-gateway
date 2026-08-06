import logoDark from '../assets/logo-negative.png'
import logoLight from '../assets/logo.png'
import { cn } from '@/lib/utils'

/**
 * The wordmark, in the variant the current theme needs.
 *
 * Both files are rendered and one is hidden by the theme variant, rather than
 * picking a source from React state. The stylesheet already keys off the theme
 * attribute, so a CSS swap needs no prop threading, no re-render, and cannot
 * disagree with the surrounding colours during the frame a state update lands.
 *
 * The two files are not interchangeable at the same height. Their ink is
 * identical — 127px tall in both — but their transparent padding is not: it is
 * 77.4% of the canvas in the light file and 93.4% in the negative one. Rendered
 * at one height the negative mark would appear about a fifth larger, so each
 * variant gets its own height chosen to put the *ink* at the same size. The
 * pairs below are derived from those measurements; re-measure if either file is
 * re-exported.
 */
const SIZES = {
  // ink ≈ 26px
  sm: { light: 'h-[34px]', dark: 'h-[28px]' },
  // ink ≈ 37px
  lg: { light: 'h-[48px]', dark: 'h-[40px]' },
} as const

export function Logo({
  size = 'sm',
  className,
}: {
  size?: keyof typeof SIZES
  className?: string
}) {
  const heights = SIZES[size]
  return (
    <>
      <img
        alt="Ferro Labs AI Gateway"
        className={cn('w-auto max-w-none shrink-0 dark:hidden', heights.light, className)}
        src={logoLight}
      />
      <img
        // The alt text belongs to the visible one only; announcing both would
        // read the product name twice.
        alt=""
        aria-hidden="true"
        className={cn('hidden w-auto max-w-none shrink-0 dark:block', heights.dark, className)}
        src={logoDark}
      />
    </>
  )
}
