// Tests for ublXml (persona-handoff-fix step 3) — this module had no test file before
// this change; ublXml used to take the mock `Invoice` (types.ts) and is now rewritten
// over the LIVE `InvoiceRecord`/`InvoiceDetailRecord` (lib/invoices.ts), including a
// NEW AccountingSupplierParty block the mock version never rendered at all. Fresh suite
// pinning the new behavior, not an update to an existing one.
import { describe, expect, it } from 'vitest'

import { ublXml } from './xml'
import type { InvoiceRecord } from './invoices'

function invoiceRecord(overrides: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-1',
    entity_id: 'e1',
    import_batch_id: null,
    invoice_number: 'INV-001',
    status: 'validated',
    issue_date: '2026-07-01',
    supplier_tin: '10000000-0001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '20000000-0001',
    buyer_name: 'Beta Traders',
    currency: 'NGN',
    subtotal: '1000.00',
    vat: '75.00',
    total: '1075.00',
    violations: [],
    rule_set_version_id: null,
    created_at: '2026-07-01T00:00:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    rule_set_version: null,
    line_items: [{ id: 'li-1', line_no: 1, description: 'Logistics consulting', quantity: '2', unit_price: '500.00', line_total: '1000.00', line_tax: '75.00' }],
    ...overrides,
  }
}

describe('ublXml', () => {
  it('renders the REAL supplier identity in an AccountingSupplierParty — the block the mock generator never emitted at all', () => {
    const xml = ublXml(invoiceRecord())

    expect(xml).toContain('<cac:AccountingSupplierParty>')
    expect(xml).toContain('<cbc:Name>Acme Ltd</cbc:Name>')
    expect(xml).toContain('<cbc:CompanyID>10000000-0001</cbc:CompanyID>')
  })

  it('renders the buyer identity in an AccountingCustomerParty', () => {
    const xml = ublXml(invoiceRecord())

    expect(xml).toContain('<cac:AccountingCustomerParty>')
    expect(xml).toContain('<cbc:Name>Beta Traders</cbc:Name>')
    expect(xml).toContain('<cbc:CompanyID>20000000-0001</cbc:CompanyID>')
  })

  it('uses the invoice_number and issue_date verbatim', () => {
    const xml = ublXml(invoiceRecord({ invoice_number: 'INV-2026-0042', issue_date: '2026-08-15' }))

    expect(xml).toContain('<cbc:ID>INV-2026-0042</cbc:ID>')
    expect(xml).toContain('<cbc:IssueDate>2026-08-15</cbc:IssueDate>')
  })

  it('derives the VAT percent from the REAL vat/subtotal ratio, never a hardcoded 7.5', () => {
    // 150/1000 = 15%, not the Nigeria-standard 7.5% a hardcoded constant would print.
    const xml = ublXml(invoiceRecord({ subtotal: '1000.00', vat: '150.00' }))

    expect(xml).toContain('<cbc:Percent>15.0</cbc:Percent>')
  })

  it('a zero subtotal renders a 0.0 percent rather than dividing by zero', () => {
    const xml = ublXml(invoiceRecord({ subtotal: '0.00', vat: '0.00' }))

    expect(xml).toContain('<cbc:Percent>0.0</cbc:Percent>')
  })

  it('falls back to NGN when currency is null', () => {
    const xml = ublXml(invoiceRecord({ currency: null }))

    expect(xml).toContain('<cbc:DocumentCurrencyCode>NGN</cbc:DocumentCurrencyCode>')
  })

  it('renders one InvoiceLine per line_items row, using the real per-line fields', () => {
    const xml = ublXml(invoiceRecord())

    expect(xml).toContain('<cac:InvoiceLine>')
    expect(xml).toContain('<cbc:Name>Logistics consulting</cbc:Name>')
    expect(xml).toContain('<cbc:InvoicedQuantity unitCode="EA">2</cbc:InvoicedQuantity>')
  })

  it('an absent line_items array (list-fetch shape) renders zero InvoiceLine blocks without crashing', () => {
    const xml = ublXml(invoiceRecord({ line_items: undefined }))

    expect(xml).not.toContain('<cac:InvoiceLine>')
  })

  it('null supplier/buyer name and TIN render as empty strings, never "null" or "undefined"', () => {
    const xml = ublXml(invoiceRecord({ supplier_name: null, supplier_tin: null, buyer_name: null, buyer_tin: null }))

    expect(xml).not.toContain('null')
    expect(xml).not.toContain('undefined')
  })
})
