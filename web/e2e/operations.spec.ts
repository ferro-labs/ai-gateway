import { expect, test, type Page } from '@playwright/test'
import { adminToken, auditEntries, capabilityParamCount, mockGateway, signIn } from './fixture'

/*
 * The irreversible half of the console.
 *
 * Rotate, revoke, delete, save, roll back and purge are the operations that
 * cannot be undone by clicking again, and none of them had a browser test: the
 * fixture answered every one of them with a 501, so the whole set was verified
 * by hand. Each test below asserts two things — what the operator is told, and
 * what actually reached the gateway — because a console that reports success on
 * a request it never sent is the specific failure these flows can hide.
 */

/** Confirms a ConfirmDialog and waits for it to be dismissed. */
async function confirm(page: Page, title: string, action: string) {
  const dialog = page.getByRole('alertdialog', { name: title })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: action }).click()
  return dialog
}

/*
 * The keys page carries two tables that name the same credentials: the keys
 * themselves, and the sessions minted from them. An unscoped row query matches
 * both, so every assertion about a key's row says which table it means.
 */
const keysTable = (page: Page) => page.getByRole('region', { name: 'Gateway keys' })
const keyRow = (page: Page, name: string) => keysTable(page).getByRole('row').filter({ hasText: name })

test('a rotated key reveals its new secret once and records when it changed', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  await page.getByRole('button', { name: 'Rotate Production application' }).click()
  await confirm(page, 'Rotate Production application?', 'Rotate key')

  // The gateway stores only the hash, so this dialog is the single moment the
  // new secret exists anywhere the operator can reach it.
  const revealed = page.getByRole('dialog', { name: 'API key rotated' })
  await expect(revealed).toBeVisible()
  await expect(revealed.getByText('fgw_live_rotatedsecret')).toBeVisible()
  // It refuses a casual dismissal for the same reason a created key does: a
  // stray Escape destroys the only copy and the key has to be rotated again.
  await page.keyboard.press('Escape')
  await expect(revealed).toBeVisible()
  await revealed.getByRole('button', { name: 'Done' }).click()
  await expect(revealed).toBeHidden()

  // A rotated key is a different secret under the same name, so the row has to
  // say when it changed — that is what tells a client that can no longer
  // authenticate from one that never could.
  await expect(keyRow(page, 'Production application')).toContainText('Rotated')
})

test('revoking a key takes it out of service and says since when', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  await page.getByRole('button', { name: 'Revoke Operations observer' }).click()
  await confirm(page, 'Revoke Operations observer?', 'Revoke key')

  await expect(page.getByText('Operations observer was revoked.')).toBeVisible()
  const row = keyRow(page, 'Operations observer')
  await expect(row).toContainText('Revoked')
  // The row stays: a revoked credential is evidence, and its usage history is
  // what an incident is reconstructed from.
  await expect(row.getByRole('button', { name: 'Revoke Operations observer' })).toBeDisabled()
  await expect(row.getByRole('button', { name: 'Rotate Operations observer' })).toBeDisabled()
})

test('a refused revoke is reported inside the confirmation, not behind it', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  await page.getByRole('button', { name: 'Revoke Locked credential' }).click()
  const dialog = await confirm(page, 'Revoke Locked credential?', 'Revoke key')

  // The 409 is readable where the operator is looking. A page-level notice sits
  // under this dialog's own backdrop, where a refusal is indistinguishable from
  // the button doing nothing at all.
  await expect(dialog.getByText(/cannot remove the last admin key/)).toBeVisible()
  await expect(dialog).toBeVisible()

  // And the key the gateway refused to revoke is still serving traffic.
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toBeHidden()
  await expect(keyRow(page, 'Locked credential')).toContainText('Active')
})

test('a deleted key is gone from the table and from the totals', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  const summary = page.getByText(/\d+ total · \d+ active/)
  await expect(summary).toContainText('3 total')

  await page.getByRole('button', { name: 'Delete Operations observer' }).click()
  await confirm(page, 'Delete Operations observer?', 'Delete key')

  await expect(page.getByText('Operations observer was deleted.')).toBeVisible()
  await expect(keyRow(page, 'Operations observer')).toHaveCount(0)
  // Re-read from the gateway rather than counted from the rows on screen: a
  // delete that only removed the row locally would leave this at three.
  await expect(summary).toContainText('2 total')
})

test('one dashboard session can be signed out without ending the rest', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/keys')

  const sessionsSection = page.getByRole('region', { name: 'Active dashboard sessions' })
  await sessionsSection.getByRole('button', { name: 'Revoke session for Operations observer' }).click()
  await confirm(page, 'Revoke the session for Operations observer?', 'Revoke session')

  await expect(page.getByText('Operations observer was signed out.')).toBeVisible()
  await expect(sessionsSection.getByText('Operations observer', { exact: true })).toHaveCount(0)
  // One stolen laptop costs one session. This browser's own sign-in is
  // untouched, so the page is still here rather than back at the login screen.
  await expect(page).toHaveURL(/\/keys$/)
  await expect(sessionsSection.getByRole('row').filter({ hasText: 'Production application' })).toHaveCount(1)
})

test('a saved configuration reaches the gateway and comes back as the active document', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/config')

  await page.getByRole('button', { name: 'Edit JSON' }).click()
  const editor = page.getByLabel('Gateway configuration JSON')
  await editor.fill(JSON.stringify({
    apiVersion: 'ferro.dev/v1',
    strategy: { mode: 'loadbalance' },
    targets: [{ virtual_key: 'openai' }, { virtual_key: 'anthropic' }],
  }, null, 2))
  await page.getByRole('button', { name: 'Save and apply' }).click()

  await expect(page.getByText('Configuration saved and applied.')).toBeVisible()
  // Read back from the gateway, not from the editor that was just typed into:
  // this is the assertion a save that never left the browser fails.
  await expect(page.getByRole('button', { name: 'Edit JSON' })).toBeVisible()
  await expect(page.getByText('"mode": "loadbalance"')).toBeVisible()
  // The gateway recorded it as a version, which is the only route back.
  await expect(page.getByRole('tab', { name: /History/ })).toContainText('3')
})

test('a save still carrying the redaction placeholder is refused before it leaves the browser', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/config')

  const puts: string[] = []
  page.on('request', (request) => {
    if (request.method() === 'PUT') puts.push(request.url())
  })

  await page.getByRole('button', { name: 'Edit JSON' }).click()
  const editor = page.getByLabel('Gateway configuration JSON')
  const withPlaceholder = {
    apiVersion: 'ferro.dev/v1',
    strategy: { mode: 'fallback' },
    targets: [{ virtual_key: 'openai' }],
    mcp_servers: [{ name: 'search', url: 'https://mcp.example.com/mcp', headers: { Authorization: '[REDACTED]' } }],
  }
  await editor.fill(JSON.stringify(withPlaceholder, null, 2))

  // Reads scrub secrets to "[REDACTED]", so an edit-and-resend of what was read
  // replaces a live credential with that literal text. Saying so here, while
  // the editor is still open, is the difference between a fixable mistake and a
  // gateway that has silently lost the token it authenticates a tool server with.
  await expect(page.getByText(/mcp_servers\[0\]\.headers still contains the "\[REDACTED\]" placeholder/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Save and apply' })).toBeDisabled()

  await editor.fill(JSON.stringify({
    ...withPlaceholder,
    mcp_servers: [{ name: 'search', url: 'https://mcp.example.com/mcp', headers: { Authorization: '${SEARCH_TOKEN}' } }],
  }, null, 2))
  await page.getByRole('button', { name: 'Save and apply' }).click()
  await expect(page.getByText('Configuration saved and applied.')).toBeVisible()

  // Exactly one save left the browser: the placeholder version never did.
  expect(puts).toHaveLength(1)
})

test('a rolled-back version is applied and named in the notice', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/config')

  await page.getByRole('tab', { name: /History/ }).click()
  const version2 = page.getByRole('row').filter({ hasText: 'v2' })
  await version2.getByRole('button', { name: 'Rollback' }).click()

  // The dialog names what changes before it is confirmed, because "roll back"
  // on its own does not say what an operator is about to lose.
  const dialog = page.getByRole('alertdialog', { name: 'Roll back to version 2?' })
  await expect(dialog).toContainText(/changes .* sections?: /)
  await dialog.getByRole('button', { name: 'Rollback configuration' }).click()

  await expect(page.getByText('Configuration rolled back to version 2.')).toBeVisible()
  await expect(page.getByRole('tab', { name: /History/ })).toContainText('3')
})

test('a refused rollback is reported inside the dialog rather than behind it', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/config')

  await page.getByRole('tab', { name: /History/ }).click()
  const version1 = page.getByRole('row').filter({ hasText: 'v1' })
  await version1.getByRole('button', { name: 'Rollback' }).click()
  const dialog = await confirm(page, 'Roll back to version 1?', 'Rollback configuration')

  // A rollback fails on apply, after the confirmation — the one point where the
  // page-level notice is covered by this dialog's own backdrop.
  await expect(dialog.getByText(/is not a registered provider/)).toBeVisible()
  await expect(dialog).toBeVisible()
  await expect(page.getByText(/Configuration rolled back/)).toHaveCount(0)
})

test('a log purge deletes before the operator\'s own midnight, scoped to the filters they confirmed', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  // Filtered first: the purge dialog can only offer to narrow the deletion by
  // filters the gateway itself can apply.
  await page.goto('/logs?provider=openai')

  await page.getByRole('button', { name: 'Delete old logs' }).click()
  const dialog = page.getByRole('dialog', { name: 'Delete old request logs' })
  await dialog.getByLabel('Delete entries before').fill('2026-01-15')

  // Unscoped by default, and it says so: an operator who reads "across every
  // provider" and meant "only openai" still has the checkbox in front of them.
  await expect(dialog).toContainText('across every provider, model, and stage')
  await dialog.getByLabel(/Limit the deletion to the current filters/).check()
  await expect(dialog).toContainText('limited to provider openai')

  const purge = page.waitForRequest((request) =>
    request.method() === 'DELETE' && request.url().includes('/admin/logs'))
  await dialog.getByRole('button', { name: 'Delete logs' }).click()
  const sent = new URL((await purge).url())

  // Local midnight, not UTC's. Far enough east or west the two name different
  // days, and this value decides what is permanently deleted.
  expect(sent.searchParams.get('before')).toBe(new Date('2026-01-15T00:00:00').toISOString())
  expect(sent.searchParams.get('provider')).toBe('openai')

  await expect(page.getByText(/24 request-log entries before .* were deleted \(provider openai\)\./)).toBeVisible()
  await expect(dialog).toBeHidden()
})

test('a purge dated in the future deletes nothing until the date is corrected', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/logs')

  const deletes: string[] = []
  page.on('request', (request) => {
    if (request.method() === 'DELETE') deletes.push(request.url())
  })

  await page.getByRole('button', { name: 'Delete old logs' }).click()
  const dialog = page.getByRole('dialog', { name: 'Delete old request logs' })
  const before = dialog.getByLabel('Delete entries before')

  // A cutoff in the future deletes the whole log, including the events of the
  // incident being investigated. Nothing may leave the browser for one.
  await before.fill('2099-01-01')
  await dialog.getByRole('button', { name: 'Delete logs' }).click()
  await expect(dialog).toBeVisible()
  expect(deletes).toEqual([])

  // And the refusal is specific to the date, not the button: a valid cutoff
  // from the same dialog goes through.
  await before.fill('2026-01-15')
  await dialog.getByRole('button', { name: 'Delete logs' }).click()
  await expect(page.getByText(/24 request-log entries before .* were deleted\./)).toBeVisible()
  expect(deletes).toHaveLength(1)
})

test('the audit trail filters by action, explains a near miss, and pages through the matches', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/audit')

  await expect(page.getByText(`${auditEntries.length} matching entries`)).toBeVisible()

  const rotations = auditEntries.filter((entry) => entry.action === 'key.rotate')
  await page.getByPlaceholder('Action, e.g. key.create').fill('key.rotate')
  await page.getByRole('button', { name: 'Apply' }).click()

  // In the URL, because an audit investigation is pasted into an incident
  // channel and a reload must not drop back to "everything, last 24 hours".
  await expect(page).toHaveURL(/action=key.rotate/)
  await expect(page.getByText(`${rotations.length} matching entries`)).toBeVisible()
  await expect(page.getByRole('cell', { name: 'key.create', exact: true })).toHaveCount(0)

  // The gateway matches the action exactly, so a plausible guess returns
  // nothing and reads exactly like a gateway nobody has touched.
  await page.getByPlaceholder('Action, e.g. key.create').fill('key.rotated')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByText('Action and actor ID are matched exactly.')).toBeVisible()

  await page.getByPlaceholder('Action, e.g. key.create').fill('')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByText(`Showing 1–50 of ${auditEntries.length}`)).toBeVisible()
  await page.getByRole('button', { name: 'Next' }).click()
  await expect(page).toHaveURL(/offset=50/)
  await expect(page.getByText(`Showing 51–${auditEntries.length} of ${auditEntries.length}`)).toBeVisible()
})

test('a refusal the gateway enforced is recorded as denied, with the reason on demand', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/audit')

  const denied = auditEntries.filter((entry) => entry.outcome === 'denied')
  await page.getByRole('combobox', { name: 'Outcome' }).click()
  await page.getByRole('option', { name: 'Denied', exact: true }).click()
  await page.getByRole('button', { name: 'Apply' }).click()

  await expect(page.getByText(`${denied.length} matching entries`)).toBeVisible()
  // "Denied" is not "Error": it is a refusal the gateway enforced, and showing
  // it in the error tone sends an operator hunting for a broken gateway on
  // evidence that the gateway held.
  const recorded = page.getByRole('region', { name: 'Recorded actions' })
  await expect(recorded.getByText('Denied').first()).toBeVisible()
  await expect(recorded.getByText('Error', { exact: true })).toHaveCount(0)

  // The detail is the row's only description of what was refused.
  await page.getByRole('button', { name: /^Show detail for/ }).first().click()
  await expect(page.getByText('cannot remove the last admin key').first()).toBeVisible()
})

test('a gateway without an audit store explains itself instead of reporting a failure', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken, { auditDisabled: true })
  await signIn(page)
  await page.goto('/audit')

  await expect(page.getByRole('heading', { name: 'The audit trail is not being recorded' })).toBeVisible()
  await expect(page.getByText(/API_KEY_STORE_BACKEND/)).toBeVisible()
  // A deployment choice, not a fault: no red banner, and no filters offering to
  // search a trail that does not exist.
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(page.getByPlaceholder('Action, e.g. key.create')).toHaveCount(0)
})

test('the capabilities matrix says how each provider treats a parameter, and narrows on either axis', async ({ page }) => {
  await mockGateway(page, ['admin'], adminToken)
  await signIn(page)
  await page.goto('/providers')

  await page.getByRole('tab', { name: 'Capabilities' }).click()
  await expect(page.getByRole('heading', { name: 'Chat parameter support' })).toBeVisible()

  // A profile records exceptions the gateway knows about, so a provider with
  // nothing to declare forwards everything. Reading a full row of "forward" as
  // "supports nothing" would invert the meaning of the whole table.
  const openai = page.getByRole('row').filter({ has: page.getByRole('rowheader', { name: 'openai' }) })
  await expect(openai).not.toContainText('unsupported')
  const anthropic = page.getByRole('row').filter({ has: page.getByRole('rowheader', { name: 'anthropic' }) })
  await expect(anthropic).toContainText('unsupported')
  await expect(anthropic).toContainText('translate')

  // One box narrows either axis, so an operator does not have to say whether
  // the word they typed is a provider or a parameter.
  const filter = page.getByPlaceholder('Filter by provider or parameter')
  await filter.fill('logprobs')
  await expect(page.getByText(`Showing 3 of 3 providers and 2 of ${capabilityParamCount} parameters`)).toBeVisible()

  await filter.fill('gemini')
  await expect(page.getByText(`Showing 1 of 3 providers and ${capabilityParamCount} of ${capabilityParamCount} parameters`)).toBeVisible()
  await expect(page.getByRole('rowheader', { name: 'anthropic' })).toHaveCount(0)

  // A query matching neither is answered honestly rather than by silently
  // showing the full matrix again.
  await filter.fill('nothing-matches-this')
  await expect(page.getByRole('heading', { name: 'No provider or parameter matches' })).toBeVisible()
  await page.getByRole('button', { name: 'Clear filter' }).click()
  await expect(page.getByRole('rowheader', { name: 'anthropic' })).toBeVisible()
})
