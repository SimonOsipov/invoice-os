// Settings > Workspace: delete dialog, deletion notice, cancel. Every workspace is registered here; the seeded tenants are never scheduled.
import { test, expect, type Locator, type Page } from '@playwright/test'
import {
  acceptById,
  cancelDeletion,
  inviteWithToken,
  listMine,
  me,
  mintSignInState,
  registerFormAccount,
  registerFresh,
  scheduleDeletion,
  setDeletionLinkToken,
  signInSession,
  subjectOf,
  type PendingInvite,
  type RealAccount,
} from '../api/client'
import { VERIFIED, passFrontDoor, signInAtFrontDoor } from '../personaSession'
import { collectSourcedErrors, expectedStatusDropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

test.beforeAll(() => { expect(test.info().project.name, 'workspace-deletion.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces') })

const HEIGHT = 1080
// internal/tenancy/tenancy.go maxNameChars.
const MAX_NAME_CHARS = 200

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

async function openWorkspaceTab(page: Page): Promise<void> {
  // Scoped to the tab strip: the in-house sidebar group is also called "Workspace".
  await page.locator('button.pf-tab').filter({ hasText: /^Workspace$/ }).click()
  await expect(page).toHaveURL(/\/settings\/workspace$/)
  await expect(page.getByTestId('settings-workspace-card')).toBeVisible()
}

async function sweepCard(page: Page): Promise<void> {
  const card = page.getByTestId('settings-workspace-card')
  const button = page.getByTestId('settings-workspace-delete')
  const main = page.locator('main.pf-main')
  await sweep(page, async (width) => {
    await settleAnimations(card, button)
    const [mainBox, cardBox, buttonBox] = await Promise.all([rectOf('main', main, width), rectOf('the card', card, width), rectOf('the button', button, width)])
    expect(enclosesRect(mainBox, cardBox, 1), `the card leaves main at ${width}px`).toBe(true)
    expect(enclosesRect(cardBox, buttonBox, 1), `Delete workspace leaves the card at ${width}px`).toBe(true)
    await assertPageDoesNotScrollSideways(page, `Settings > Workspace at ${width}px`)
  })
}

async function sweepDialog(page: Page, name: string): Promise<void> {
  const panel = page.getByRole('dialog')
  const parts = {
    'the shown name': panel.getByTestId('delete-workspace-name-shown'),
    'the input': panel.getByTestId('delete-workspace-name'),
    Cancel: panel.getByRole('button', { name: 'Cancel', exact: true }),
    'Delete workspace': panel.getByTestId('delete-workspace-confirm'),
  }
  await expect(parts['the shown name']).toHaveText(name)
  await sweep(page, async (width) => {
    await settleAnimations(panel, ...Object.values(parts))
    const panelBox = await rectOf('the panel', panel, width)
    expect(enclosesRect({ x: 0, y: 0, width, height: HEIGHT }, panelBox, 1), `the panel leaves the viewport at ${width}px`).toBe(true)
    const boxes: Record<string, Rect> = {}
    for (const [label, loc] of Object.entries(parts)) {
      boxes[label] = await rectOf(label, loc, width)
      expect(enclosesRect(panelBox, boxes[label], 1), `${label} leaves the panel at ${width}px`).toBe(true)
    }
    expect(rectsOverlap(boxes.Cancel, boxes['Delete workspace']), `the dialog buttons overlap at ${width}px`).toBe(false)
    await assertPageDoesNotScrollSideways(page, `the dialog at ${width}px`)
  })
}

async function sweepNotice(page: Page): Promise<void> {
  const card = page.getByTestId('workspace-deletion-notice')
  const parts = {
    'the body': card.getByText(/we delete every invoice, file and member of/),
    'Cancel deletion': page.getByTestId('deletion-notice-cancel'),
    'Sign out': card.getByRole('button', { name: 'Sign out', exact: true }),
  }
  await sweep(page, async (width) => {
    await settleAnimations(card, ...Object.values(parts))
    const cardBox = await rectOf('the notice', card, width)
    expect(enclosesRect({ x: 0, y: 0, width, height: HEIGHT }, cardBox, 1), `the notice leaves the viewport at ${width}px`).toBe(true)
    const boxes: Record<string, Rect> = {}
    for (const [label, loc] of Object.entries(parts)) {
      boxes[label] = await rectOf(label, loc, width)
      expect(enclosesRect(cardBox, boxes[label], 1), `${label} leaves the notice at ${width}px`).toBe(true)
    }
    expect(rectsOverlap(boxes['Cancel deletion'], boxes['Sign out']), `the notice buttons overlap at ${width}px`).toBe(false)
    // The notice has no app shell, so layout.ts's `.pf-scroll` helper would never resolve.
    const doc = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }))
    expect(doc.scrollWidth - doc.clientWidth, `the notice scrolls sideways at ${width}px (${JSON.stringify(doc)})`).toBeLessThanOrEqual(1)
  })
}

// Cancel is idempotent, so the cleanup is safe whether or not the test got as far as scheduling.
async function withWorkspace(account: RealAccount, token: string, page: Page, drive: () => Promise<void>): Promise<void> {
  try {
    await signInAtFrontDoor(page, account, '/settings/members')
    await drive()
  } finally {
    await cancelDeletion(token)
  }
}

test('workspace deletion: an admin deletes from Settings, reloads, cancels and is back', async ({ page }) => {
  test.setTimeout(240_000)
  const { account, token } = await registerFormAccount('ws-delete')
  // A request in flight when the lock lands answers 403 workspace_locked; that 403 is the schedule's own effect.
  const errors = collectSourcedErrors(page, expectedStatusDropper(page, 403, /\/api\//))

  await withWorkspace(account, token, page, async () => {
    await openWorkspaceTab(page)
    await sweepCard(page)

    await page.getByTestId('settings-workspace-delete').click()
    const confirm = page.getByTestId('delete-workspace-confirm')
    const input = page.getByTestId('delete-workspace-name')
    await expect(confirm).toBeDisabled()
    await input.fill(`${account.workspaceName}x`)
    await expect(confirm).toBeDisabled()
    await input.fill(account.workspaceName)
    await expect(confirm).toBeEnabled()
    await sweepDialog(page, account.workspaceName)

    await confirm.click()
    const notice = page.getByTestId('workspace-deletion-notice')
    await expect(notice).toContainText(account.workspaceName)
    await expect(page.getByTestId('deletion-notice-cancel')).toBeVisible()
    await sweepNotice(page)

    await page.reload()
    await expect(notice).toContainText(account.workspaceName)
    await page.getByTestId('deletion-notice-cancel').click()

    await expect(page.locator(VERIFIED)).toBeAttached({ timeout: 30_000 })
    await expect(notice).toHaveCount(0)
    await expect(page.locator('button.pf-tab').filter({ hasText: /^Members$/ })).toBeVisible()
  })
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('workspace deletion: a 200-character name stays inside the dialog and the notice', async ({ page }) => {
  test.setTimeout(240_000)
  const name = 'W'.repeat(MAX_NAME_CHARS - 8) + crypto.randomUUID().slice(0, 8)
  expect(name).toHaveLength(MAX_NAME_CHARS)
  const { account, token } = await registerFormAccount('ws-delete-long', { workspaceName: name })

  await withWorkspace(account, token, page, async () => {
    await openWorkspaceTab(page)
    await page.getByTestId('settings-workspace-delete').click()
    const confirm = page.getByTestId('delete-workspace-confirm')
    await expect(confirm).toBeDisabled()
    await page.getByTestId('delete-workspace-name').fill(name)
    await expect(confirm).toBeEnabled()
    await sweepDialog(page, name)

    await confirm.click()
    await expect(page.getByTestId('workspace-deletion-notice')).toContainText(name)
    await sweepNotice(page)

    await page.getByTestId('deletion-notice-cancel').click()
    await expect(page.locator(VERIFIED)).toBeAttached({ timeout: 30_000 })
    await expect(page.locator('button.pf-tab').filter({ hasText: /^Workspace$/ })).toBeVisible()
  })
})

test('workspace deletion: a second admin cancels from the emailed link', async ({ page }) => {
  test.setTimeout(240_000)
  const { account, token } = await registerFormAccount('ws-cancel-link')
  const errors = collectSourcedErrors(page, expectedStatusDropper(page, 403, /\/api\//))
  try {
    const tenantId = (await me(token)).tenant.id
    const b = await registerFresh('ws-cancel-link-b')
    await inviteWithToken(token, tenantId, b.email, 'admin')
    const session = (await signInSession(b.email, b.password)).access_token
    const [pending] = ((await listMine(session)).body as { invitations: PendingInvite[] }).invitations
    const accepted = await acceptById(session, pending.id, 'Cancel Link Admin')
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
    const bId = subjectOf((await signInSession(b.email, b.password)).access_token)

    const scheduled = await scheduleDeletion(token, account.workspaceName)
    expect(scheduled.status, JSON.stringify(scheduled.body)).toBe(200)
    const linkToken = mintSignInState()
    await setDeletionLinkToken(tenantId, bId, linkToken)

    await passFrontDoor(page, b, `/#cancel-deletion=${linkToken}`)
    const notice = page.getByTestId('workspace-deletion-notice')
    await expect(notice).toContainText(account.workspaceName)
    const sent = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/tenancy/v1/workspace/deletion/cancel-by-link'))
    await page.getByTestId('deletion-notice-cancel').click()
    await sent

    await expect(page.locator(VERIFIED)).toBeAttached({ timeout: 30_000 })
    await expect(notice).toHaveCount(0)
  } finally {
    await cancelDeletion(token)
  }
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})
