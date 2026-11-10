// The deployed check page: the crawler's HTML, and one real check from upload to the sent text.
// The send test writes data, so it skips on production (the nightly runs it on its own preview).
import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import { resolveTarget } from '../targets'
import { seedConsent } from './landingConsent'

const CHECK_URL = resolveTarget('CHECK_URL')
const LIVE = new URL(CHECK_URL).host === 'www.ascomply.com'

// frontend/check/src/copy.ts: CHECK_TITLE, CHECK_DESCRIPTION, CHECK_H1.
const TITLE = 'Free e-invoice readiness check — ASComply Africa'
const DESCRIPTION = 'Upload one invoice file and get the MBS rule results for its first 2 invoices by email. Free, no account needed.'
const H1 = 'Check your invoices against the MBS rules'
// frontend/check/src/copy.ts: CHECK_EYEBROW, TRUST_CUES titles, STEP_LABELS, WHAT_H2[0].
const EYEBROW = 'FREE E-INVOICE READINESS CHECK'
const TRUST_TITLES = ['Free', 'No account', 'File not kept', 'Results by email']
const STEPS = ['Upload', 'Match columns', 'Email', 'Sent']
const WHAT_HEADING = 'Two invoices, checked rule by rule.'
// frontend/check/src/copy.ts: SENT_TEXT.
const sentText = (email: string) => `Your results are on their way to ${email}.`
// internal/gateway/readiness_check.go: checkIPSentence.
const IP_SENTENCE = 'This network has run 10 checks today. Try again tomorrow.'

// Read from the landing source, not retyped: the gateway takes only the sentence the box shows.
const CONSENT_SRC = new URL('../../frontend/landing/src/readinessConsent.ts', import.meta.url)
const CONSENT_MATCH = /export const READINESS_CONSENT_TEXT =\s*'((?:[^'\\]|\\.)*)'/.exec(readFileSync(CONSENT_SRC, 'utf8'))
if (CONSENT_MATCH == null) throw new Error(`${CONSENT_SRC.pathname}: no READINESS_CONSENT_TEXT literal`)
const CONSENT = CONSENT_MATCH[1] as string

// Field label (internal/invoicefields/fields.go) -> the CSV header that holds it.
const COLUMNS: [string, string][] = [
  ['Invoice number', 'Inv'],
  ['Issue date', 'Date'],
  ['Currency', 'Cur'],
  ['Subtotal', 'Net'],
  ['VAT', 'Tax'],
  ['Total', 'Gross'],
  ['Buyer name', 'Buyer'],
  ['Buyer TIN', 'BTIN'],
  ['Supplier name', 'Seller'],
  ['Supplier TIN', 'STIN'],
]
// The second invoice has no supplier TIN (rule supplier-tin-required).
const CSV = [
  COLUMNS.map(([, header]) => header).join(','),
  'INV-001,2026-11-01,NGN,1000,75,1075,Beta Ltd,123456780001,Acme Ltd,123456780002',
  'INV-002,2026-11-02,NGN,2000,150,2150,Beta Ltd,123456780001,Acme Ltd,',
  'INV-003,2026-11-03,NGN,3000,225,3225,Beta Ltd,123456780001,Acme Ltd,123456780002',
].join('\n')

test('check: crawler reads the title, the description and the h1 without JavaScript', async ({ request }) => {
  const res = await request.get(CHECK_URL)
  expect(res.status()).toBe(200)
  expect(res.headers()['content-type']).toContain('text/html')
  const html = await res.text()
  expect(html).toContain(`<title>${TITLE}</title>`)
  expect(html).toContain(`content="${DESCRIPTION}"`)
  expect(html).toContain(`<h1>${H1}</h1>`)
})

  await expect(current).toHaveCount(0)
  expect(errors, `console errors on the check:\n${errors.join('\n')}`).toEqual([])
})
