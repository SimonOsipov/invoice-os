// Tests for aggregateCustomers/initials (persona-handoff-fix step 3) — this module had
// no test file before this change; aggregateCustomers used to take the mock `Invoice[]`
// (types.ts) and is now rewritten over the LIVE `InvoiceRecord[]` (lib/invoices.ts), so
// this is a fresh suite pinning the new behavior rather than an update to an existing one.
import { describe, expect, it } from 'vitest'

import { aggregateCustomers, initials } from './customers'
import type { InvoiceRecord } from './invoices'

function invoiceRecord(overrides: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-1',
    entity_id: 'e1',
    import_batch_id: null,
    invoice_number: 'INV-001',
    status: 'draft',
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
    ...overrides,
  }
}

describe('aggregateCustomers', () => {
  it('an empty invoice list yields an empty customer list', () => {
    expect(aggregateCustomers([])).toEqual([])
  })

  it('groups rows sharing the same buyer_tin into one customer, summing total and count', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: '20000000-0001', buyer_name: 'Beta Traders', total: '1075.00', issue_date: '2026-06-01' }),
      invoiceRecord({ id: 'b', buyer_tin: '20000000-0001', buyer_name: 'Beta Traders', total: '500.00', issue_date: '2026-06-15' }),
    ]

    const result = aggregateCustomers(rows)

    expect(result).toHaveLength(1)
    expect(result[0]).toMatchObject({ name: 'Beta Traders', tin: '20000000-0001', count: 2, totalNum: 1575, valid: true, last: '2026-06-15' })
  })

  it('two rows with the SAME buyer_tin but different buyer_name still merge under one customer, keyed on TIN (the domain identifier)', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: '20000000-0001', buyer_name: 'Beta Traders' }),
      invoiceRecord({ id: 'b', buyer_tin: '20000000-0001', buyer_name: 'Beta Traders Ltd' }),
    ]

    expect(aggregateCustomers(rows)).toHaveLength(1)
  })

  it('two rows with the SAME buyer_name but different (both present) buyer_tin do NOT merge — TIN wins as the grouping key', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: '20000000-0001', buyer_name: 'Beta Traders' }),
      invoiceRecord({ id: 'b', buyer_tin: '30000000-0002', buyer_name: 'Beta Traders' }),
    ]

    const result = aggregateCustomers(rows)

    expect(result).toHaveLength(2)
    expect(result.map((c) => c.tin).sort()).toEqual(['20000000-0001', '30000000-0002'])
  })

  it('a null buyer_tin falls back to grouping by buyer_name (case-insensitively)', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: null, buyer_name: 'Gamma Foods', total: '100.00' }),
      invoiceRecord({ id: 'b', buyer_tin: null, buyer_name: 'GAMMA FOODS', total: '200.00' }),
    ]

    const result = aggregateCustomers(rows)

    expect(result).toHaveLength(1)
    expect(result[0]).toMatchObject({ count: 2, totalNum: 300 })
  })

  it('a null buyer_tin AND null buyer_name is invalid and merges into one "—" bucket', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: null, buyer_name: null }),
      invoiceRecord({ id: 'b', buyer_tin: null, buyer_name: null }),
    ]

    const result = aggregateCustomers(rows)

    expect(result).toHaveLength(1)
    expect(result[0]).toMatchObject({ name: '—', tin: '', valid: false, count: 2 })
  })

  it('a TIN matching the backend format (8 digits, hyphen, 4 digits) is valid', () => {
    const result = aggregateCustomers([invoiceRecord({ buyer_tin: '20000000-0001' })])

    expect(result[0].valid).toBe(true)
  })

  it('a TIN NOT matching the backend format is invalid, and the invalid tin string is kept verbatim', () => {
    const result = aggregateCustomers([invoiceRecord({ buyer_tin: '12345' })])

    expect(result[0]).toMatchObject({ valid: false, tin: '12345' })
  })

  it('total sums Number(total) across a customer\'s rows; a null total contributes 0', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: '20000000-0001', total: '500.00' }),
      invoiceRecord({ id: 'b', buyer_tin: '20000000-0001', total: null }),
    ]

    expect(aggregateCustomers(rows)[0].totalNum).toBe(500)
  })

  it('last picks the MAX of issue_date ?? created_at across a customer\'s rows', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: '20000000-0001', issue_date: '2026-05-01', created_at: '2026-05-01T00:00:00Z' }),
      invoiceRecord({ id: 'b', buyer_tin: '20000000-0001', issue_date: null, created_at: '2026-07-20T00:00:00Z' }),
    ]

    expect(aggregateCustomers(rows)[0].last).toBe('2026-07-20T00:00:00Z')
  })

  it('sorts customers by totalNum descending', () => {
    const rows = [
      invoiceRecord({ id: 'a', buyer_tin: '20000000-0001', buyer_name: 'Small Co', total: '100.00' }),
      invoiceRecord({ id: 'b', buyer_tin: '30000000-0002', buyer_name: 'Big Co', total: '900.00' }),
    ]

    const result = aggregateCustomers(rows)

    expect(result.map((c) => c.name)).toEqual(['Big Co', 'Small Co'])
  })
})

describe('initials', () => {
  it('takes the first letter of the first two words, uppercased', () => {
    expect(initials('Beta Traders Ltd')).toBe('BT')
  })

  it('strips non-letter characters before splitting on spaces', () => {
    expect(initials('—')).toBe('')
  })
})
