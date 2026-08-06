import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { BRAND_TEXT, PROVIDER_MARKS } from './provider-marks'
import { ProviderLogo } from './ProviderLogo'

function markup(provider: string): string {
  const { container } = render(<ProviderLogo provider={provider} />)
  return container.innerHTML
}

describe('ProviderLogo', () => {
  it('draws the vendored artwork for a provider it carries a mark for', () => {
    const { container } = render(<ProviderLogo provider="openai" />)

    expect(container.querySelector('svg')).not.toBeNull()
  })

  it('falls back to initials rather than to a blank tile', () => {
    // A gateway may register a provider released after this build. An empty
    // square beside its name reads as a broken image.
    render(<ProviderLogo provider="databricks" />)

    expect(screen.getByText('DA')).toBeInTheDocument()
  })

  it('reads a separated id as initials, not as a truncation', () => {
    render(<ProviderLogo provider="acme-labs" />)

    expect(screen.getByText('AL')).toBeInTheDocument()
  })

  it('renders something for a provider named nothing at all', () => {
    render(<ProviderLogo provider="" />)

    expect(screen.getByText('?')).toBeInTheDocument()
  })

  it('stays out of the accessibility tree, since every use sits beside the name', () => {
    // Announcing the mark as well would read the provider's name twice on every
    // row of a thirty-provider page.
    expect(markup('openai')).toContain('aria-hidden="true"')
    expect(markup('databricks')).toContain('aria-hidden="true"')
  })
})

describe('PROVIDER_MARKS', () => {
  it('tints only the artwork that does not paint itself', () => {
    // A brand mark carrying its own fills must never be recoloured, and a
    // single-colour mark inherits currentColor so a brand colour can be applied.
    expect(PROVIDER_MARKS.openai?.brand).toBe(false)
    expect(PROVIDER_MARKS.gemini?.brand).toBe(true)

    const { container } = render(<ProviderLogo provider="openai" />)
    expect(container.querySelector('svg')).toHaveAttribute('fill', 'currentColor')

    const brand = render(<ProviderLogo provider="gemini" />).container
    expect(brand.querySelector('svg')).not.toHaveAttribute('fill')
  })

  it('paints a single-colour mark in the vendor brand colour, not the page foreground', () => {
    // groq's mark is monochrome, so it inherits currentColor — which the brand
    // class must set to groq's orange rather than leave at the foreground.
    expect(PROVIDER_MARKS.groq?.brand).toBe(false)
    const brandClass = BRAND_TEXT.groq
    expect(brandClass).toBeTruthy()

    const { container } = render(<ProviderLogo provider="groq" />)
    const svg = container.querySelector('svg')
    expect(svg?.getAttribute('class')).toContain(brandClass)
    expect(svg?.getAttribute('class')).not.toContain('text-foreground')
  })

  it('gives every mark a viewBox, without which the artwork does not scale', () => {
    for (const [provider, mark] of Object.entries(PROVIDER_MARKS)) {
      expect(mark.viewBox, `${provider} has no viewBox`).toMatch(/^[\d.\-\s]+$/)
    }
  })
})
