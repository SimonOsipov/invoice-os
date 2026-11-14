// SECAUD-50: a failed or undone import releases its document, so the same file imports for another client.
import { test, expect } from '@playwright/test'
import { buildForm, createEntity, getImportBatch, getInvoice, importFetch, listInvoices, login, PERSONAS, undoImport, uploadDocument } from './client'
import { assertErrorEnvelope } from './contract-helpers'
import { freshTin } from './fixtures'

const HEADER = 'Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price'
const MAPPING: Record<string, string> = {
  invoice_number: 'Invoice No',
  issue_date: 'Issue Date',
  buyer_tin: 'Buyer TIN',
  buyer_name: 'Buyer',
  currency: 'Currency',
  subtotal: 'Subtotal',
  vat: 'VAT',
  total: 'Total',
  line_description: 'Item',
  line_quantity: 'Qty',
  line_unit_price: 'Unit Price',
}

// A per-run invoice number keeps the bytes unique, so no earlier run's binding holds this document.
function uniqueCsv(): string {
  const row = [`INV-REL-${freshTin()}`, '2026-01-15', '87654321-0002', 'Release Buyer', 'NGN', '1000.00', '75.00', '1075.00', 'Item 1', '1', '100.00'].join(',')
  return `${HEADER}\n${row}`
}

function importForm(entityId: string, documentId: string, headerRow?: string): FormData {
  const f = buildForm(entityId, documentId, MAPPING)
  if (headerRow !== undefined) f.set('header_row', headerRow)
  return f
}

test.describe('import release (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test('REL-01 a failed import for A leaves the document free to import for B', async () => {
    const csv = uniqueCsv()
    const lineCount = csv.split('\n').length
    const [a, b] = [
      await createEntity(token, { name: `REL-01 A ${freshTin()}`, tin: freshTin() }),
      await createEntity(token, { name: `REL-01 B ${freshTin()}`, tin: freshTin() }),
    ]
    const documentId = await uploadDocument(token, csv)

    // The last line as the header leaves zero data rows: the batch finalizes "failed" (service.go).
    const failed = await importFetch(token, importForm(a.id, documentId, String(lineCount)))
    expect(failed.status, JSON.stringify(failed.body)).toBe(201)
    expect((failed.body as { status: string }).status).toBe('failed')

    const good = await importFetch(token, importForm(b.id, documentId))
    expect(good.status, JSON.stringify(good.body)).toBe(201)
    const goodBody = good.body as { id: string; status: string }
    expect(goodBody.status).toBe('completed')

    const listed = await listInvoices(token, { import_batch_id: goodBody.id })
    expect(listed.invoices).toHaveLength(1)
  })

  test('REL-02 an undone import for A leaves the document free to import for B', async () => {
    const a = await createEntity(token, { name: `REL-02 A ${freshTin()}`, tin: freshTin() })
    const b = await createEntity(token, { name: `REL-02 B ${freshTin()}`, tin: freshTin() })
    const documentId = await uploadDocument(token, uniqueCsv())

    const first = await importFetch(token, importForm(a.id, documentId))
    expect(first.status, JSON.stringify(first.body)).toBe(201)
    const firstBody = first.body as { id: string; status: string }
    expect(firstBody.status).toBe('completed')

    const before = await getImportBatch(token, firstBody.id)
    expect(before.status).toBe(200)
    expect((before.body as { undoable: boolean }).undoable).toBe(true)
    const [invoice] = (await listInvoices(token, { import_batch_id: firstBody.id })).invoices
    expect(invoice).toBeDefined()

    const undone = await undoImport(token, firstBody.id)
    expect(undone.status, JSON.stringify(undone.body)).toBe(200)
    expect((undone.body as { discarded_invoices: number }).discarded_invoices).toBe(1)

    expect((await getInvoice(token, invoice.id)).status).toBe('discarded')
    expect((await listInvoices(token, { import_batch_id: firstBody.id })).invoices).toHaveLength(0)
    const after = await getImportBatch(token, firstBody.id)
    expect(after.body).toMatchObject({ status: 'undone', undoable: false })

    assertErrorEnvelope(await undoImport(token, firstBody.id), 409, 'a second undo of the same import')

    const second = await importFetch(token, importForm(b.id, documentId))
    expect(second.status, JSON.stringify(second.body)).toBe(201)
    expect((second.body as { status: string }).status).toBe('completed')
  })
})
