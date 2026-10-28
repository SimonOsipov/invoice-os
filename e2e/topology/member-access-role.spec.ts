// Settings › Members drawer: an admin changes a member's access role and reads it back after a reload.
// A fresh firm workspace, so the sole-admin and approver-gap states are this test's own.
import { randomUUID } from 'node:crypto'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { createWorkflowRole, grantMembership, me, memberships, provisionRealAccount, registerFresh, signInSession, staffWorkflowRole, subjectOf } from '../api/client'
import { collectErrors, signInAtFrontDoor } from '../personaSession'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

test.beforeAll(() => { expect(test.info().project.name, 'member-access-role.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces') })

// frontend/app/src/lib/members.ts PROTECTED_ADMIN_NOTE, ROLE_CHANGE_KEEP, ROLE_CHANGE_CONFIRMED.
const PROTECTED_ADMIN_NOTE = "You're the only admin. Promote someone else first."
const KEEP = 'Keep role'
const CHANGE = 'Change role'
const ROLE_IDS = ['admin', 'preparer', 'reviewer'] as const

function memberRow(page: Page, name: string) {
  return page.getByTestId('member-row').filter({ has: page.getByText(name, { exact: true }) })
}

// The roster's Access role label: the first line of the row's second cell (the second line is the client scope).
function accessLabel(page: Page, name: string): Locator {
  return memberRow(page, name).locator('xpath=./span[2]/span[1]')
}

async function atEachWidth(page: Page, fn: (width: number) => Promise<void>): Promise<number[]> {
  const entry = page.viewportSize()
  const measured: number[] = []
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
      await fn(width)
      measured.push(width)
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  return measured
}

async function boxOf(target: Locator, name: string): Promise<Rect> {
  const box = await target.boundingBox()
  expect(box, `${name} must render`).toBeTruthy()
  return box!
}

test("firm Settings: an admin changes a member's access role in the drawer and it survives a reload", async ({ page }) => {
  test.setTimeout(240_000)
  const errors = collectErrors(page)
  const run = randomUUID().slice(0, 8)
  const adminName = `Access Admin ${run}`
  const memberName = `Access Reviewer ${run}`
  const roleTitle = `Access Seat ${run}`

  const admin = await provisionRealAccount('access-role', 'firm', adminName)
  const adminToken = (await signInSession(admin.email, admin.password)).access_token
  const reviewer = await registerFresh('access-role-reviewer')
  const reviewerId = subjectOf((await signInSession(reviewer.email, reviewer.password)).access_token)
  await grantMembership({ user_id: reviewerId, tenant_id: (await me(adminToken)).tenant.id, role: 'reviewer', display_name: memberName, email: reviewer.email })
  const seat = await createWorkflowRole(adminToken, { title: roleTitle })
  await staffWorkflowRole(adminToken, seat.key, [reviewerId])

  const patches: string[] = []
  page.on('request', (req) => {
    if (req.method() === 'PATCH' && req.url().includes('/memberships/')) patches.push(req.url())
  })
  const patchAnswered = (user: string) => page.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().endsWith(`/memberships/${user}/role`))
  const storedRole = async () => (await memberships(adminToken)).memberships.find((m) => m.user_id === reviewerId)?.role

  await signInAtFrontDoor(page, admin, '/settings/members')
  await expect(memberRow(page, memberName)).toBeVisible()
  await expect(memberRow(page, memberName)).toContainText(roleTitle)

  const drawer = page.getByTestId('member-drawer')
  const strip = page.getByTestId('member-role-confirm')
  const card = (id: string) => page.getByTestId(`drawer-role-${id}`)

  await memberRow(page, memberName).click()
  await expect(drawer).toBeVisible()
  for (const id of ROLE_IDS) await expect(card(id).locator('input')).toBeEnabled()
  await expect(card('reviewer').locator('input')).toBeChecked()

  // Preparer cannot approve, so the strip opens and names the seat the member would leave unstaffed.
  await card('preparer').click()
  await expect(strip).toBeVisible()
  await expect(page.getByTestId('member-role-gaps')).toContainText(roleTitle)
  expect(patches, 'the strip opened after a PATCH').toEqual([])

  await settleAnimations(drawer, strip)
  const widths = await atEachWidth(page, async (width) => {
    const panel = await boxOf(drawer, 'drawer panel')
    const stripBox = await boxOf(strip, 'confirm strip')
    const cards = await boxOf(card('admin'), 'role cards')
    const keep = await boxOf(strip.getByRole('button', { name: KEEP }), KEEP)
    const change = await boxOf(strip.getByRole('button', { name: CHANGE }), CHANGE)
    expect(enclosesRect(panel, stripBox, 1), `the strip leaves the drawer panel at ${width}px`).toBe(true)
    expect(Math.abs(stripBox.x - cards.x), `the strip is off the role cards' left edge at ${width}px`).toBeLessThanOrEqual(1)
    expect(rectsOverlap(keep, change), `${KEEP} and ${CHANGE} overlap at ${width}px`).toBe(false)
  })
  expect(widths).toEqual([...WIDE_WIDTHS])

  await strip.getByRole('button', { name: KEEP }).click()
  await expect(strip).toBeHidden()
  await expect(card('reviewer').locator('input')).toBeChecked()
  expect(patches, 'Keep role sent a PATCH').toEqual([])

  await card('preparer').click()
  await expect(strip).toBeVisible()
  const changed = patchAnswered(reviewerId)
  await strip.getByRole('button', { name: CHANGE }).click()
  expect((await changed).status()).toBe(200)
  expect(patches).toHaveLength(1)
  await expect(accessLabel(page, memberName)).toHaveText('Preparer')

  await page.reload()
  await expect(accessLabel(page, memberName)).toHaveText('Preparer')
  expect(await storedRole()).toBe('preparer')

  // Reviewer can approve, so there is no gap and no strip.
  await memberRow(page, memberName).click()
  const back = patchAnswered(reviewerId)
  await card('reviewer').click()
  expect((await back).status()).toBe(200)
  await expect(strip).toBeHidden()
  await page.reload()
  await expect(accessLabel(page, memberName)).toHaveText('Reviewer')
  expect(await storedRole()).toBe('reviewer')

  await memberRow(page, adminName).click()
  for (const id of ROLE_IDS) await expect(card(id).locator('input')).toBeDisabled()
  await expect(drawer).toContainText(PROTECTED_ADMIN_NOTE)

  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})
