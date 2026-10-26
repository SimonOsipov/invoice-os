// Your account page: a member deletes themself, the last member schedules the workspace, and the card, page and dialog stay inside at every width.
// Every workspace is registered here; the seeded tenants are never touched.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { acceptById, cancelDeletion, inviteWithToken, listMine, me, registerFormAccount, registerFresh, signInSession, type PendingInvite, type RealAccount } from '../api/client'
import { VERIFIED, signInAtFrontDoor } from '../personaSession'
import { resolveTarget } from '../targets'
import { collectSourcedErrors, expectedStatusDropper, type Dropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL } from './targets'

test.beforeAll(() => { expect(test.info().project.name, 'account-deletion.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces') })

const LANDING_URL = resolveTarget('LANDING_URL')
const HEIGHT = 1080
// internal/tenancy/tenancy.go maxNameChars.
const MAX_NAME_CHARS = 200
// frontend/app/src/lib/accountDeletion.ts DELETE_ACCOUNT_PHRASE.
const PHRASE = 'delete my account'
// frontend/app/src/auth.ts session storage key.
const SESSION_KEY = 'invoice-os.session'

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

const anyOf = (...droppers: Dropper[]): Dropper => (text, url) => droppers.some((d) => d(text, url))

// A fresh account invited as a preparer to `admin`'s workspace and joined under its own name.
async function joinedPreparer(adminToken: string, workspaceName: string): Promise<RealAccount> {
  const fresh = await registerFresh('selfdelete-ui')
  await inviteWithToken(adminToken, (await me(adminToken)).tenant.id, fresh.email, 'preparer')
  const session = (await signInSession(fresh.email, fresh.password)).access_token
  const { invitations } = (await listMine(session)).body as { invitations: PendingInvite[] }
  const displayName = `Leaver ${crypto.randomUUID().slice(0, 8)}`
  const accepted = await acceptById(session, invitations[0].id, displayName)
  expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
  return { ...fresh, workspaceName, displayName }
}

async function storedSession(page: Page): Promise<string | null> {
  const { origins } = await page.context().storageState()
  const app = origins.find((o) => o.origin === new URL(APP_URL).origin)
  return app?.localStorage.find((e) => e.name === SESSION_KEY)?.value ?? null
}

async function sweepNameCard(page: Page): Promise<void> {
  const card = page.getByTestId('identity-card')
  const button = page.getByTestId('identity-card-account')
  const name = page.getByTestId('persona-name')
  const signOut = card.getByRole('button', { name: 'Sign out', exact: true })
  await sweep(page, async (width) => {
    await settleAnimations(card, button, name, signOut)
    const [cardBox, buttonBox, nameBox, signOutBox] = await Promise.all([rectOf('the card', card, width), rectOf('the account button', button, width), rectOf('the name', name, width), rectOf('Sign out', signOut, width)])
    expect(enclosesRect(cardBox, buttonBox, 1), `the account button leaves the identity card at ${width}px`).toBe(true)
    expect(enclosesRect(buttonBox, nameBox, 1), `the account button does not enclose the name at ${width}px`).toBe(true)
    expect(rectsOverlap(buttonBox, signOutBox), `the account button overlaps Sign out at ${width}px`).toBe(false)
    await assertPageDoesNotScrollSideways(page, `the name card at ${width}px`)
  })
}

async function sweepPage(page: Page): Promise<void> {
  const card = page.getByTestId('account-delete-card')
  const button = page.getByTestId('account-delete')
  const main = page.locator('main.pf-main')
  await sweep(page, async (width) => {
    await settleAnimations(card, button)
    const [mainBox, cardBox, buttonBox] = await Promise.all([rectOf('main', main, width), rectOf('the card', card, width), rectOf('the button', button, width)])
    expect(enclosesRect(mainBox, cardBox, 1), `the card leaves main at ${width}px`).toBe(true)
    expect(enclosesRect(cardBox, buttonBox, 1), `Delete my account leaves the card at ${width}px`).toBe(true)
    await assertPageDoesNotScrollSideways(page, `Your account at ${width}px`)
  })
}

async function sweepDialog(page: Page): Promise<void> {
  const panel = page.getByTestId('delete-account-panel')
  const parts = {
    'the input': panel.getByTestId('delete-account-typed'),
    Cancel: panel.getByRole('button', { name: 'Cancel', exact: true }),
    'Delete my account': panel.getByTestId('delete-account-confirm'),
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
    expect(rectsOverlap(boxes.Cancel, boxes['Delete my account']), `the dialog buttons overlap at ${width}px`).toBe(false)
    await assertPageDoesNotScrollSideways(page, `the dialog at ${width}px`)
  })
}

test('account deletion: a member deletes their account and lands on the landing', async ({ page }) => {
  test.setTimeout(240_000)
  const { account: admin, token } = await registerFormAccount('selfdelete-ui-leave')
  const member = await joinedPreparer(token, admin.workspaceName)
  // The sign-out races the app's last requests; a 401/403 from /api/ after the delete is its own effect.
  const errors = collectSourcedErrors(page, anyOf(expectedStatusDropper(page, 401, /\/api\//), expectedStatusDropper(page, 403, /\/api\//)))

  await signInAtFrontDoor(page, member, '/settings/members')
  expect(await storedSession(page), 'storedSession missed the app session').not.toBeNull()

  await page.getByTestId('identity-card-account').click()
  await expect(page).toHaveURL(/\/account$/)
  await expect(page.getByTestId('account-delete-body')).toContainText('Your account goes at once')
  await page.getByTestId('account-delete').click()
  const confirm = page.getByTestId('delete-account-confirm')
  await expect(confirm).toBeDisabled()
  await page.getByTestId('delete-account-typed').fill(`${PHRASE}x`)
  await expect(confirm).toBeDisabled()
  await page.getByTestId('delete-account-typed').fill(PHRASE)
  await expect(confirm).toBeEnabled()
  await confirm.click()

  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 60_000 })
  expect(await storedSession(page), 'the app origin still holds a session').toBeNull()
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('account deletion: the last member schedules the workspace and can cancel', async ({ page }) => {
  test.setTimeout(240_000)
  const { account, token } = await registerFormAccount('selfdelete-ui-last')
  // A request in flight when the lock lands answers 403 workspace_locked; that 403 is the schedule's own effect.
  const errors = collectSourcedErrors(page, expectedStatusDropper(page, 403, /\/api\//))

  try {
    await signInAtFrontDoor(page, account, '/settings/members')
    await page.getByTestId('identity-card-account').click()
    await expect(page).toHaveURL(/\/account$/)
    await expect(page.getByTestId('account-delete-body')).toContainText(`You are the last member. ${account.workspaceName} will be deleted in 30 days.`)
    await expect(page.getByTestId('account-delete')).toHaveText('Delete my account and workspace')

    await page.getByTestId('account-delete').click()
    await expect(page.getByTestId('delete-workspace-dialog')).toBeVisible()
    const confirm = page.getByTestId('delete-workspace-confirm')
    const input = page.getByTestId('delete-workspace-name')
    await expect(confirm).toBeDisabled()
    await input.fill(`${account.workspaceName}x`)
    await expect(confirm).toBeDisabled()
    await input.fill(account.workspaceName)
    // The export step arrives with DEL-06 (delete-workspace-export); until then the dialog has none.
    if ((await page.getByTestId('delete-workspace-export').count()) > 0) {
      await expect(page.getByTestId('delete-workspace-export')).toBeVisible()
      await page.getByTestId('delete-workspace-exported').check()
    }
    await expect(confirm).toBeEnabled()
    await confirm.click()

    const notice = page.getByTestId('workspace-deletion-notice')
    await expect(notice).toContainText(account.workspaceName)
    await expect(page.getByTestId('deletion-notice-cancel')).toBeVisible()

    await page.reload()
    await expect(notice).toContainText(account.workspaceName)
    await page.getByTestId('deletion-notice-cancel').click()

    await expect(page.locator(VERIFIED)).toBeAttached({ timeout: 30_000 })
    await expect(notice).toHaveCount(0)
  } finally {
    // Cancel is idempotent, so the cleanup is safe whether or not the test got as far as scheduling.
    await cancelDeletion(token)
  }
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('account deletion: the name card, the page and the dialog keep their parts inside at every width', async ({ page }) => {
  test.setTimeout(240_000)
  const workspaceName = 'W'.repeat(MAX_NAME_CHARS - 8) + crypto.randomUUID().slice(0, 8)
  expect(workspaceName).toHaveLength(MAX_NAME_CHARS)
  const { account, token } = await registerFormAccount('selfdelete-ui-long', { workspaceName })
  const member = await joinedPreparer(token, account.workspaceName)

  await signInAtFrontDoor(page, member, '/settings/members')
  await sweepNameCard(page)

  await page.getByTestId('identity-card-account').click()
  await expect(page).toHaveURL(/\/account$/)
  await expect(page.getByTestId('account-delete-body')).toContainText(workspaceName)
  await sweepPage(page)

  await page.getByTestId('account-delete').click()
  await expect(page.getByTestId('delete-account-panel')).toBeVisible()
  await sweepDialog(page)
  // Nothing is deleted: close the dialog so the member stays as the run found them.
  await page.getByTestId('delete-account-panel').getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page.getByTestId('delete-account-dialog')).toHaveCount(0)
})
