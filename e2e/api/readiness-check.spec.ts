// A readiness check through the deployed gateway: the worker mails the results, the spec reads the
// captured mail. The nightly enforces the per-IP limit of 10 a day: the group makes 4 checks per attempt.
import { test, expect } from '@playwright/test'
import { nightlyTarget } from '../targets'
import { capturedMail, contactsDirect, postCheck, workerPost, type CheckMapping } from './client'

// internal/gateway/readiness_check.go: checkConsentSentence, checkSignUpSentence, checkIPSentence.
const CONSENT_SENTENCE = 'consent_text must be 1 to 500 characters'
const SIGN_UP_SENTENCE = 'This address has used its 3 free checks. Sign up to check every invoice.'
const IP_SENTENCE = 'This network has run 10 checks today. Try again tomorrow.'
// internal/accountmail/accountmail.go: ReadinessCheckSubject.
const MAIL_SUBJECT = 'Your ASComply readiness check results'
// internal/accountmail/accountmail.go RenderReadinessCheck: Intro and ChecksLine.
const TWO_OF_THREE = '2 of 3 invoices checked'
const TWO_LEFT = 'You have 2 free checks left.'
// internal/invoicefields/fields.go: Label of supplier_tin.
const SUPPLIER_TIN_LABEL = 'Supplier TIN'
// internal/notifications/store.go: the tag contacts/me returns.
const TAG = 'readiness check'
const CONSENT = 'Yes, send me product news and offers.'

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
const POLL = { timeout: 30_000, intervals: [500, 1000, 2000] }
// The nightly delivers to the real vendors, which is slower than a PR's fake mode.
const DELIVERY_POLL = { timeout: nightlyTarget() ? 120_000 : 30_000, intervals: [500, 1000, 2000] }

// ceiling: a third attempt in one window can meet the 429; it is an environment failure, not a product one.
async function run(email: string) {
  const res = await postCheck('run', { email, consent_text: CONSENT, mapping: MAPPING }, FILE)
  if (res.status === 429) {
    throw new Error(
      `environment: ${IP_SENTENCE} The nightly enforces 10 checks a day per network and this suite makes 4 per attempt; wait for the window to clear.`,
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

  test.describe.serial('one address, four runs', () => {
    const email = freshEmail()

    test('a run mails the results to the address', async () => {
      const res = await run(email)
      expect(res.status, JSON.stringify(res.body)).toBe(200)
      expect(res.body).toEqual({ status: 'sent', checks_left: 2 })

      let mail: { subject: string; html: string }[] = []
      await expect.poll(async () => (mail = await capturedMail(email)).length, POLL).toBe(1)
      expect(mail[0].subject).toBe(MAIL_SUBJECT)
      for (const want of [TWO_OF_THREE, SUPPLIER_TIN_LABEL, FIRST_TOTAL, FIRST_BUYER, TWO_LEFT]) {
        expect(mail[0].html, want).toContain(want)
      }
    })

    test('the address becomes a readiness check contact opted in to Marketing', async () => {
      test.setTimeout(nightlyTarget() ? 180_000 : 60_000)
      let row: Awaited<ReturnType<typeof contactsDirect>> = null
      await expect.poll(async () => ((row = await contactsDirect(email))?.resend.delivered_at ?? null) !== null, DELIVERY_POLL).toBe(true)
      expect(row!.tags).toContain(TAG)
      expect(row!.marketing_eligible).toBe(true)
      expect(row!.resend.applies).toBe(true)
    })

    test('the fourth run on one address is told to sign up', async () => {
      const second = await run(email)
      expect(second.body).toEqual({ status: 'sent', checks_left: 1 })
      const third = await run(email)
      expect(third.body).toEqual({ status: 'sent', checks_left: 0 })

      const fourth = await run(email)
      expect(fourth.status).toBe(409)
      expect(fourth.body).toEqual({ error: SIGN_UP_SENTENCE })
      await expect.poll(async () => (await capturedMail(email)).length, POLL).toBe(3)
    })
  })
})
