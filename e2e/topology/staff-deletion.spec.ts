// Support Console Tenants: staff start, cancel and purge a fresh workspace through the modal; the deletion header, member rows and modal fit at every wide width; the header wraps at 390px.
// Every workspace is registered here; the seeded tenants are never acted on.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { acceptById, inviteWithToken, listMine, me, provisionStaffAccount, rawFetch, registerFormAccount, registerFresh, signInSession, type PendingInvite, type StaffAccount } from '../api/client'
import { seedStaffSession } from '../staffSession'
import { collectSourcedErrors, expectedStatusDropper } from './consoleGate'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

test.beforeAll(() => { expect(test.info().project.name, 'staff-deletion.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces') })
test.use({ viewport: { width: 1440, height: 900 } })

const HEIGHT = 1080
// internal/tenancy/tenancy.go maxNameChars.
const MAX_NAME_CHARS = 200
const STAFF_TENANTS = '/api/tenancy/v1/staff/tenants'

const auth = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })
const main = (page: Page): Locator => page.locator('main')
const rowOf = (page: Page, id: string): Locator => main(page).locator(`button[data-tenant-id="${id}"]`)

async function rectOf(label: string, target: Locator, width: number): Promise<Rect> {
  const box = await target.boundingBox()
  if (!box) throw new Error(`${label} rendered no box at ${width}px`)
  return box
}

async function sweep(page: Page, check: (width: number) => Promise<void>): Promise<void> {
  const entry = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: HEIGHT })
      await check(width)
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
}

async function noSideways(page: Page, where: string): Promise<void> {
  const doc = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }))
  expect(doc.scrollWidth - doc.clientWidth, `${where} scrolls sideways (${JSON.stringify(doc)})`).toBeLessThanOrEqual(1)
}

async function staffSession(page: Page, account: StaffAccount): Promise<string> {
  return (await seedStaffSession(page, 'support', account)).token
}

async function openTenant(page: Page, id: string, name: string): Promise<void> {
  await page.locator('aside nav button.ops-nav').filter({ hasText: 'Tenants' }).click()
  await expect(main(page).getByRole('heading', { level: 1, name: 'Tenants' })).toBeVisible()
  await main(page).getByLabel('Search tenants').fill(name.slice(0, 60))
  await rowOf(page, id).click()
  await expect(main(page).getByRole('heading', { level: 2, name })).toBeVisible()
}

async function fresh(prefix: string, workspaceName?: string): Promise<{ adminToken: string; tenantId: string; name: string }> {
  const reg = await registerFormAccount(prefix, { workspaceName })
  return { adminToken: reg.token, tenantId: (await me(reg.token)).tenant.id, name: reg.account.workspaceName }
}

async function addPreparer(adminToken: string, tenantId: string): Promise<void> {
  const account = await registerFresh('staff-deletion-preparer')
  await inviteWithToken(adminToken, tenantId, account.email, 'preparer')
  const session = (await signInSession(account.email, account.password)).access_token
  const { invitations } = (await listMine(session)).body as { invitations: PendingInvite[] }
  const accepted = await acceptById(session, invitations[0].id, 'Staff Deletion Preparer')
  expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
}

// Fills the reason and confirms in the open modal.
async function confirmModal(page: Page, confirm: string, reason: string): Promise<void> {
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Reason').fill(reason)
  await dialog.getByRole('button', { name: confirm, exact: true }).click()
  await expect(dialog).toBeHidden()
}

test('support console: start, cancel and purge a workspace', async ({ page }) => {
  test.setTimeout(240_000)
  const errors = collectSourcedErrors(page, expectedStatusDropper(page, 404, new RegExp(`${STAFF_TENANTS}/`)))
  const ws = await fresh('staff-deletion-journey')
  const staff = await provisionStaffAccount('staff-deletion-ui')
  const token = await staffSession(page, staff)
  await openTenant(page, ws.tenantId, ws.name)

  const actions = page.getByTestId('tenant-actions')
  const scheduled = main(page).getByText(/Scheduled for deletion on/)
  await actions.getByRole('button', { name: 'Delete workspace', exact: true }).click()
  await confirmModal(page, 'Start deletion', 'e2e start')
  await expect(scheduled).toBeVisible()
  expect((await me(ws.adminToken)).tenant.deletion_due_at, 'the customer reads the due date').not.toBeNull()

  await actions.getByRole('button', { name: 'Cancel deletion', exact: true }).click()
  await confirmModal(page, 'Cancel deletion', 'e2e cancel')
  await expect(scheduled).toBeHidden()
  expect((await me(ws.adminToken)).tenant.deletion_due_at, 'the customer reads no due date').toBeNull()
  await expect(actions.getByRole('button', { name: 'Purge now', exact: true }), 'Purge now waits for a scheduled deletion').toHaveCount(0)
  await expect(main(page).getByText('Not scheduled. Use Delete workspace first')).toBeVisible()

  await actions.getByRole('button', { name: 'Delete workspace', exact: true }).click()
  await confirmModal(page, 'Start deletion', 'e2e start again')
  await expect(scheduled).toBeVisible()

  await actions.getByRole('button', { name: 'Purge now', exact: true }).click()
  const dialog = page.getByRole('dialog')
  const purge = dialog.getByRole('button', { name: 'Purge now', exact: true })
  await dialog.getByLabel('Reason').fill('e2e purge')
  await dialog.getByLabel('Workspace name').fill(`${ws.name}x`)
  await expect(purge, 'a wrong name keeps the confirm blocked').toBeDisabled()
  await dialog.getByLabel('Workspace name').fill(ws.name)
  await expect(purge).toBeEnabled()
  await purge.click()
  await expect(page.getByText('Purge started'), 'the toast follows the 202').toBeVisible()
  // The purge job may erase the tenant before the re-read lands; the read then answers 404.
  await expect(main(page).getByText(/Purge in progress/).or(main(page).getByText('Tenant not found.'))).toBeVisible()

  await expect.poll(async () => (await rawFetch(`${STAFF_TENANTS}/${ws.tenantId}`, { headers: auth(token) })).status, { intervals: [3_000], timeout: 90_000 }).toBe(404)

  await page.reload()
  await page.locator('aside nav button.ops-nav').filter({ hasText: 'Tenants' }).click()
  await main(page).getByLabel('Search tenants').fill(ws.name.slice(0, 60))
  await expect(rowOf(page, ws.tenantId), 'the purged tenant leaves the list').toHaveCount(0)
  expect(errors, errors.join('\n')).toEqual([])
})

test('support console: the deletion header, member rows and modal fit at every wide width', async ({ page }) => {
  test.setTimeout(240_000)
  const errors = collectSourcedErrors(page)
  const name = `W${crypto.randomUUID().replaceAll('-', '')}`.padEnd(MAX_NAME_CHARS, 'x')
  expect(name).toHaveLength(MAX_NAME_CHARS)
  const ws = await fresh('staff-deletion-layout', name)
  await addPreparer(ws.adminToken, ws.tenantId)
  await staffSession(page, await provisionStaffAccount('staff-deletion-layout'))
  await openTenant(page, ws.tenantId, name)

  const actions = page.getByTestId('tenant-actions')
  const header = actions.locator('xpath=..')
  const card = header.locator('xpath=..')
  const text = main(page).getByRole('heading', { level: 2, name }).locator('xpath=../..')
  const viewJobs = header.getByRole('button', { name: 'View jobs', exact: true })
  const deleteButtons = main(page).getByRole('button', { name: 'Delete account', exact: true })
  // Member rows offer Delete account only while the workspace is unscheduled.
  await expect(deleteButtons).toHaveCount(2)
  await sweep(page, async (width) => {
    for (let i = 0; i < 2; i++) {
      const button = deleteButtons.nth(i)
      const row = button.locator('xpath=..')
      const chip = button.locator('xpath=preceding-sibling::*[1]')
      await settleAnimations(row, button, chip)
      const [rowBox, buttonBox, chipBox] = await Promise.all([rectOf(`row ${i}`, row, width), rectOf(`Delete account ${i}`, button, width), rectOf(`status chip ${i}`, chip, width)])
      expect(enclosesRect(rowBox, buttonBox, 1), `Delete account ${i} leaves its row at ${width}px`).toBe(true)
      expect(rectsOverlap(buttonBox, chipBox), `Delete account ${i} and its status chip overlap at ${width}px`).toBe(false)
    }
    await noSideways(page, `Tenants, member rows at ${width}px`)
  })

  // Purge now shows only on a scheduled workspace.
  await actions.getByRole('button', { name: 'Delete workspace', exact: true }).click()
  await confirmModal(page, 'Start deletion', 'e2e layout')
  await expect(main(page).getByText(/Scheduled for deletion on/)).toBeVisible()
  await expect(deleteButtons, 'a scheduled workspace offers no Delete account').toHaveCount(0)
  const actionButtons = actions.getByRole('button')
  await expect(actionButtons, 'Cancel deletion and Purge now').toHaveCount(2)

  await sweep(page, async (width) => {
    await settleAnimations(card, header, text, viewJobs, actions)
    const r = {
      card: await rectOf('the detail card', card, width),
      header: await rectOf('the header', header, width),
      text: await rectOf('the text block', text, width),
      viewJobs: await rectOf('View jobs', viewJobs, width),
      actions: await rectOf('the action group', actions, width),
    }
    for (const part of ['header', 'text', 'viewJobs', 'actions'] as const) {
      expect(enclosesRect(r.card, r[part], 1), `${part} leaves the detail card at ${width}px`).toBe(true)
    }
    for (const part of ['text', 'viewJobs', 'actions'] as const) {
      expect(enclosesRect(r.header, r[part], 1), `${part} leaves the header at ${width}px`).toBe(true)
    }
    const [first, second] = [await rectOf('first action', actionButtons.nth(0), width), await rectOf('second action', actionButtons.nth(1), width)]
    for (const [label, box] of [['first action', first], ['second action', second]] as const) {
      expect(enclosesRect(r.actions, box, 1), `${label} leaves the action group at ${width}px`).toBe(true)
    }
    expect(rectsOverlap(first, second), `the action buttons overlap at ${width}px`).toBe(false)
    for (const [a, b] of [['text', 'viewJobs'], ['text', 'actions'], ['viewJobs', 'actions']] as const) {
      expect(rectsOverlap(r[a], r[b]), `${a} and ${b} overlap at ${width}px`).toBe(false)
    }

    await noSideways(page, `Tenants at ${width}px`)
  })

  // The purge modal holds the long title, the long shown name, both inputs and both buttons.
  await actions.getByRole('button', { name: 'Purge now', exact: true }).click()
  const panel = page.getByRole('dialog')
  const parts = {
    title: panel.getByRole('heading', { level: 3 }),
    'the shown name': panel.getByText(name, { exact: true }),
    'the name input': panel.getByLabel('Workspace name'),
    'the reason input': panel.getByLabel('Reason'),
    Back: panel.getByRole('button', { name: 'Back', exact: true }),
    'Purge now': panel.getByRole('button', { name: 'Purge now', exact: true }),
  }
  await sweep(page, async (width) => {
    await settleAnimations(panel, ...Object.values(parts))
    const panelBox = await rectOf('the panel', panel, width)
    expect(enclosesRect({ x: 0, y: 0, width, height: HEIGHT }, panelBox, 1), `the panel leaves the viewport at ${width}px`).toBe(true)
    const boxes: Record<string, Rect> = {}
    for (const [label, loc] of Object.entries(parts)) {
      boxes[label] = await rectOf(label, loc, width)
      expect(enclosesRect(panelBox, boxes[label], 1), `${label} leaves the panel at ${width}px`).toBe(true)
    }
    expect(rectsOverlap(boxes.Back, boxes['Purge now']), `Back and Purge now overlap at ${width}px`).toBe(false)
    expect(rectsOverlap(boxes['the name input'], boxes['the reason input']), `the inputs overlap at ${width}px`).toBe(false)
    await noSideways(page, `the modal at ${width}px`)
  })
  await parts.Back.click()
  await expect(panel).toBeHidden()
  expect(errors, errors.join('\n')).toEqual([])
})

const NARROW_WIDTHS = [390, 768, ...WIDE_WIDTHS]

test('support console: the tenant header wraps and shows the whole state line at 390px and every wide width', async ({ page }) => {
  test.setTimeout(240_000)
  const errors = collectSourcedErrors(page)
  const name = `N${crypto.randomUUID().replaceAll('-', '')}`.padEnd(MAX_NAME_CHARS, 'x')
  const ws = await fresh('staff-deletion-narrow', name)
  await staffSession(page, await provisionStaffAccount('staff-deletion-narrow'))
  await openTenant(page, ws.tenantId, name)

  const actions = page.getByTestId('tenant-actions')
  const header = actions.locator('xpath=..')
  const card = header.locator('xpath=..')
  const heading = main(page).getByRole('heading', { level: 2, name })
  const text = heading.locator('xpath=../..')
  const viewJobs = header.getByRole('button', { name: 'View jobs', exact: true })

  const check = async (state: string, stateLine: Locator): Promise<void> => {
    const entry = page.viewportSize()
    try {
      for (const width of NARROW_WIDTHS) {
        await page.setViewportSize({ width, height: HEIGHT })
        await settleAnimations(card, header, text, viewJobs, actions)
        const where = `${state} at ${width}px`
        const r = {
          text: await rectOf('the text block', text, width),
          viewJobs: await rectOf('View jobs', viewJobs, width),
          actions: await rectOf('the action group', actions, width),
        }
        const cardBox = await rectOf('the detail card', card, width)
        for (const part of ['text', 'viewJobs', 'actions'] as const) {
          expect(enclosesRect(cardBox, r[part], 1), `${part} leaves the detail card, ${where}`).toBe(true)
        }
        for (const [a, b] of [['text', 'viewJobs'], ['text', 'actions'], ['viewJobs', 'actions']] as const) {
          expect(rectsOverlap(r[a], r[b]), `${a} and ${b} overlap, ${where}`).toBe(false)
        }
        const buttons = actions.getByRole('button')
        const count = await buttons.count()
        const boxes: Rect[] = []
        for (let i = 0; i < count; i++) {
          const box = await rectOf(`action ${i}`, buttons.nth(i), width)
          expect(enclosesRect(cardBox, box, 1), `action ${i} leaves the detail card, ${where}`).toBe(true)
          boxes.push(box)
        }
        for (let i = 0; i < count; i++) for (let j = i + 1; j < count; j++) expect(rectsOverlap(boxes[i], boxes[j]), `actions ${i} and ${j} overlap, ${where}`).toBe(false)
        expect((await rectOf('the name', heading, width)).width, `the name is 0 wide, ${where}`).toBeGreaterThan(0)
        expect((await rectOf('the state line', stateLine, width)).width, `the state line is 0 wide, ${where}`).toBeGreaterThan(0)
        const fit = await stateLine.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        expect(fit.scrollWidth, `the state line is truncated, ${where} (${JSON.stringify(fit)})`).toBeLessThanOrEqual(fit.clientWidth)
        await noSideways(page, `Tenants, ${where}`)
      }
    } finally {
      if (entry) await page.setViewportSize(entry)
    }
  }

  const stateLine = main(page).getByText(/Scheduled for deletion on/)
  await check('unscheduled', main(page).getByText('Not scheduled. Use Delete workspace first; Purge now follows.'))
  await actions.getByRole('button', { name: 'Delete workspace', exact: true }).click()
  await confirmModal(page, 'Start deletion', 'e2e narrow')
  await expect(stateLine).toBeVisible()
  await check('scheduled', stateLine)
  expect(errors, errors.join('\n')).toEqual([])
})
