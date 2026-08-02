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

// task-334 (BUG-01-08): aggregateCustomers itself is a plain forEach reduce with no
// page-size assumption -- this pins that it already handles a whole-set (259-row,
// 256-buyer) input correctly. The real gap this story fixes is the CALLER only ever
// handing it a 50-row page (CustomersView.test.tsx covers that).
describe('aggregateCustomers over a whole two-page set ([customers-aggregate-by-paging])', () => {
  // 3 buyers appear twice, both occurrences inside the first 50 rows (47 distinct there);
  // 44 more single-occurrence buyers fill out the first 50; 209 more land past row 50.
  // 3+44+209 = 256 distinct buyers, 6+44+209 = 259 rows -- mirrors the story's own
  // measured numbers (a 50-row page reads 47 of 256).
  function buildWholeSet(): InvoiceRecord[] {
    const rows: InvoiceRecord[] = []
    for (let i = 0; i < 3; i++) {
      const tin = `2000000${i}-0001`
      rows.push(invoiceRecord({ id: `dup-${i}-a`, buyer_tin: tin, buyer_name: `Dup Buyer ${i}`, total: '100.00' }))
      rows.push(invoiceRecord({ id: `dup-${i}-b`, buyer_tin: tin, buyer_name: `Dup Buyer ${i}`, total: '100.00' }))
    }
    for (let i = 0; i < 44; i++) {
      rows.push(invoiceRecord({ id: `p1-${i}`, buyer_tin: `3000${String(i).padStart(4, '0')}-0001`, buyer_name: `Page1 Buyer ${i}`, total: '100.00' }))
    }
    for (let i = 0; i < 209; i++) {
      rows.push(invoiceRecord({ id: `p2-${i}`, buyer_tin: `4000${String(i).padStart(4, '0')}-0001`, buyer_name: `Page2 Buyer ${i}`, total: '100.00' }))
    }
    return rows
  }

  it('a 50-row slice undercounts at 47 buyers -- the exact shape a single-page fetch used to see', () => {
    const slice = buildWholeSet().slice(0, 50)
    expect(slice).toHaveLength(50)
    expect(aggregateCustomers(slice)).toHaveLength(47)
  })

  it('the whole 259-row, 256-buyer set counts every buyer and sums every invoice', () => {
    const all = buildWholeSet()
    expect(all).toHaveLength(259)

    const result = aggregateCustomers(all)

    expect(result).toHaveLength(256)
    const expectedTotal = all.reduce((s, r) => s + Number(r.total), 0)
    const actualTotal = result.reduce((s, c) => s + c.totalNum, 0)
    expect(actualTotal).toBe(expectedTotal)
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
