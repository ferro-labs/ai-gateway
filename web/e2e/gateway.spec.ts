import { expect, test } from '@playwright/test'
import { GATEWAY_MASTER_KEY, GATEWAY_ORIGIN } from '../playwright.config'
import { recordRuntimeErrors } from './fixture'

/*
 * The one journey that runs against a real gateway.
 *
 * Every other spec in this directory answers `/admin`, `/v1` and `/readyz` from
 * `fixture.ts`, which is what makes them fast and deterministic — and what
 * makes them structurally unable to notice the Go API moving. A renamed field,
 * a changed status, a body that is a different shape on a 503 than on a 200:
 * the fixture keeps answering the old way and every assertion passes.
 *
 * So this file mocks nothing. `playwright.config.ts` starts `cmd/ferrogw` with
 * memory stores, one target and no route to the internet, and the browser talks
 * to it through the preview server's proxy exactly as the dashboard talks to a
 * gateway in development.
 *
 * Run it with:
 *
 *     FERROGW_E2E_GATEWAY=1 npm run test:e2e --prefix web
 *
 * What it is for is the *contract*, not breadth. Where an assertion could be
 * written either against a string this file made up or against what the gateway
 * actually reports, it is written against the gateway: the expectations below
 * are read back from `/admin/health` and `/admin/keys` over the same HTTP the
 * page uses. The exception is the handful of facts that are true by
 * construction — one target, one provider, a ready gateway — because those are
 * what a renamed field silently turns into a dash, a NaN or a red pill, and an
 * expectation derived from the same renamed field would move with it and pass.
 */

/** Named for the run, so the row asserted below is the one this test created. */
const KEY_NAME = 'Contract journey key'

test('the dashboard and a real gateway agree on what the API sends', async ({ page, request }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'a contract holds in one viewport')
  const errors = recordRuntimeErrors(page)

  // --- Sign in -------------------------------------------------------------
  // Against the real POST /admin/session: the master key is exchanged for a
  // session token, and everything after this point is authenticated with it.
  await page.goto('/login')
  await page.getByLabel('Gateway key').fill(GATEWAY_MASTER_KEY)
  await page.getByRole('button', { name: 'Continue' }).click()
  await expect(page).toHaveURL(/\/overview$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()

  // --- A read page ---------------------------------------------------------
  const summary = page.getByRole('region', { name: 'Gateway summary' })
  // One target naming one registered provider, from GET /admin/dashboard. The
  // negative half is the load-bearing one: a renamed count reaches
  // `formatNumber(undefined)` and renders, without throwing, as NaN.
  await expect(summary).toContainText('/ 1 registered')
  await expect(summary).not.toContainText('NaN')

  const healthPanel = page.getByRole('region', { name: 'Gateway health' })
  // Node-side, against the gateway itself: the expectations below are what it
  // reports, not what this file assumes it reports.
  const session = await request.post(`${GATEWAY_ORIGIN}/admin/session`, {
    headers: { Authorization: `Bearer ${GATEWAY_MASTER_KEY}` },
  })
  expect(session.status(), 'POST /admin/session mints a dashboard session').toBe(201)
  const { token } = await session.json() as { token: string }
  const adminHealth = await request.get(`${GATEWAY_ORIGIN}/admin/health`, {
    headers: { Authorization: `Bearer ${token}` },
  }).then((response) => response.json()) as { components?: Array<{ name: string }> }

  // Two assertions, because either alone can be satisfied by a broken page: the
  // panel must render components at all — a renamed `components` key empties
  // both the page and the expectation below — and every component this gateway
  // reports must be one of the rows on screen.
  await expect(healthPanel.getByRole('listitem')).not.toHaveCount(0)
  for (const component of adminHealth.components ?? []) {
    await expect(healthPanel.getByRole('listitem').filter({ hasText: component.name })).toHaveCount(1)
  }

  // Readiness is a different body on a 503 than on a 200, which is the shape
  // change most likely to pass a mocked suite. This gateway is ready.
  await expect(healthPanel.getByText('Serving traffic')).toBeVisible()
  await expect(healthPanel).not.toContainText('Not ready')
  await expect(healthPanel).not.toContainText('Readiness could not be read')

  await page.getByRole('link', { exact: true, name: 'Providers' }).click()
  await expect(page.getByRole('heading', { level: 3, name: 'openai' })).toBeVisible()

  // --- One mutation --------------------------------------------------------
  // Created through the page, read back twice: from the table, which re-reads
  // GET /admin/keys, and from the store over HTTP. A create that never left the
  // browser satisfies neither.
  await page.getByRole('link', { exact: true, name: 'API Keys' }).click()
  await page.getByRole('button', { name: 'Create key' }).click()
  await page.getByPlaceholder('Production application').fill(KEY_NAME)
  await page.getByRole('button', { exact: true, name: 'Create key' }).last().click()

  const created = page.getByRole('dialog', { name: 'API key created' })
  await expect(created).toBeVisible()
  await expect(created.getByText(/^fgw_/)).toBeVisible()
  await created.getByRole('button', { name: 'Done' }).click()
  await expect(created).toBeHidden()

  await expect(page.getByRole('region', { name: 'Gateway keys' })
    .getByRole('row').filter({ hasText: KEY_NAME })).toHaveCount(1)

  const stored = await request.get(`${GATEWAY_ORIGIN}/admin/keys`, {
    headers: { Authorization: `Bearer ${token}` },
  }).then((response) => response.json()) as Array<{ name: string }>
  expect(stored.map((key) => key.name)).toContain(KEY_NAME)

  // A response the page could not read at all usually surfaces here first.
  expect(errors).toEqual([])
})
