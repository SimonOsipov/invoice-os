// A preview readiness check pressed end to end: run, set the confirm token (mock build only), press.
import { randomBytes } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { confirmCheck, postCheck, setReadinessConfirmToken } from './client'

// Read from the landing source, not retyped: the gateway takes only the sentence the box shows.
const CONSENT_SRC = new URL('../../frontend/landing/src/readinessConsent.ts', import.meta.url)
const CONSENT_MATCH = /export const READINESS_CONSENT_TEXT =\s*'((?:[^'\\]|\\.)*)'/.exec(readFileSync(CONSENT_SRC, 'utf8'))
if (CONSENT_MATCH == null) throw new Error(`${CONSENT_SRC.pathname}: no READINESS_CONSENT_TEXT literal`)
export const READINESS_CONSENT = CONSENT_MATCH[1] as string

const FILE = {
  name: 'invoices.csv',
  content: ['Inv,Date,Cur,Net,Tax,Gross,Buyer,BTIN,Seller,STIN', 'INV-001,2026-11-01,NGN,1000,75,1075,Beta Ltd,123456780001,Acme Ltd,123456780002'].join('\n'),
}
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

export async function pressReadinessCheck(email: string): Promise<void> {
  const run = await postCheck('run', { email, consent_text: READINESS_CONSENT, mapping: MAPPING }, FILE)
  if (run.status !== 200) throw new Error(`readiness run answered ${run.status}: ${JSON.stringify(run.body)}`)
  const token = randomBytes(32).toString('base64url')
  await setReadinessConfirmToken(email, token)
  const pressed = await confirmCheck(token)
  if (pressed.status !== 200) throw new Error(`readiness press answered ${pressed.status}: ${pressed.body}`)
}
