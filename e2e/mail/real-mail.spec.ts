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
import { brandLayoutFacts, CheckSteps, judgeBrandLayout, judgeSends, mailTextDiff, nightlySendsToday, overBudget, readAccounts, requireEnv } from './mailChecks'
import { contactTopics, getContact, getReceived, listSent, waitForMail } from './resendClient'

// Copies of the Go values: accountmail.InviteSubject and InviteAcceptURL; the recovery and confirmation
// subjects are GOTRUE_MAILER_SUBJECTS_* in scripts/ci/preview.sh mail-on.
const INVITE_SUBJECT = 'You are invited to ASComply'
const INVITE_LINK = 'https://www.ascomply.com/invite#token='
const PASSWORD_SUBJECT = 'Confirm your ASComply account'
// The signup mail has the same subject: GOTRUE_MAILER_SUBJECTS_CONFIRMATION.
const SIGNUP_SUBJECT = PASSWORD_SUBJECT
// accountmail.BlogConfirmSubject and BlogConfirmURL.
const BLOG_SUBJECT = 'Confirm your ASComply blog subscription'
const BLOG_LINK = 'https://www.ascomply.com/blog/subscribe/confirm?token='
const BLOG_SUBSCRIBED = 'You are subscribed.'
const RESET_SUBJECT = 'Reset your ASComply password'
const RESET_LINK_FAILED = 'That reset link did not work. It may have expired or already been used.'
// frontend/landing/src/components/VerifyNotice.tsx COPY.failed.
const VERIFY_FAILED = 'This link is already used or expired. If you confirmed your email, sign in.'
const INVITE_INVALID = 'This invite is no longer valid'
const WAIT_MS = 3 * 60_000
const DOWN_WAIT_MS = 30_000
const TOPIC_POLL_MS = 5_000
// Set by the first failed "... mail arrives" wait; later waits are short so a mail outage ends inside the test timeout.
let mailDown = false
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

function brandStep(html: string): void {
  const reason = judgeBrandLayout(brandLayoutFacts(html))
  if (reason) throw new Error(reason)
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
  let inviteHtml = ''
  let inviteText = ''
  let passwordLink = ''
  let passwordHtml = ''
  let passwordText = ''
  let resetLink = ''
  let resetHtml = ''
  let resetText = ''
  const signupAddr = `nightly-${env.NIGHTLY_RUN_ID}-signup@${env.RESEND_RECEIVING_DOMAIN}`
  const blogAddr = `nightly-${env.NIGHTLY_RUN_ID}-blog@${env.RESEND_RECEIVING_DOMAIN}`
  const signupPassword = crypto.randomUUID().slice(0, 16)
  let signupHtml = ''
  let signupLink = ''
  let blogHtml = ''
  let blogToken = ''

  const arrival = async (to: string, subject: string): Promise<string> => {
    try {
      const mail = await waitForMail(key, to, subject, mailDown ? DOWN_WAIT_MS : WAIT_MS)
      return (await getReceived(key, mail.id)).html ?? ''
    } catch (e) {
      mailDown = true
      throw e
    }
  }

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
      inviteHtml = await arrival(inviteAddr, INVITE_SUBJECT)
      inviteText = await textOf(mailPage, inviteHtml)
      const link = await linkOf(mailPage, 'Accept invite')
      if (!link.startsWith(INVITE_LINK)) throw new Error('the invite link does not start with the accept page')
      inviteToken = decodeURIComponent(link.slice(INVITE_LINK.length))
    })

    await steps.run('invite mail uses the brand layout', () => brandStep(inviteHtml))

    await steps.run('invite text matches', async () => {
      const want = wantHtml(['invite', admin.workspaceName, admin.displayName, inviteAddr, 'reviewer'], inviteToken)
      expectSameText(await textOf(mailPage, want), inviteText)
    })

    await steps.run('invite page registers the invitee', async () => {
      await registerOnAcceptPage(await freshPage(), inviteToken)
    })

    await steps.run('password mail arrives', async () => {
      passwordHtml = await arrival(inviteAddr, PASSWORD_SUBJECT)
      passwordText = await textOf(mailPage, passwordHtml)
      passwordLink = await linkOf(mailPage, 'Choose your password')
      if (!passwordLink.startsWith(`${gateway}/auth/verify?token=`)) throw new Error('the password link does not start at the gateway verify route')
      if (!passwordLink.includes('type=signup')) throw new Error('the password link has no type=signup')
      if (!passwordLink.endsWith('&invite=1')) throw new Error('the password link does not end with invite=1')
    })

    await steps.run('password mail uses the brand layout', () => brandStep(passwordHtml))

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
      resetHtml = await arrival(reset.email, RESET_SUBJECT)
      resetText = await textOf(mailPage, resetHtml)
      resetLink = await linkOf(mailPage, 'Choose a new password')
      if (!resetLink.startsWith(`${gateway}/auth/reset-password?token=`)) throw new Error('the reset link does not start at the gateway reset route')
    })

    await steps.run('reset mail uses the brand layout', () => brandStep(resetHtml))

    await steps.run('reset text matches', async () => {
      expectSameText(await textOf(mailPage, wantHtml(['recovery', reset.email], resetLink)), resetText)
    })

    await steps.run('reset link sets the password', async () => {
      const page = await freshPage()
      await page.goto(resetLink)
      await page.getByLabel('New password', { exact: true }).fill(newPassword)
      await page.getByRole('button', { name: 'Set new password', exact: true }).click()
      await page.waitForURL((u) => u.href.startsWith(landing), { timeout: 30_000 })
      // A failed reset lands here too, with a notice.
      await expect.poll(() => new URL(page.url()).searchParams.has('reset'), { message: 'the landing strips ?reset' }).toBe(false)
      await expect(page.getByRole('status').filter({ hasText: RESET_LINK_FAILED })).toHaveCount(0)
    })

    await steps.run('sign-in with the new password', async () => {
      await signInSession(reset.email, newPassword)
      const old = await rawFetch('/auth/sign-in', { method: 'POST', body: { email: reset.email, password: reset.password, state: mintSignInState() } })
      if (old.status !== 401) throw new Error(`the old password answered ${old.status}, want 401`)
    })

    await steps.run('signup mail arrives', async () => {
      // The body nightlycontacts sends to /auth/register.
      const res = await rawFetch('/auth/register', {
        method: 'POST',
        body: {
          email: signupAddr,
          password: signupPassword,
          workspace_name: 'Nightly mail signup',
          display_name: 'Nightly Signup',
          kind: 'firm',
        },
      })
      if (res.status !== 202) throw new Error(`register answered ${res.status}`)
      signupHtml = await arrival(signupAddr, SIGNUP_SUBJECT)
      await mailPage.setContent(signupHtml, { waitUntil: 'domcontentloaded' })
      signupLink = await linkOf(mailPage, 'Confirm email address')
      if (!signupLink.startsWith(`${gateway}/auth/verify?token=`)) throw new Error('the signup link does not start at the gateway verify route')
      if (!signupLink.includes('type=signup')) throw new Error('the signup link has no type=signup')
      if (signupLink.includes('invite=1')) throw new Error('the signup link carries invite=1')
    })

    await steps.run('signup mail uses the brand layout', () => brandStep(signupHtml))

    await steps.run('signup link confirms the address', async () => {
      const before = await rawFetch('/auth/sign-in', {
        method: 'POST',
        body: {
          email: signupAddr,
          password: signupPassword,
          state: mintSignInState(),
        },
      })
      if (before.status !== 403) throw new Error(`sign-in before the link answered ${before.status}, want 403`)
      const page = await freshPage()
      await seedConsent(page, false)
      await page.goto(signupLink, { waitUntil: 'commit' })
      await page.waitForURL((u) => u.origin === new URL(gateway).origin && u.pathname === '/auth/verify' && u.searchParams.has('state'), {
        timeout: 30_000,
      })
      await page.getByRole('button', { name: 'Confirm my email', exact: true }).click()
      await page.waitForURL((u) => u.href.startsWith(landing), { timeout: 30_000 })
      await expect(page.getByRole('status').filter({ hasText: VERIFY_FAILED })).toHaveCount(0)
      await signInSession(signupAddr, signupPassword)
    })

    await steps.run('newsletter mail arrives', async () => {
      const cats = await rawFetch('/blog/categories')
      const categoryId = (cats.body as { categories?: { id: string }[] } | undefined)?.categories?.[0]?.id
      if (cats.status !== 200 || !categoryId) throw new Error(`categories answered ${cats.status} with no category`)
      const sub = await rawFetch('/blog/subscriptions', {
        method: 'POST',
        body: {
          email: blogAddr,
          category_ids: [categoryId],
          frequency: 'weekly',
        },
      })
      if (sub.status !== 202) throw new Error(`subscribe answered ${sub.status}`)
      blogHtml = await arrival(blogAddr, BLOG_SUBJECT)
      await mailPage.setContent(blogHtml, { waitUntil: 'domcontentloaded' })
      const link = await linkOf(mailPage, 'Confirm subscription')
      if (!link.startsWith(BLOG_LINK)) throw new Error('the newsletter link does not start with the blog confirm page')
      blogToken = decodeURIComponent(link.slice(BLOG_LINK.length))
    })

    await steps.run('newsletter mail uses the brand layout', () => brandStep(blogHtml))

    await steps.run('newsletter link confirms the subscription', async () => {
      const before = await getContact(key, blogAddr)
      if (before !== 404) throw new Error(`Resend answered ${before} for the subscriber before the link, want 404`)
      const page = await freshPage()
      await page.goto(`${resolveTarget('BLOG_URL')}/subscribe/confirm?token=${encodeURIComponent(blogToken)}`)
      await page.getByRole('button', { name: 'Confirm subscription', exact: true }).click()
      await expect(page.getByText(BLOG_SUBSCRIBED)).toBeVisible({
        timeout: 30_000,
      })
    })

    await steps.run('subscriber is a Resend contact on the Blog topic', async () => {
      const topicId = process.env.RESEND_BLOG_TOPIC_ID?.trim()
      if (!topicId) throw new Error('not set: RESEND_BLOG_TOPIC_ID')
      const deadline = Date.now() + WAIT_MS
      let last = 'no Blog topic yet'
      for (;;) {
        try {
          const topic = (await contactTopics(key, blogAddr)).find((t) => t.id === topicId)
          if (topic?.subscription === 'opt_in') return
          last = topic ? `Blog topic subscription is ${topic.subscription}` : 'no Blog topic yet'
        } catch (e) {
          last = e instanceof Error ? e.message : String(e)
        }
        if (Date.now() >= deadline) throw new Error(`the subscriber is not opted in to the Blog topic after 3 minutes (${last})`)
        await new Promise((resolve) => setTimeout(resolve, TOPIC_POLL_MS))
      }
    })

    await steps.run('only these mails are real', async () => {
      const verdict = judgeSends(await listSent(key, windowStart), {
        invite: inviteAddr,
        reset: reset.email,
        signup: signupAddr,
        blog: blogAddr,
      })
      if (!verdict.ok) throw new Error(verdict.detail)
    })
  } finally {
    await mailPage.context().close()
  }

  const section = steps.section()
  expect(section.ok, section.failures.map((f) => `${f.test}: ${f.message}`).join('\n')).toBe(true)
})
