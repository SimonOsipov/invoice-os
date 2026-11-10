// The unsubscribe link end to end: link, page, press, Marketing state, then a later opt-in that is skipped.
// The nightly reads the real new-device mail and follows its link; a preview reads no mail (the topology
// job and every preview send none), so there the link is built from the access token's own token.
import { randomUUID } from 'node:crypto'
import { test, expect } from '@playwright/test'
import { overBudgetFor, readinessMailEnv, EMAIL_RUN_SENDS, UNSUBSCRIBE_RUN_SENDS } from '../mail/mailChecks'
import { getReceived, listSent, waitForMail } from '../mail/resendClient'
import { nightlyTarget } from '../targets'
import { apiBase, contactsMe, fleetHeaders, rawFetch, registerFormAccount, unsubscribeTokenOf, type ContactMe, type RealAccount } from './client'
import { googleIdToken, serviceUrl } from './preview'

// internal/accountmail/accountmail.go: NewDeviceSubject.
const NEW_DEVICE_SUBJECT = 'New sign-in to your ASComply account'
// internal/accountmail/unsubscribe.go: UnsubscribeURL.
const UNSUBSCRIBE_URL = 'https://api.ascomply.com/unsubscribe'
// internal/accountmail/layout.html: the footer line and its link.
const FOOTER = /Don(?:'|&#39;)t want marketing email from ASComply\? <a href="([^"]*)"[^>]*>Unsubscribe<\/a>\./
const UUID = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
// internal/gateway/unsubscribe_page.go: the heading in unsubscribe_page.html, unsubscribeDoneCopy, unsubscribeBadCopy.
const HEADING = 'Unsubscribe from ASComply marketing email'
const DONE_COPY =
  'You are unsubscribed. We will send you no more marketing email. You will still get email about your account and your own requests, such as password resets, sign-in alerts and readiness check results. The blog newsletter has its own unsubscribe link.'
const BAD_COPY = 'This unsubscribe link does not work. Email sam@ascomply.com and we will unsubscribe you.'
// cmd/notifications/main.go: the two unsubscribe routes and the readiness intake.
const LOOKUP_ROUTE = '/internal/contacts/unsubscribe/lookup'
const PRESS_ROUTE = '/internal/contacts/unsubscribe'
const INTAKE = '/internal/contacts/readiness-checks'
const CONSENT = 'Yes, send me product news and offers.'

// Three real browser agents parse to three distinct device pairs, none the empty pair Node signs in with.
const AGENTS = [
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36',
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0',
  'Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36',
]
const HOUR_MS = 3600_000
const MAIL_WAIT_MS = 90_000
const POLL = { timeout: nightlyTarget() ? 120_000 : 30_000, intervals: [500, 1000, 2000] }

async function page(method: 'GET' | 'POST', token: string) {
  const res = await fetch(`${apiBase()}/unsubscribe?t=${encodeURIComponent(token)}`, { method })
  return { status: res.status, html: await res.text() }
}

async function notificationsPost(path: string, body: unknown, opts: { gatewayToken?: boolean } = {}) {
  const base = serviceUrl('notifications')
  const res = await fetch(`${base}${path}`, {
    method: 'POST',
    headers: {
      'X-Serverless-Authorization': `Bearer ${await googleIdToken(base)}`,
      'Content-Type': 'application/json',
      ...(opts.gatewayToken !== false && fleetHeaders()),
    },
    body: JSON.stringify(body),
  })
  return { status: res.status, body: await res.json().catch(() => undefined) }
}

// One device sign-in with its own User-Agent; the worker mails a pair it has not seen.
async function signInAs(account: RealAccount, userAgent: string) {
  const state = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString('base64url')
  const headers = { 'User-Agent': userAgent }
  const signIn = await rawFetch('/auth/sign-in', { method: 'POST', headers, body: { email: account.email, password: account.password, state } })
  if (signIn.status !== 200) throw new Error(`sign-in answered ${signIn.status}`)
  const { code } = signIn.body as { code: string }
  const exchange = await rawFetch('/auth/exchange', { method: 'POST', headers, body: { code, state } })
  if (exchange.status !== 200) throw new Error(`exchange answered ${exchange.status}`)
}

test.describe('unsubscribe link (API E2E)', () => {
  test.describe.serial('an unticked registrant follows the link and presses', () => {
    let token = ''
    let account: RealAccount
    let access = ''
    // The token the page is driven with: the mail's on the nightly, the access token's elsewhere.
    let linkToken = ''

    const me = async () => {
      const row = await contactsMe(access)
      if (row === null) throw new Error('contacts/me answered 404')
      return row
    }

    test("the account's mail carries its unsubscribe link", async () => {
      test.setTimeout(nightlyTarget() ? 480_000 : 60_000)
      // Resolved here, so a missing variable fails this test and not the file.
      const mailEnv = nightlyTarget() ? readinessMailEnv() : null
      if (mailEnv) {
        const sent = await listSent(mailEnv.RESEND_READ_KEY, new Date(Date.now() - 24 * HOUR_MS))
        if (overBudgetFor(sent, mailEnv.RESEND_RECEIVING_DOMAIN, UNSUBSCRIBE_RUN_SENDS + EMAIL_RUN_SENDS)) {
          throw new Error(
            `environment: nightly mails today leave no room for the unsubscribe run (${UNSUBSCRIBE_RUN_SENDS}) and the email run (${EMAIL_RUN_SENDS})`,
          )
        }
      }
      const email = mailEnv ? `nightly-${mailEnv.NIGHTLY_RUN_ID}-unsub-${randomUUID().slice(0, 8)}@${mailEnv.RESEND_RECEIVING_DOMAIN}` : undefined
      const reg = await registerFormAccount('unsub', email ? { email } : {})
      account = reg.account
      access = reg.token
      token = unsubscribeTokenOf(access)
      linkToken = token
      expect(new RegExp(`^${UUID}$`).test(token), 'the access token holds a UUID unsubscribe token').toBe(true)
      // A preview reads no mail.
      if (!mailEnv) return

      let html = ''
      for (const agent of AGENTS) {
        await signInAs(account, agent)
        try {
          const item = await waitForMail(mailEnv.RESEND_READ_KEY, account.email, NEW_DEVICE_SUBJECT, MAIL_WAIT_MS)
          html = (await getReceived(mailEnv.RESEND_READ_KEY, item.id)).html ?? ''
          break
        } catch (e) {
          if (!/^no mail in /.test((e as Error).message)) throw e
        }
      }
      if (html === '') throw new Error(`no new-device mail arrived after ${AGENTS.length} new user agents`)

      const footer = FOOTER.exec(html)
      expect(footer !== null, 'the mail holds the footer line with an Unsubscribe link').toBe(true)
      const href = footer![1]
      const hrefMatch = new RegExp(`^${UNSUBSCRIBE_URL.replace(/[.]/g, '\\.')}\\?t=(${UUID})$`).exec(href)
      expect(hrefMatch !== null, 'the Unsubscribe href is the constant prefix and a UUID token').toBe(true)
      expect(hrefMatch![1] === token, "the mail's token equals the access token's").toBe(true)
      linkToken = hrefMatch![1]
    })

    test('opening the link changes nothing', async () => {
      test.setTimeout(nightlyTarget() ? 240_000 : 60_000)
      // The hand-off lands after sign-in; the later tick must differ from this delivery.
      await expect.poll(async () => ((await contactsMe(access))?.resend.delivered_at ?? null) !== null, POLL).toBe(true)

      const res = await page('GET', linkToken)
      expect(res.status).toBe(200)
      expect(res.html.includes(HEADING), 'the page shows the heading').toBe(true)
      const domain = account.email.slice(account.email.lastIndexOf('@') + 1)
      expect(res.html.includes(`${account.email[0]}***@${domain}`), 'the page shows the masked address').toBe(true)
      expect(res.html.match(/<button\b/g)?.length, 'the page holds one button').toBe(1)
      expect((await me()).marketing_unsubscribed).toBe(false)
    })

    test('pressing unsubscribes, and a second press says the same', async () => {
      const first = await page('POST', linkToken)
      expect(first.status).toBe(200)
      expect(first.html.includes(DONE_COPY), 'the first press shows the done copy').toBe(true)
      expect((await me()).marketing_unsubscribed).toBe(true)

      const second = await page('POST', linkToken)
      expect(second.status).toBe(200)
      expect(second.html.includes(DONE_COPY), 'the second press shows the done copy').toBe(true)
    })

    test('a later tick is skipped', async () => {
      test.setTimeout(nightlyTarget() ? 240_000 : 60_000)
      const at = Date.now()
      const res = await notificationsPost(INTAKE, { email: account.email, marketing_consent_text: CONSENT })
      expect(res.status, JSON.stringify(res.body)).toBe(202)

      let row: ContactMe | null = null
      await expect
        .poll(async () => {
          row = await contactsMe(access)
          const delivered = row?.resend.delivered_at
          return delivered != null && Date.parse(delivered) >= at
        }, POLL)
        .toBe(true)
      expect(row!.marketing_unsubscribed).toBe(true)
      expect(row!.resend.opt_in_sent, 'the skipped tick sent no opt-in').toBe(false)
      expect(row!.marketing_eligible).toBe(false)
    })

    test('a tampered link does not work', async () => {
      const last = token.at(-1) === '0' ? '1' : '0'
      const res = await page('GET', token.slice(0, -1) + last)
      expect(res.status).toBe(404)
      expect(res.html.includes(BAD_COPY), 'the page shows the bad-link copy').toBe(true)
    })
  })

  test('the unsubscribe routes refuse a call without the gateway token', async () => {
    for (const route of [LOOKUP_ROUTE, PRESS_ROUTE]) {
      const res = await notificationsPost(route, { token: randomUUID() }, { gatewayToken: false })
      expect(res.status, route).toBe(401)
    }
  })
})
