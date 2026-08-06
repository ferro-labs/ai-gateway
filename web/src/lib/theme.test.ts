import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { memoryStorage, stubMatchMedia } from '../test/stubs'

const STORAGE_KEY = 'ferrogw.dashboard.theme'

/**
 * The module subscribes to the media query once per load, so each test takes a
 * fresh copy — otherwise the listener from the first test is still attached to
 * the first test's fake and the rest measure nothing.
 */
async function loadTheme() {
  vi.resetModules()
  return import('./theme')
}


describe('theme', () => {
  beforeEach(() => {
    vi.stubGlobal('localStorage', memoryStorage())
    delete document.documentElement.dataset.theme
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('reports "system" until the operator makes a choice', async () => {
    stubMatchMedia(true)
    const { preferredTheme, resolveTheme } = await loadTheme()

    expect(preferredTheme()).toBe('system')
    expect(resolveTheme('system')).toBe('dark')
  })

  it('does not persist the operating system preference on boot', async () => {
    stubMatchMedia(true)
    const { applyTheme, preferredTheme } = await loadTheme()

    // Exactly what main.tsx does on every load. Writing here would freeze the
    // operating system's current preference in as an explicit choice.
    applyTheme(preferredTheme())

    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    expect(preferredTheme()).toBe('system')
  })

  it('follows a later operating-system switch while no choice is stored', async () => {
    const media = stubMatchMedia(false)
    const { applyTheme, preferredTheme } = await loadTheme()
    applyTheme(preferredTheme())
    expect(document.documentElement.dataset.theme).toBe('light')

    media.matches = true
    media.emit()

    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('ignores the operating system once a theme is chosen', async () => {
    const media = stubMatchMedia(false)
    const { saveTheme } = await loadTheme()
    saveTheme('dark')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')

    media.matches = true
    media.emit()
    expect(document.documentElement.dataset.theme).toBe('dark')

    media.matches = false
    media.emit()
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('hands the decision back when "system" is chosen again', async () => {
    const media = stubMatchMedia(false)
    const { saveTheme } = await loadTheme()
    saveTheme('dark')

    saveTheme('system')

    // Cleared rather than stored as the word, so the next boot reads it as no
    // preference and consults the operating system.
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    expect(document.documentElement.dataset.theme).toBe('light')

    media.matches = true
    media.emit()
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('restores a stored choice on the next boot', async () => {
    stubMatchMedia(true)
    localStorage.setItem(STORAGE_KEY, 'light')
    const { applyTheme, preferredTheme } = await loadTheme()

    expect(preferredTheme()).toBe('light')
    applyTheme(preferredTheme())
    expect(document.documentElement.dataset.theme).toBe('light')
  })
})
