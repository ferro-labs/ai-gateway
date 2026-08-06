import { expect, test } from '@playwright/test'
import { GATEWAY_MASTER_KEY, GATEWAY_ORIGIN } from '../playwright.config'
import { recordRuntimeErrors } from './fixture'

const LIVE_MODEL = 'gpt-4o-mini'

test('the Playground streams from a live provider and reflects config rollback', async ({ page, request }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'the bounded live journey runs in one viewport')
  const errors = recordRuntimeErrors(page)

  await page.goto('/login')
  await page.getByLabel('Gateway key').fill(GATEWAY_MASTER_KEY)
  await page.getByRole('button', { name: 'Continue' }).click()
  await expect(page).toHaveURL(/\/overview$/)

  await page.getByRole('link', { exact: true, name: 'Playground' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Playground' })).toBeVisible()
  await page.getByRole('combobox', { name: 'Model' }).click()
  await page.getByRole('option', { exact: true, name: LIVE_MODEL }).click()
  await page.getByLabel('Max tokens').fill('16')
  await page.getByLabel('Message').fill('Say exactly: pong')
  await page.getByRole('button', { exact: true, name: 'Send' }).click()

  const assistant = page.locator('article').filter({ hasText: 'Assistant' }).last()
  await expect(assistant).toContainText(/pong/i, { timeout: 30_000 })
  await expect(page.getByText(/input tokens$/)).toBeVisible()
  await expect(page.getByText(/output tokens$/)).toBeVisible()
  await page.screenshot({
    path: '/tmp/goal21-playground.png',
    fullPage: false,
    mask: [assistant.locator('p')],
  })

  const session = await request.post(`${GATEWAY_ORIGIN}/admin/session`, {
    headers: { Authorization: `Bearer ${GATEWAY_MASTER_KEY}` },
  })
  expect(session.status()).toBe(201)
  const { token } = await session.json() as { token: string }
  const headers = { Authorization: `Bearer ${token}` }
  const openAIConfig = {
    strategy: { mode: 'single' },
    targets: [{ virtual_key: 'openai', models: [LIVE_MODEL] }],
  }
  const unavailableConfig = {
    strategy: { mode: 'single' },
    targets: [{ virtual_key: 'groq', models: [LIVE_MODEL] }],
  }
  expect((await request.put(`${GATEWAY_ORIGIN}/admin/config`, { headers, data: openAIConfig })).status()).toBe(200)
  expect((await request.put(`${GATEWAY_ORIGIN}/admin/config`, { headers, data: unavailableConfig })).status()).toBe(200)
  expect((await request.get(`${GATEWAY_ORIGIN}/readyz`)).status()).toBe(503)

  await page.getByRole('link', { exact: true, name: 'Configuration' }).click()
  const activeConfig = page.getByRole('group', { name: 'Active gateway configuration' })
  await expect(activeConfig).toContainText('groq')

  const rollback = await request.post(`${GATEWAY_ORIGIN}/admin/config/rollback/1`, { headers })
  expect(rollback.status()).toBe(200)
  await expect.poll(
    async () => (await request.get(`${GATEWAY_ORIGIN}/readyz`)).status(),
    { timeout: 5_000 },
  ).toBe(200)
  await page.getByRole('button', { name: 'Refresh configuration' }).click()
  await expect(activeConfig).toContainText('openai')
  await expect(activeConfig).not.toContainText('groq')

  const history = await request.get(`${GATEWAY_ORIGIN}/admin/config/history`, { headers })
  expect(history.status()).toBe(200)
  const historyBody = await history.json() as {
    data: Array<{ version: number, rolled_back_from?: number }>
  }
  expect(historyBody.data).toHaveLength(3)
  expect(historyBody.data.at(-1)?.rolled_back_from).toBe(2)
  await page.screenshot({ path: '/tmp/goal21-config-rollback.png', fullPage: false })

  expect(errors).toEqual([])
})
