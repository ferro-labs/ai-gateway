import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Logo } from './Logo'

describe('Logo', () => {
  it('names the product once, though both theme variants sit in the document', () => {
    const { container } = render(<Logo />)

    // The CSS swap needs both files present; only the one a reader hears is named.
    expect(container.querySelectorAll('img')).toHaveLength(2)
    expect(screen.getByRole('img', { name: 'Ferro Labs AI Gateway' })).toBeInTheDocument()
    expect(screen.getAllByRole('img')).toHaveLength(1)
  })

  it('gives both variants a source, so the swap never lands on an empty frame', () => {
    const { container } = render(<Logo size="lg" />)

    const sources = [...container.querySelectorAll('img')].map((img) => img.getAttribute('src'))
    expect(sources.every((src) => Boolean(src))).toBe(true)
    // Two different files, not the same one twice — the negative mark is what
    // makes the wordmark legible on a dark sidebar.
    expect(new Set(sources).size).toBe(2)
  })
})
