import { expect, test } from '@playwright/test'
import { adminToken, mockGateway, signIn } from './fixture'

/**
 * Visual baselines for every route.
 *
 * These exist to catch silent style drift. A CSS reset that changes which rule
 * wins on a bare element selector produces no compile error, no failing unit
 * test, and no console warning — it just renders differently. Snapshots are the
 * only thing that fails when that happens.
 *
 * Captured against the stylesheet in place at the time of writing, so they are
 * a record of intended appearance rather than merely of current appearance.
 *
 * The Playwright config rebuilds before serving and never reuses a running
 * server, so these always run against the current source. That is enforced
 * there rather than relied on here.
 */

const ROUTES = [
  'overview',
  'getting-started',
  'keys',
  'logs',
  'audit',
  'providers',
  'strategy',
  'plugins',
  'config',
  'analytics',
  'playground',
] as const

test.describe('visual baselines', () => {
  for (const route of ROUTES) {
    test(route, async ({ page }) => {
      await mockGateway(page, ['admin'], adminToken)
      await signIn(page)
      await page.goto(`/${route}`)
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
      // The traffic chart animates in; settle before capturing so the baseline
      // is not a race.
      await page.waitForLoadState('networkidle')
      await expect(page).toHaveScreenshot(`${route}.png`, {
        fullPage: true,
        // Timestamps and relative times ("2m ago") change between runs.
        mask: [page.locator('[data-volatile]')],
        maxDiffPixelRatio: 0.01,
      })
    })
  }

  test('sign-in', async ({ page }) => {
    await mockGateway(page, ['admin'], adminToken)
    await page.goto('/login')
    await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible()
    await expect(page).toHaveScreenshot('login.png', { fullPage: true, maxDiffPixelRatio: 0.01 })
  })

  test('create-key dialog', async ({ page }) => {
    await mockGateway(page, ['admin'], adminToken)
    await signIn(page)
    await page.goto('/keys')
    await page.getByRole('button', { name: 'Create key' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await expect(page).toHaveScreenshot('dialog-create-key.png', { maxDiffPixelRatio: 0.01 })
  })
})
