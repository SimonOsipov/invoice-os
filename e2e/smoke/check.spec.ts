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
// frontend/check/src/copy.ts: UPLOAD_STEP_NO, UPLOAD_TITLE, FILE_LABEL, DROP_TITLE, DROP_TITLE_OVER, DROP_BUTTON, FILE_PRIVACY, TYPE_SENTENCE.
const UPLOAD_STEP_NO = '01 · Upload'
const UPLOAD_TITLE = 'Add one invoice file'
const FILE_LABEL = 'Invoice file'
const DROP_TITLE = 'Drag your invoice file here'
const DROP_TITLE_OVER = 'Drop it here to start'
const DROP_BUTTON = 'Choose a file'
const FILE_PRIVACY = 'Your file is used only for this check and deleted when it finishes.'
const TYPE_SENTENCE = 'Choose a CSV, XLSX, PDF or DOCX file.'
const FILE_CHIPS = ['CSV', 'XLSX', 'PDF', 'DOCX']
// frontend/app/src/lib/importFlow.ts: MAX_UPLOAD_BYTES (15 MiB); frontend/check/src/copy.ts: MAX_INVOICES (publiccheck.MaxInvoices).
const DROP_HELP = 'One file, up to 15 MiB. We check its first 2 invoices.'
// frontend/check/src/copy.ts: SENT_TEXT.
const sentText = (email: string) => `Check your inbox at ${email}.`
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

// Writes nothing, so it runs on production too.
test('check: the page shows the hero, the trust cues and the progress strip', async ({ page }) => {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  await seedConsent(page, false)
  await page.goto(CHECK_URL)
  await expect(page.getByText(EYEBROW, { exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { level: 1, name: H1 })).toBeVisible()
  const cues = page.getByRole('list', { name: 'About this check' })
  for (const title of TRUST_TITLES) await expect(cues.getByText(title, { exact: true })).toBeVisible()
  const strip = page.getByRole('list', { name: 'Check progress' })
  await expect(strip.locator('.check-step-lbl')).toHaveText(STEPS)
  await expect(strip.locator('[aria-current="step"] .check-step-lbl')).toHaveText('Upload')
  await expect(page.locator('#what').getByRole('heading', { level: 2 })).toContainText(WHAT_HEADING)
  expect(errors).toEqual([])
})

// Writes nothing, so it runs on production too.
test('check: the drop zone shows its text and refuses a wrong file in place', async ({ page }) => {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  const requests: string[] = []
  page.on('request', (req) => {
    if (req.url().includes('/readiness-check/')) requests.push(req.url())
  })
  await seedConsent(page, false)
  await page.goto(CHECK_URL)
  const card = page.locator('.check-upload')
  for (const text of [UPLOAD_STEP_NO, UPLOAD_TITLE, FILE_LABEL, DROP_TITLE, DROP_BUTTON, DROP_HELP, FILE_PRIVACY]) {
    await expect(card.getByText(text, { exact: true })).toBeVisible()
  }
  await expect(card.locator('.check-chip')).toHaveText(FILE_CHIPS)

  const zone = card.locator('.check-drop')
  const dataTransfer = await page.evaluateHandle(() => {
    const dt = new DataTransfer()
    dt.items.add(new File(['not an invoice'], 'notes.txt', { type: 'text/plain' }))
    return dt
  })
  await zone.dispatchEvent('dragenter', { dataTransfer })
  await expect(card.getByText(DROP_TITLE_OVER, { exact: true })).toBeVisible()
  await zone.dispatchEvent('drop', { dataTransfer })

  const alert = card.getByRole('alert')
  await expect(alert).toHaveText(TYPE_SENTENCE)
  const color = await alert.evaluate((el) => getComputedStyle(el).color)
  await expect(zone).toHaveCSS('border-top-color', color)
  await expect(card.getByText(DROP_TITLE, { exact: true })).toBeVisible()
  expect(requests).toEqual([])
  expect(errors, `console errors on the check:\n${errors.join('\n')}`).toEqual([])
})

test('check: a visitor uploads a spreadsheet, maps it and is told to check the inbox to confirm', async ({ page }) => {
  test.skip(LIVE, 'www.ascomply.com is production: the send writes a contact and a mail')
  test.setTimeout(180_000)
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  let limited = false
  page.on('response', (res) => {
    if (res.url().endsWith('/readiness-check/run') && res.status() === 429) limited = true
  })

  await seedConsent(page, false)
  await page.goto(CHECK_URL)
  const [chooser] = await Promise.all([page.waitForEvent('filechooser'), page.getByText(DROP_BUTTON, { exact: true }).click()])
  await chooser.setFiles({ name: 'invoices.csv', mimeType: 'text/csv', buffer: Buffer.from(CSV) })
  await expect(page.getByRole('heading', { name: 'Match your columns' })).toBeVisible()
  const current = page.getByRole('list', { name: 'Check progress' }).locator('[aria-current="step"] .check-step-lbl')
  await expect(current).toHaveText('Match columns')
  // Choose every column: a preview's AI is fake, the nightly's is real, so the suggestion proves nothing.
  for (const [label, header] of COLUMNS) {
    await page.getByLabel(label, { exact: true }).selectOption(header)
  }
  await page.getByRole('button', { name: 'Continue' }).click()

  await expect(current).toHaveText('Email')
  const email = `check-${crypto.randomUUID()}@example.com`
  await page.getByLabel('Email address', { exact: true }).fill(email)
  await expect(page.locator('.check-consent')).toHaveText(CONSENT)
  await page.locator('.check-consent input').check()
  await page.getByRole('button', { name: 'Send' }).click()
  try {
    await expect(page.getByText(sentText(email))).toBeVisible({ timeout: 120_000 })
  } catch (e) {
    if (limited) throw new Error(`environment: ${IP_SENTENCE} The nightly enforces 10 checks a day per network; wait for the window to clear.`)
    throw e
  }

  await expect(current).toHaveCount(0)
  expect(errors, `console errors on the check:\n${errors.join('\n')}`).toEqual([])
})
