// A readiness check through the deployed gateway. A run holds the check and mails a confirmation link; the
// press sends the results. On the nightly the spec reads both mails from the real inbox; a preview captures
// them and sets the confirm token instead. The nightly enforces the per-IP limit of 10 a day: the e2e job makes
// 5 checks per attempt (4 here, 1 in smoke).
import { randomBytes, randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { test, expect } from '@playwright/test'
import { readinessBudgetProblem, readinessMailEnv } from '../mail/mailChecks'
import { getReceived, listSent, settledMailCount, waitForMailCount } from '../mail/resendClient'
import { nightlyTarget } from '../targets'
import { confirmCheck, contactsDirect, contactsMe, postCheck, registerFormAccount, setReadinessConfirmToken, workerPost, type CheckMapping } from './client'

// internal/gateway/readiness_check.go: checkConsentSentence, checkIPSentence.
const CONSENT_SENTENCE = 'consent_text must be 1 to 500 characters'
const IP_SENTENCE = 'This network has run 10 checks today. Try again tomorrow.'
// internal/accountmail/accountmail.go: ReadinessCheckSubject, ReadinessConfirmSubject, ReadinessUsedSubject.
const MAIL_SUBJECT = 'Your ASComply readiness check results'
const CONFIRM_SUBJECT = 'Confirm your email to get your readiness check results'
const USED_SUBJECT = 'This address has used its free readiness checks'
// internal/gateway/readiness_confirm_page.go: confirmDoneCopy, confirmExpiredCopy (its link text left out).
const DONE_COPY = 'Done. Your email from no-reply@ascomply.com is on its way. Check your spam folder if you cannot find it.'
const EXPIRED_COPY = 'This link has expired or was already used.'
// internal/accountmail/accountmail.go: ReadinessConfirmURL; the token is 43 base64url characters (readinesscheck tokenLength).
const CONFIRM_LINK = /readiness-check\/confirm\?t=([A-Za-z0-9_-]{43})/
// internal/accountmail/accountmail.go RenderReadinessCheck: Intro and ChecksLine.
const TWO_OF_THREE = '2 of 3 invoices checked'
const TWO_LEFT = 'You have 2 free checks left.'
// internal/invoicefields/fields.go: Label of supplier_tin.
const SUPPLIER_TIN_LABEL = 'Supplier TIN'
// internal/notifications/store.go: the tag contacts/me returns.
const TAG = 'readiness check'
// Read from the landing source, not retyped: the gateway takes only the sentence the box shows.
const CONSENT_SRC = new URL('../../frontend/landing/src/readinessConsent.ts', import.meta.url)
const CONSENT_MATCH = /export const READINESS_CONSENT_TEXT =\s*'((?:[^'\\]|\\.)*)'/.exec(readFileSync(CONSENT_SRC, 'utf8'))
if (CONSENT_MATCH == null) throw new Error(`${CONSENT_SRC.pathname}: no READINESS_CONSENT_TEXT literal`)
const CONSENT = CONSENT_MATCH[1] as string

const HEADER = ['Inv', 'Date', 'Cur', 'Net', 'Tax', 'Gross', 'Buyer', 'BTIN', 'Seller', 'STIN']
// The second invoice has no supplier TIN (rule supplier-tin-required).
const CSV = [
  HEADER.join(','),
  'INV-001,2026-11-01,NGN,1000,75,1075,Beta Ltd,123456780001,Acme Ltd,123456780002',
  'INV-002,2026-11-02,NGN,2000,150,2150,Beta Ltd,123456780001,Acme Ltd,',
  'INV-003,2026-11-03,NGN,3000,225,3225,Beta Ltd,123456780001,Acme Ltd,123456780002',
].join('\n')
const FILE = { name: 'invoices.csv', content: CSV }
const MAPPING = JSON.stringify({
  invoice_number: 'Inv',
  issue_date: 'Date',
  currency: 'Cur',
  subtotal: 'Net',
  vat: 'Tax',
  total: 'Gross',
  buyer_name: 'Buyer',
  buyer_tin: 'BTIN',
  supplier_name: 'Seller',
  supplier_tin: 'STIN',
})
const FIRST_TOTAL = '1075.00'
const FIRST_BUYER = 'Beta Ltd'

const freshEmail = () => `readiness-${crypto.randomUUID()}@example.com`
const HOUR_MS = 3600_000
const RECEIVE_MS = 120_000
// The nightly delivers to the real vendors, which is slower than a PR's fake mode.
const DELIVERY_POLL = { timeout: nightlyTarget() ? 120_000 : 30_000, intervals: [500, 1000, 2000] }

// ceiling: a third attempt in one window can meet the 429; it is an environment failure, not a product one.
async function run(email: string) {
  const res = await postCheck('run', { email, consent_text: CONSENT, mapping: MAPPING }, FILE)
  if (res.status === 429) {
    throw new Error(
      `environment: ${IP_SENTENCE} The nightly enforces 10 checks a day per network and the e2e job makes 5 per attempt (4 here, 1 in smoke); wait for the window to clear.`,
    )
  }
  return res
}

test.describe('readiness check (API E2E, gateway to worker)', () => {
  test('the mapping route answers the spreadsheet columns', async () => {
    const res = await postCheck('mapping', {}, FILE)
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    expect((res.body as CheckMapping).columns).toEqual(HEADER)
  })

  test('a run without consent is refused', async () => {
    const res = await postCheck('run', { email: freshEmail() }, FILE)
    expect(res.status).toBe(400)
    expect(res.body).toEqual({ error: CONSENT_SENTENCE })
  })

  test('the worker route refuses a call without the gateway token', async () => {
    const res = await workerPost('/internal/public-checks/run', {}, { gatewayToken: false })
    expect(res.status).toBe(401)
  })

  test('the worker has no captured-mail route', async () => {
    const res = await workerPost('/internal/public-checks/captured-mail', { to: freshEmail() })
    expect(res.status).toBe(404)
  })

  test.describe.serial('one address, four runs', () => {
    test.describe.configure({ retries: 0 })
    // The nightly reads the real inbox; a preview captures the mail and nothing reads it.
    // Resolved in the tests, so a missing variable fails these and not the whole file.
    let cached: { mailEnv: ReturnType<typeof readinessMailEnv> | null; email: string } | undefined
    const inbox = () =>
      (cached ??= (() => {
        const mailEnv = nightlyTarget() ? readinessMailEnv() : null
        const email = mailEnv
          ? `nightly-${mailEnv.NIGHTLY_RUN_ID}-readiness-${randomUUID().slice(0, 8)}@${mailEnv.RESEND_RECEIVING_DOMAIN}`
          : freshEmail()
        return { mailEnv, email }
      })())
    // Never names the real address or domain: matchers print their received value into the report.
    const countError = (email: string, want: number, found: number) =>
      new Error(`${email.split('@')[0]}@<receiving domain>: want exactly ${want} mails, found ${found} after ${RECEIVE_MS / 60_000} minutes`)
    // Slack for clock skew; the address is unique to this attempt.
    const since = new Date(Date.now() - 60_000)

    // The nth run's confirm token: the nightly reads the link of the newest unread confirmation mail; a preview sets one.
    const seen = new Set<string>()
    async function tokenOfRun(n: number): Promise<string> {
      const { mailEnv, email } = inbox()
      if (!mailEnv) {
        const token = randomBytes(32).toString('base64url')
        await setReadinessConfirmToken(email, token)
        return token
      }
      const items = await waitForMailCount(mailEnv.RESEND_READ_KEY, email, CONFIRM_SUBJECT, n, since, RECEIVE_MS)
      const fresh = items.filter((i) => !seen.has(i.id))
      if (fresh.length !== 1) throw countError(email, 1, fresh.length)
      seen.add(fresh[0].id)
      const link = CONFIRM_LINK.exec((await getReceived(mailEnv.RESEND_READ_KEY, fresh[0].id)).html ?? '')
      if (link === null) throw new Error('the confirmation mail holds no confirm link')
      return link[1] as string
    }
    async function press(n: number): Promise<void> {
      const res = await confirmCheck(await tokenOfRun(n))
      expect(res.status, res.body).toBe(200)
      expect(res.body).toContain(DONE_COPY)
    }

    test('a run asks for confirmation and makes no contact until the press', async () => {
      test.setTimeout(240_000)
      const { mailEnv, email } = inbox()
      if (mailEnv) {
        const problem = readinessBudgetProblem(
          await listSent(mailEnv.RESEND_READ_KEY, new Date(Date.now() - 24 * HOUR_MS)),
          mailEnv.RESEND_RECEIVING_DOMAIN,
        )
        if (problem !== null) throw new Error(problem)
      }
      const res = await run(email)
      expect(res.status, JSON.stringify(res.body)).toBe(200)
      expect(res.body).toEqual({ status: 'confirm' })
      // The old code wrote the contact before it answered.
      expect(await contactsDirect(email), 'a contact before the press').toBeNull()
      await press(1)
    })

    test('nightly: the results mail arrives after the press', async () => {
      test.skip(!nightlyTarget(), 'a preview captures the mail and nothing reads it')
      test.setTimeout(180_000)
      const { mailEnv, email } = inbox()
      if (!mailEnv) throw new Error('nightly without a mail environment')
      const items = await waitForMailCount(mailEnv.RESEND_READ_KEY, email, MAIL_SUBJECT, 1, since, RECEIVE_MS)
      if (items.length !== 1) throw countError(email, 1, items.length)
      const mail = await getReceived(mailEnv.RESEND_READ_KEY, items[0].id)
      expect(mail.subject === MAIL_SUBJECT, `the mail subject is not "${MAIL_SUBJECT}"`).toBe(true)
      const html = mail.html ?? ''
      for (const want of [TWO_OF_THREE, SUPPLIER_TIN_LABEL, FIRST_TOTAL, FIRST_BUYER, TWO_LEFT]) {
        expect(html.includes(want), want).toBe(true)
      }
    })

    test('the address becomes a readiness check contact opted in to Marketing', async () => {
      test.setTimeout(nightlyTarget() ? 180_000 : 60_000)
      const { email } = inbox()
      let row: Awaited<ReturnType<typeof contactsDirect>> = null
      await expect.poll(async () => ((row = await contactsDirect(email))?.resend.delivered_at ?? null) !== null, DELIVERY_POLL).toBe(true)
      expect(row!.tags).toContain(TAG)
      expect(row!.marketing_eligible).toBe(true)
      expect(row!.resend.applies).toBe(true)
    })

    test('the fourth run gets the same answer and holds nothing', async () => {
      // Runs 2-4, then the fixed 2-minute hold on the inbox.
      test.setTimeout(480_000)
      const { mailEnv, email } = inbox()
      const first = { status: 200, body: { status: 'confirm' } }
      const second = await run(email)
      expect(second.body).toEqual(first.body)
      await press(2)
      const third = await run(email)
      expect(third.body).toEqual(first.body)
      await press(3)

      // internal/readinesscheck/handlers.go: MaxRunsPerEmail = 3; the fourth run is answered like a fresh address.
      const fourth = await run(email)
      expect({ status: fourth.status, body: fourth.body }).toEqual(first)
      if (!mailEnv) {
        // Nothing is held, so there is no token to replace.
        await expect(setReadinessConfirmToken(email, randomBytes(32).toString('base64url'))).rejects.toThrow(/404/)
        return
      }

      const results = await settledMailCount(mailEnv.RESEND_READ_KEY, email, MAIL_SUBJECT, since, RECEIVE_MS)
      if (results.length !== 3) throw countError(email, 3, results.length)
      const confirms = await settledMailCount(mailEnv.RESEND_READ_KEY, email, CONFIRM_SUBJECT, since, 0)
      if (confirms.length !== 3) throw countError(email, 3, confirms.length)
      const usedMails = await settledMailCount(mailEnv.RESEND_READ_KEY, email, USED_SUBJECT, since, 0)
      if (usedMails.length !== 1) throw countError(email, 1, usedMails.length)
    })
  })

  test('a press of an unknown link is expired', async () => {
    // A well-shaped token nothing issued.
    const unknown = await confirmCheck(randomBytes(32).toString('base64url'))
    expect(unknown.status).toBe(410)
    expect(unknown.body).toContain(EXPIRED_COPY)
    const malformed = await confirmCheck('abc')
    expect(malformed.status).toBe(404)
  })

  test('a registrant who confirms a check becomes marketing-eligible', async () => {
    test.skip(nightlyTarget(), 'one more run would pass the nightly 10-a-day IP budget')
    test.setTimeout(120_000)
    const { account, token } = await registerFormAccount('readiness-confirm-reg')
    const res = await run(account.email)
    expect(res.body).toEqual({ status: 'confirm' })
    expect((await contactsMe(token))?.marketing_eligible ?? false, 'eligible before the press').toBe(false)
    const confirmToken = randomBytes(32).toString('base64url')
    await setReadinessConfirmToken(account.email, confirmToken)
    const pressed = await confirmCheck(confirmToken)
    expect(pressed.status, pressed.body).toBe(200)
    await expect.poll(async () => (await contactsMe(token))?.marketing_eligible ?? false, DELIVERY_POLL).toBe(true)
  })
})
