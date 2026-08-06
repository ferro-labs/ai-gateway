import { expect, test, type Page } from '@playwright/test'
import { adminToken, expectAccessible, mockGateway, readOnlyToken, recordRuntimeErrors, signIn } from './fixture'

/** Every route reachable from the navigation, checked in both themes. */
const ACCESSIBILITY_ROUTES = [
  ['Overview', 'overview'],
  ['Getting Started', 'getting-started'],
  ['API Keys', 'keys'],
  ['Request Logs', 'logs'],
  ['Audit Trail', 'audit'],
  ['Providers', 'providers'],
  ['Routing Strategies', 'strategy'],
  ['Plugins', 'plugins'],
  ['Configuration', 'config'],
  ['Analytics', 'analytics'],
  ['Playground', 'playground'],
] as const

/**
 * Reaches the dark theme from a fresh profile.
 *
 * The control cycles system → light → dark so an operator who once picked a
 * theme can hand the decision back to the operating system, which is why this
 * takes two presses rather than one.
 */
async function useDarkTheme(page: Page) {
  const themeToggle = page.getByRole('button', { name: /^Theme:/ })
  await themeToggle.click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await themeToggle.click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
}

/*
 * Every route, in both themes, one scan per test.
 *
 * Contrast and labelling regressions arrive with whichever page was edited, so
 * checking only the landing page found none of the fifty-six failures a palette
 * change introduced across the rest — and checking only the light theme missed
 * two more, both composites the component library derives at runtime (a colour
 * over a 10% tint of itself, and text at 60% opacity) that were written down
 * nowhere to be checked by hand.
 *
 * This was two tests, each sweeping all eleven routes in a loop: the light
 * sweep folded into the desktop workflow test below, and a dark sweep of its
 * own. An axe scan costs about a second, so both sat at 23s and 21s of a 30s
 * timeout — and a test that spends most of its budget does not fail when it
 * regresses, it fails when the machine is busy. Under whole-suite load they
 * crossed, which made a real regression indistinguishable from the noise.
 *
 * Splitting per route is what removes the pressure rather than hiding it: no
 * test runs more than one scan, so none is near its timeout, they fill the
 * worker pool instead of serialising behind each other, and a failure names the
 * route and the theme instead of the sweep that happened to contain them.
 */
test.describe('route accessibility', () => {
  // Desktop only, as the sweep has always been: the mobile project covers its
  // own layout in the responsiveness test below.
  test.beforeEach(() => {
    test.skip(test.info().project.name !== 'desktop-chromium')
  })

  for (const theme of ['light', 'dark'] as const) {
    for (const [label, path] of ACCESSIBILITY_ROUTES) {
      test(`${label} is accessible in the ${theme} theme`, async ({ page }) => {
        await mockGateway(page, ['admin'], adminToken)
        await signIn(page)
        if (theme === 'dark') await useDarkTheme(page)

        // Reached by its navigation link rather than by goto, so the link that
        // claims to serve this route is what is proven to reach it.
        await page.getByRole('link', { exact: true, name: label }).click()
        await expect(page).toHaveURL(new RegExp('/' + path + '$'))
        await expect(page.getByRole('heading', { level: 1, name: label })).toBeVisible()

        await expectAccessible(page)
      })
    }
  }
})

/*
 * A tab panel is a route the navigation cannot reach, so it is swept on its own
 * rather than left out of the accessibility check entirely.
 *
 * The matrix is the widest table in the console — nineteen parameter columns
 * overflow their container horizontally — so it is where `scrollable-region-focusable`
 * (WCAG 2.1.1 / 2.1.3) bites first: without a focusable scroll container the
 * providers past the right edge do not exist for a keyboard-only operator.
 */
test('the capabilities matrix can be scrolled by keyboard', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/providers')
  await page.getByRole('tab', { name: 'Capabilities' }).click()
  await expect(page.getByRole('heading', { name: 'Chat parameter support' })).toBeVisible()
  await expectAccessible(page)
})

test('desktop operator workflows are usable and stable', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium')
  const errors = recordRuntimeErrors(page)
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)

  await expect(page.getByText('18,432', { exact: true }).first()).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Requests over time' })).toBeVisible()
  await page.screenshot({ path: '/tmp/ferrogw-dashboard-desktop.png' })

  await page.getByRole('link', { exact: true, name: 'Playground' }).click()
  await page.getByLabel('Message').fill('Confirm this gateway route.')
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.getByText('Gateway route is healthy.')).toHaveCount(1)

  await page.getByRole('link', { exact: true, name: 'API Keys' }).click()
  await page.getByRole('button', { name: 'Create key' }).click()
  await expect(page.getByPlaceholder('Production application')).toBeFocused()
  await page.getByPlaceholder('Production application').fill('Browser validation')
  await page.getByRole('button', { exact: true, name: 'Create key' }).last().click()
  await expect(page.getByRole('heading', { name: 'API key created' })).toBeVisible()
  await expect(page.getByText('fgw_live_newsecretvalue')).toBeVisible()
  const createdDialog = page.getByRole('dialog', { name: 'API key created' })
  // Focus lands on the action the dialog exists for. The secret is shown once,
  // so copying it is the task, not dismissing it.
  await expect(createdDialog.getByRole('button', { name: 'Copy API key' })).toBeFocused()
  // The dialog refuses a casual dismissal: Escape and a click outside would
  // otherwise destroy the only copy of a key the gateway cannot show again.
  await page.keyboard.press('Escape')
  await expect(createdDialog).toBeVisible()
  await page.mouse.click(5, 5)
  await expect(createdDialog).toBeVisible()
  await page.getByRole('button', { name: 'Done' }).click()
  await expect(createdDialog).toBeHidden()

  expect(await page.evaluate(() =>
    document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
  expect(errors).toEqual([])
})

test('mobile navigation and provider details remain responsive', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile-chromium')
  const errors = recordRuntimeErrors(page)
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)

  await page.screenshot({ path: '/tmp/ferrogw-dashboard-mobile.png' })
  await page.getByRole('button', { name: 'Open navigation' }).click()
  const mobileDrawer = page.getByRole('dialog', { name: 'Mobile navigation' })
  await expect(mobileDrawer).toBeVisible()
  // Focus enters the drawer at its first destination rather than at dismiss.
  await expect(mobileDrawer.getByRole('link', { name: 'Getting Started' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Mobile navigation' })).toBeHidden()

  await page.getByRole('button', { name: 'Open navigation' }).click()
  await page.getByRole('dialog', { name: 'Mobile navigation' })
    .getByRole('link', { exact: true, name: 'Providers' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Providers' })).toBeVisible()
  await page.getByRole('button', { name: 'Show openai models' }).click()
  await expect(page.getByText('gpt-4.1-mini', { exact: true })).toBeVisible()
  await page.screenshot({ path: '/tmp/ferrogw-dashboard-mobile-providers.png' })
  await expectAccessible(page)

  expect(await page.evaluate(() =>
    document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
  expect(errors).toEqual([])
})

test('read-only sessions cannot access mutation controls', async ({ page }) => {
  await mockGateway(page, ['read_only'], readOnlyToken)
  await signIn(page, readOnlyToken)
  await page.goto('/keys')
  await expect(page.getByText(/Read-only sessions can inspect keys/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create key' })).toHaveCount(0)
  // GET /admin/sessions is readable on a read-only scope (the list itself
  // renders), but DELETE is admin-only, so the destructive control is absent.
  await expect(page.getByRole('heading', { name: 'Active dashboard sessions' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Sign out all sessions' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Edit / })).toHaveCount(0)
  await page.goto('/config')
  await expect(page.getByText(/cannot apply or roll back changes/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Edit JSON' })).toHaveCount(0)
})

test('runtime configuration can target a separate gateway origin', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium')
  await mockGateway(page, ['admin'], adminToken)
  await page.route('**/config.json', async (route) => {
    await route.fulfill({
      body: JSON.stringify({ gatewayBaseUrl: 'https://gateway.example.test' }),
      contentType: 'application/json',
      status: 200,
    })
  })

  const gatewayRequests: string[] = []
  page.on('request', (request) => {
    if (request.url().startsWith('https://gateway.example.test/')) {
      gatewayRequests.push(request.url())
    }
  })

  await signIn(page)
  expect(gatewayRequests).toContain('https://gateway.example.test/admin/health')
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
})

test('a failed stream is reported, not presented as a finished answer', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium')
  await mockGateway(page, ['admin'], adminToken, { streamFailure: true })
  await signIn(page)
  await page.goto('/playground')

  await page.getByLabel('Message').fill('Does this gateway route work?')
  await page.getByRole('button', { name: 'Send' }).click()

  // The gateway reports a mid-stream failure as a normal data frame on an
  // already-200 response and then closes without [DONE]. Read as a completion,
  // that looks like the model stopping mid-sentence — so on the one page whose
  // job is diagnosing the gateway, an upstream 429 became a plausible answer.
  await expect(page.getByText(/upstream provider returned 429/)).toBeVisible()

  // The truncated text must not be left standing as a completed assistant turn.
  await expect(page.getByText('Partial ans', { exact: true })).toHaveCount(0)
})

test('dashboard sessions are listed, and signing out all of them ends this one too', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  // Scoped to the sessions region: "Operations observer" is also a key name
  // in the table above, and an unscoped query would be ambiguous between them.
  const sessionsSection = page.getByRole('region', { name: 'Active dashboard sessions' })
  await expect(sessionsSection).toBeVisible()
  // By row: the observer's name appears twice within it — once as the subject,
  // once as the credential the session was minted from.
  await expect(sessionsSection.getByRole('row').filter({ hasText: 'Operations observer' })).toHaveCount(1)

  // Confirmed before it fires: opening the dialog must not itself sign anyone out.
  await page.getByRole('button', { name: 'Sign out all sessions' }).click()
  const confirm = page.getByRole('alertdialog', { name: 'Sign out all dashboard sessions?' })
  await expect(confirm).toBeVisible()

  await confirm.getByRole('button', { name: 'Sign out everyone' }).click()

  // The DELETE kills this browser's own session row too. The keys/sessions
  // refetch that follows now 401s, and the app's existing SESSION_EXPIRED_EVENT
  // handling bounces to sign-in — the same path any other expired session
  // takes, not a special case built for this button.
  await expect(page).toHaveURL(/\/login$/)
})

test("a key's expiry can be extended from the Edit dialog", async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  await page.getByRole('button', { name: 'Edit Production application' }).click()
  const dialog = page.getByRole('dialog', { name: 'Edit API key' })
  await expect(dialog.getByLabel('Name')).toHaveValue('Production application')

  await dialog.getByRole('combobox', { name: 'Expires' }).click()
  await page.getByRole('option', { name: 'Never expires', exact: true }).click()
  await dialog.getByRole('button', { name: 'Save changes' }).click()

  await expect(dialog).toBeHidden()
  await expect(page.getByText('Production application was updated.')).toBeVisible()
})

test('a refused key edit is shown in the dialog, not silently dropped', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  await page.getByRole('button', { name: 'Edit Locked credential' }).click()
  const dialog = page.getByRole('dialog', { name: 'Edit API key' })
  await dialog.getByRole('button', { name: 'Save changes' }).click()

  // The 409 is readable inside the still-open dialog, not lost behind its
  // backdrop or indistinguishable from the button simply doing nothing.
  await expect(dialog.getByText(/cannot remove the last admin key/)).toBeVisible()
  await expect(dialog).toBeVisible()
})
