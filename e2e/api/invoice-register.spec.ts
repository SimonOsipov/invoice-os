// GET /v1/invoice-register over the deployed gateway (internal/archive/register.go).
// Own fetch and own XLSX read: client.ts is unchanged. The entity name carries `Zz ` so it never sorts
// ahead of the seeded default client (see evidence-bundle.spec.ts). Every entity here is private to
// the spec, so exact row counts are deterministic.
import { test, expect } from '@playwright/test'
import { unzipSync } from 'fflate'
import {
  apiBase, approveUntilClosed, createEntity, createInvoice, firmApproverTokens, getAuditLog, login, rawFetch, validateInvoice, PERSONAS,
} from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, ensureFirmPolicyActive, type RawResult } from './contract-helpers'
import { sheetRows } from '../xlsxRows'

// internal/archive/invoices.go invoicesCSVHeader.
const HEADER = [
  'invoice_id', 'invoice_number', 'status', 'issue_date', 'currency',
  'subtotal', 'vat', 'total', 'supplier_tin', 'supplier_name', 'buyer_tin', 'buyer_name',
  'irn', 'csid', 'qr_payload', 'rejection_reasons', 'created_at',
  'invoice_kind', 'tax_currency_code', 'payment_status', 'due_date', 'tax_point_date', 'issue_time',
  'supplier_email', 'supplier_telephone', 'supplier_street', 'supplier_city',
  'supplier_postal_zone', 'supplier_country', 'supplier_state', 'supplier_lga',
  'buyer_email', 'buyer_telephone', 'buyer_street', 'buyer_city',
  'buyer_postal_zone', 'buyer_country', 'buyer_state', 'buyer_lga',
  'document_type', 'original_invoice_number',
]

// internal/archive/register.go xlsxContentType.
const XLSX_TYPE = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
// internal/archive/register.go parseRegisterRequest.
const TO_REQUIRED = 'to is required'
const NOT_FOUND = 'not found'

const FROM = '2025-03-01'
const TO = '2025-03-31'

function registerFetch(token: string | null, params: Record<string, string>): Promise<Response> {
  return fetch(`${apiBase()}/api/invoice/v1/invoice-register?${new URLSearchParams(params)}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
}

async function asRaw(res: Response): Promise<RawResult> {
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

function invoiceFields(invoiceNumber: string, issueDate: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: `${issueDate}T00:00:00Z`,
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '99999999-0001',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

test.describe('invoice register (API E2E, over the deployed gateway)', () => {
  let token: string
  let entityId: string
  const run = freshTin()
  const num = (label: string) => `INV-FIRMS-05-${label}-${run}`
  const inFirst = num('0301')
  const inLast = num('0331')
  const outBefore = num('0228')
  const outAfter = num('0401')
  const draft = num('0315')

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    entityId = (await createEntity(token, { name: `Zz FIRMS-05 ${run}`, tin: freshTin() })).id
    const dated: [string, string, boolean][] = [
      [inFirst, '2025-03-01', true],
      [inLast, '2025-03-31', true],
      [outBefore, '2025-02-28', true],
      [outAfter, '2025-04-01', true],
      [draft, '2025-03-15', false],
    ]
    for (const [number, date, validate] of dated) {
      const created = await createInvoice(token, { entity_id: entityId, ...invoiceFields(number, date) })
      if (validate) {
        const result = await validateInvoice(token, created.id)
        expect(result.status, `${number} must have left draft`).not.toBe('draft')
      }
    }
  })

  async function download() {
    const res = await registerFetch(token, { entity_id: entityId, from: FROM, to: TO })
    return { res, bytes: new Uint8Array(await res.arrayBuffer()) }
  }

  test('download: xlsx, named for the client and the period', async () => {
    const { res, bytes } = await download()
    expect(res.status, 'status').toBe(200)
    expect(res.headers.get('Content-Type'), 'Content-Type').toBe(XLSX_TYPE)
    expect(res.headers.get('Content-Disposition'), 'Content-Disposition').toMatch(
      /^attachment; filename=ASComply_invoice-register_Zz-FIRMS-05-[A-Za-z0-9-]+_20250301_20250331\.xlsx$/,
    )
    expect(() => unzipSync(bytes), 'the body must open as a ZIP').not.toThrow()
  })

  test('the register lists the in-period, non-draft invoices under the 41-column header, then the footer', async () => {
    const { bytes } = await download()
    const [header, ...sheet] = sheetRows(bytes)
    expect(header, 'header row').toEqual(HEADER)
    const rows = sheet.slice(0, -1)
    const footer = sheet[sheet.length - 1]
    const col = (name: string) => HEADER.indexOf(name)
    expect(footer[col('invoice_number')], 'footer label').toBe('Net total')
    expect(
      ['subtotal', 'vat', 'total'].map((c) => footer[col(c)]),
      'no invoice in the period is filed',
    ).toEqual(['0.00', '0.00', '0.00'])
    expect(footer.filter((v, i) => v !== '' && ![1, col('subtotal'), col('vat'), col('total')].includes(i)), 'every other footer cell is empty').toEqual([])
    expect(rows.map((r) => r[1]).sort(), 'the register holds exactly the two in-period, non-draft invoices').toEqual(
      [inFirst, inLast].sort(),
    )
    for (const r of rows) expect(r[2], `${r[1]} status`).not.toBe('draft')
    const numbers = rows.map((r) => r[1])
    for (const out of [outBefore, outAfter, draft]) expect(numbers, `${out} is outside the register`).not.toContain(out)
  })

  test('a failed invoice enters Net total when resolved outside and leaves when undone', async () => {
    await ensureFirmPolicyActive(token)
    const tin = freshTin()
    const entity = await createEntity(token, { name: `Zz FIRMS-05 resolve ${tin}`, tin })
    const created = await createInvoice(token, { entity_id: entity.id, ...invoiceFields(`INV-FIRMS-05-RES-${tin}`, '2025-03-15') })
    await validateInvoice(token, created.id)
    await approveUntilClosed(created.id, await firmApproverTokens())
    const auth = { Authorization: `Bearer ${token}` }
    for (const target of ['queued', 'failed']) {
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, { method: 'POST', headers: auth, body: { target } })
      expect(res.status, `setup: transition to ${target} should return 200`).toBe(200)
    }
    const col = (name: string) => HEADER.indexOf(name)
    const netTotal = async () => {
      const res = await registerFetch(token, { entity_id: entity.id, from: FROM, to: TO })
      const [, ...sheet] = sheetRows(new Uint8Array(await res.arrayBuffer()))
      return { rows: sheet.length - 1, footer: ['subtotal', 'vat', 'total'].map((c) => sheet[sheet.length - 1][col(c)]) }
    }
    const resolved = `/api/invoice/v1/invoices/${created.id}/resolved-outside`

    expect(await netTotal(), 'an unresolved failed invoice is listed but not filed').toEqual({ rows: 1, footer: ['0.00', '0.00', '0.00'] })
    const post = await rawFetch(resolved, { method: 'POST', headers: auth, body: { reason: `resolved ${tin}` } })
    expect(post.status, 'resolving should return 200').toBe(200)
    expect(await netTotal(), 'a resolved failed invoice is filed').toEqual({ rows: 1, footer: ['1000.00', '75.00', '1075.00'] })
    const del = await rawFetch(resolved, { method: 'DELETE', headers: auth })
    expect(del.status, 'un-resolving should return 200').toBe(200)
    expect(await netTotal(), 'undoing removes it').toEqual({ rows: 1, footer: ['0.00', '0.00', '0.00'] })
  })

  test('the download is audited with the client, the period and the row count', async () => {
    await download()
    const { events } = await getAuditLog(token, { event: ['invoice_register.exported'], company: entityId })
    const mine = events.filter((e) => e.entity_id === entityId)
    expect(mine.length, 'an invoice_register.exported row for the entity').toBeGreaterThan(0)
    expect(
      mine.map((e) => e.payload),
      'a row with this client, period and row count',
    ).toContainEqual({ entity_id: entityId, from: FROM, to: TO, row_count: 2 })
  })

  test('refusals: 401, 400, 400, 404', async () => {
    const ok = { entity_id: entityId, from: FROM, to: TO }
    assertErrorEnvelope(await asRaw(await registerFetch(null, ok)), 401, 'no token')

    const noTo = await asRaw(await registerFetch(token, { entity_id: entityId, from: FROM }))
    assertErrorEnvelope(noTo, 400, 'missing to')
    expect((noTo.body as { error: string }).error, 'missing to message').toBe(TO_REQUIRED)

    assertErrorEnvelope(await asRaw(await registerFetch(token, { ...ok, from: '2025-03-01T00:00:00Z' })), 400, 'RFC3339 from')

    const tokenB = await login(PERSONAS.B)
    const foreign = await asRaw(await registerFetch(tokenB, ok))
    assertErrorEnvelope(foreign, 404, "tenant B on tenant A's entity")
    expect((foreign.body as { error: string }).error, 'foreign entity message').toBe(NOT_FOUND)
  })
})
