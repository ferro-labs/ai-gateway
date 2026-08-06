import { readFileSync } from 'node:fs'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ErrorBoundary } from './ErrorBoundary'

function Boom(): never {
  throw new Error('render blew up')
}

/*
 * React prints the caught error to `console.error` on its way through the
 * boundary, and so does the boundary itself. Both are expected here and would
 * otherwise bury a real failure in the suite output. Anything else still gets
 * through — a suppressed unexpected error is a lost bug.
 */
const EXPECTED = ['Dashboard render failed', 'The above error occurred', 'An error occurred in the']

beforeEach(() => {
  const original = console.error.bind(console)
  vi.spyOn(console, 'error').mockImplementation((...args: unknown[]) => {
    const message = args.map((arg) => (typeof arg === 'string' ? arg : '')).join(' ')
    if (EXPECTED.some((expected) => message.includes(expected))) return
    original(...args)
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ErrorBoundary', () => {
  it('leaves a working child alone', () => {
    render(
      <ErrorBoundary>
        <h1>Overview</h1>
      </ErrorBoundary>,
    )

    expect(screen.getByRole('heading', { name: 'Overview' })).toBeInTheDocument()
  })

  it('shows a recovery screen rather than a blank page when a page throws mid-render', () => {
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )

    // `alert` so a screen reader is told the screen changed under it, and the
    // text has to say what happened — a blank white page says nothing.
    const fallback = screen.getByRole('alert')
    expect(fallback).toHaveTextContent('This screen could not be rendered.')
    expect(fallback).toHaveTextContent('check the browser console')
  })

  it('offers a reload, the only way back once the tree is unmountable', async () => {
    const reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })
    const user = userEvent.setup()
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )

    await user.click(screen.getByRole('button', { name: 'Reload dashboard' }))

    expect(reload).toHaveBeenCalled()
    vi.unstubAllGlobals()
  })

  /*
   * This screen is only ever seen when the application is already broken, so
   * nothing else exercises its styling. Every class it uses shipped for a while
   * with no rule behind it at all: the page shown when the dashboard cannot
   * start was itself unstyled, and no test noticed.
   */
  it('is styled by rules styles.css actually defines', () => {
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )
    const css = readFileSync('src/styles.css', 'utf8')

    const fallback = screen.getByRole('alert')
    const button = screen.getByRole('button', { name: 'Reload dashboard' })
    expect(fallback).toHaveClass('fatal-error')
    expect(screen.getByText('Dashboard error')).toHaveClass('eyeless-label')
    expect(button).toHaveClass('button', 'button-primary')

    for (const rule of ['.fatal-error', '.eyeless-label', '.button', '.button-primary']) {
      expect(css, `styles.css defines no ${rule} rule`).toMatch(
        new RegExp(`\\${rule}(?![\\w-])`),
      )
    }
  })
})
