import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test, type Page } from '@playwright/test'

import { me, mintSignInState, rawFetch, requestPasswordReset, signInSession } from '../api/client'
import { openInvite, readSessionIdentity, registerOnAcceptPage } from '../inviteSession'
import { seedConsent } from '../smoke/landingConsent'
import { resolveTarget } from '../targets'
import { collectSourcedErrors, expectedStatusDropper } from '../topology/consoleGate'
import { CheckSteps, judgeSends, mailTextDiff, nightlySendsToday, overBudget, readAccounts, requireEnv } from './mailChecks'
import { getReceived, listSent, waitForMail } from './resendClient'

// Copies of the Go values: accountmail.InviteSubject and InviteAcceptURL; the recovery and confirmation
// subjects are GOTRUE_MAILER_SUBJECTS_* in scripts/ci/preview.sh mail-on.
const INVITE_SUBJECT = 'You are invited to ASComply'
const INVITE_LINK = 'https://www.ascomply.com/invite#token='
const PASSWORD_SUBJECT = 'Confirm your ASComply account'
const RESET_SUBJECT = 'Reset your ASComply password'
const INVITE_INVALID = 'This invite is no longer valid'
const WAIT_MS = 3 * 60_000
const REPO_ROOT = fileURLToPath(new URL('../..', import.meta.url))

// The expected mail, rendered by the Go template with the received link; the secret goes on stdin, never argv.
function wantHtml(args: string[], secret: string): string {
  return execFileSync('go', ['run', './internal/tools/mailwant', ...args], { cwd: REPO_ROOT, input: secret, encoding: 'utf8' })
}

async function textOf(page: Page, html: string): Promise<string> {
  await page.setContent(html, { waitUntil: 'domcontentloaded' })
  return page.evaluate(() => document.body.innerText)
}

async function linkOf(page: Page, label: string): Promise<string> {
  const href = await page.getByRole('link', { name: label, exact: true }).first().getAttribute('href')
  if (!href) throw new Error(`no "${label}" link in the mail`)
  return href
}

function expectSameText(want: string, got: string): void {
  const diff = mailTextDiff(want, got)
  if (diff) throw new Error(diff)
}

test('real mail: the invite and the reset arrive, read right, and their links work', async ({ browser }) => {
  test.setTimeout(840_000)
  const env = requireEnv('RESEND_READ_KEY', 'RESEND_RECEIVING_DOMAIN', 'NIGHTLY_RUN_ID', 'MAIL_WINDOW_START', 'MAIL_SECTION_FILE', 'MAIL_ACCOUNTS_FILE')
  const windowStart = new Date(env.MAIL_WINDOW_START)
  if (Number.isNaN(windowStart.getTime())) throw new Error('MAIL_WINDOW_START is not a date')
  const { admin, reset } = readAccounts(env.MAIL_ACCOUNTS_FILE)
  const key = env.RESEND_READ_KEY
  const inviteAddr = `nightly-${env.NIGHTLY_RUN_ID}-invite@${env.RESEND_RECEIVING_DOMAIN}`
  const gateway = resolveTarget('GATEWAY_URL')
  const landing = resolveTarget('LANDING_URL')

  mkdirSync(dirname(env.MAIL_SECTION_FILE), { recursive: true })
  const steps = new CheckSteps(env.MAIL_SECTION_FILE)

  // One page renders every received and expected mail; remote content stays blocked.
  const mailPage = await (await browser.newContext()).newPage()
  await mailPage.route(/^https?:/, (route) => route.abort())

  const freshPage = async () => (await browser.newContext()).newPage()
  const invitePassword = crypto.randomUUID().slice(0, 16)
  const newPassword = crypto.randomUUID().slice(0, 16)
  let tenantId = ''
  let inviteToken = ''
  let inviteText = ''
  let passwordLink = ''
  let passwordText = ''
  let resetLink = ''
  let resetText = ''

  try {
    await steps.run('mail budget', async () => {
      const sends = await listSent(key, new Date(Date.now() - 24 * 3600 * 1000))
      if (overBudget(sends, env.RESEND_RECEIVING_DOMAIN)) {
        const n = nightlySendsToday(sends, env.RESEND_RECEIVING_DOMAIN)
        throw new Error(`${n} nightly mails went out today; the budget is spent`)
      }
    })

    await steps.run('invite mail arrives', async () => {
      const adminToken = (await signInSession(admin.email, admin.password)).access_token
      tenantId = (await me(adminToken)).tenant.id
      const res = await rawFetch('/api/tenancy/v1/invitations', {
        method: 'POST',
        headers: { Authorization: `Bearer ${adminToken}` },
        body: { emails: [inviteAddr], role: 'reviewer' },
      })
      if (res.status !== 200) throw new Error(`invite answered ${res.status}`)
      const mail = await waitForMail(key, inviteAddr, INVITE_SUBJECT, WAIT_MS)
      inviteText = await textOf(mailPage, (await getReceived(key, mail.id)).html ?? '')
      const link = await linkOf(mailPage, 'Accept invite')
      if (!link.startsWith(INVITE_LINK)) throw new Error('the invite link does not start with the accept page')
      inviteToken = decodeURIComponent(link.slice(INVITE_LINK.length))
    })

    await steps.run('invite text matches', async () => {
      const want = wantHtml(['invite', admin.workspaceName, admin.displayName, inviteAddr, 'reviewer'], inviteToken)
      expectSameText(await textOf(mailPage, want), inviteText)
    })

    await steps.run('invite page registers the invitee', async () => {
      await registerOnAcceptPage(await freshPage(), inviteToken)
    })

    await steps.run('password mail arrives', async () => {
      const mail = await waitForMail(key, inviteAddr, PASSWORD_SUBJECT, WAIT_MS)
      passwordText = await textOf(mailPage, (await getReceived(key, mail.id)).html ?? '')
      passwordLink = await linkOf(mailPage, 'Choose your password')
      if (!passwordLink.startsWith(`${gateway}/auth/verify?token=`)) throw new Error('the password link does not start at the gateway verify route')
      if (!passwordLink.includes('type=signup')) throw new Error('the password link has no type=signup')
      if (!passwordLink.endsWith('&invite=1')) throw new Error('the password link does not end with invite=1')
    })

    await steps.run('password mail text matches', async () => {
      // The template appends &invite=1 itself.
      const want = wantHtml(['confirmation', inviteAddr], passwordLink.slice(0, -'&invite=1'.length))
      expectSameText(await textOf(mailPage, want), passwordText)
    })

    await steps.run('password link opens the workspace', async () => {
      const page = await freshPage()
      await seedConsent(page, false)
      // The first sign-in of an invitee reads /v1/me before it has a workspace: a 403.
      const errors = collectSourcedErrors(page, expectedStatusDropper(page, 403, /\/api\/tenancy\/v1\/me$/))
      await page.goto(passwordLink)
      await page.getByLabel('Password', { exact: true }).fill(invitePassword)
      await page.getByRole('button', { name: 'Confirm and set password', exact: true }).click()
      await page.getByTestId('join-name').fill('Mail Invitee')
      await page.getByRole('button', { name: `Join ${admin.workspaceName}`, exact: true }).click()
      const identity = await readSessionIdentity(page)
      expect(identity.tenant.id, 'the session is bound to the inviting workspace').toBe(tenantId)
      expect(identity.user.role, 'the invited role').toBe('reviewer')
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    await steps.run('used invite link is refused', async () => {
      const page = await freshPage()
      await seedConsent(page, false)
      await openInvite(page, inviteToken)
      await expect(page.getByRole('heading', { name: INVITE_INVALID, exact: true })).toBeVisible({ timeout: 30_000 })
    })

    await steps.run('reset mail arrives', async () => {
      const res = await requestPasswordReset({ email: reset.email })
      if (res.status !== 202) throw new Error(`reset request answered ${res.status}`)
      const mail = await waitForMail(key, reset.email, RESET_SUBJECT, WAIT_MS)
      resetText = await textOf(mailPage, (await getReceived(key, mail.id)).html ?? '')
      resetLink = await linkOf(mailPage, 'Choose a new password')
      if (!resetLink.startsWith(`${gateway}/auth/reset-password?token=`)) throw new Error('the reset link does not start at the gateway reset route')
    })

    await steps.run('reset text matches', async () => {
      expectSameText(await textOf(mailPage, wantHtml(['recovery', reset.email], resetLink)), resetText)
    })

    await steps.run('reset link sets the password', async () => {
      const page = await freshPage()
      await page.goto(resetLink)
      await page.getByLabel('New password', { exact: true }).fill(newPassword)
      await page.getByRole('button', { name: 'Set new password', exact: true }).click()
      await page.waitForURL((u) => u.href.startsWith(landing), { timeout: 30_000 })
    })

    await steps.run('sign-in with the new password', async () => {
      await signInSession(reset.email, newPassword)
      const old = await rawFetch('/auth/sign-in', { method: 'POST', body: { email: reset.email, password: reset.password, state: mintSignInState() } })
      if (old.status !== 401) throw new Error(`the old password answered ${old.status}, want 401`)
    })

    await steps.run('only these mails are real', async () => {
      const verdict = judgeSends(await listSent(key, windowStart), inviteAddr, reset.email)
      if (!verdict.ok) throw new Error(verdict.detail)
    })
  } finally {
    await mailPage.context().close()
  }

  const section = steps.section()
  expect(section.ok, section.failures.map((f) => `${f.test}: ${f.message}`).join('\n')).toBe(true)
})
