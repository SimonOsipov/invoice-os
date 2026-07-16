// Specs for the Map step's pure logic (Platform.dc.html ~L1369-1441): header
// recognition, the initial mapping, and grouping line-item rows into invoices.
//
// These pin the two behaviours the Map step exists to guarantee: that
// invoice_number is never auto-guessed (a plausible wrong default on the fiscal
// identifier invites rubber-stamping), and that rows of one invoice disagreeing
// on a header field quarantine that invoice while citing its spreadsheet rows.
// Pure functions — no React, no fetch, no mocking needed.
import { describe, expect, it } from 'vitest'

import { CANON, FILE_DATA } from '../data'
import { groupInvoices, initMapping, recognize } from './mapping'

const HEADERS = FILE_DATA.csv.headers

describe('recognize', () => {
  it('auto-places the seven columns whose headers clearly match', () => {
    const rec = recognize(HEADERS)
    expect(rec).toMatchObject({
      issue_date: 'Issue Date',
      buyer_tin: 'Buyer TIN',
      currency: 'Currency',
      vat: 'VAT',
      total: 'Total',
      line_quantity: 'Qty',
      line_unit_price: 'Unit Price',
    })
  })

  it('never guesses invoice_number, even though a plausible column exists', () => {
    expect(HEADERS).toContain('Invoice No')
    expect(recognize(HEADERS).invoice_number).toBeNull()
  })

  it('leaves the ambiguous fields unplaced rather than guessing', () => {
    const rec = recognize(HEADERS)
    expect(rec.buyer_name).toBeNull()
    expect(rec.subtotal).toBeNull()
    expect(rec.line_description).toBeNull()
  })

  it('never places two fields on the same column', () => {
    const placed = Object.values(recognize(HEADERS)).filter(Boolean)
    expect(new Set(placed).size).toBe(placed.length)
  })

  it('places nothing when no header matches', () => {
    expect(Object.values(recognize(['a', 'b', 'c'])).every((v) => v === null)).toBe(true)
  })
})

describe('initMapping', () => {
  it('returns a key for every canonical field', () => {
    const map = initMapping('csv')
    expect(Object.keys(map).sort()).toEqual(CANON.map((c) => c.key).sort())
  })

  it('leaves exactly the four unrecognised fields to be placed by hand', () => {
    const unplaced = Object.keys(initMapping('csv')).filter((k) => !initMapping('csv')[k])
    expect(unplaced.sort()).toEqual(['buyer_name', 'invoice_number', 'line_description', 'subtotal'])
  })
})

describe('groupInvoices', () => {
  const mapped = { ...initMapping('csv'), invoice_number: 'Invoice No', buyer_name: 'Customer', subtotal: 'Net', line_description: 'Item' }

  it('returns nothing until invoice_number is mapped', () => {
    expect(groupInvoices('csv', initMapping('csv'))).toEqual([])
  })

  it('returns nothing for a null file or mapping', () => {
    expect(groupInvoices(null, mapped)).toEqual([])
    expect(groupInvoices('csv', null)).toEqual([])
  })

  it('groups the 9 line-item rows into 5 invoices', () => {
    const g = groupInvoices('csv', mapped)
    expect(g).toHaveLength(5)
    expect(g.map((x) => x.number)).toEqual(['INV-2041', 'INV-2042', 'INV-2043', 'INV-2044', 'INV-2045'])
    expect(g.reduce((s, x) => s + x.lineCount, 0)).toBe(FILE_DATA.csv.rows.length)
  })

  it('carries header values from the first row of each group', () => {
    const first = groupInvoices('csv', mapped)[0]
    expect(first).toMatchObject({ issueDate: '2026-06-03', buyer: 'Shoprite Nigeria', total: '1,058,875', lineCount: 2 })
  })

  it('quarantines only the invoice whose rows disagree on a header field', () => {
    const g = groupInvoices('csv', mapped)
    expect(g.filter((x) => x.quarantined).map((x) => x.number)).toEqual(['INV-2043'])
  })

  it('cites the disagreeing field, its spreadsheet rows, and both values', () => {
    const bad = groupInvoices('csv', mapped).find((x) => x.number === 'INV-2043')
    expect(bad?.conflicts).toEqual([{ field: 'issue_date', label: 'issue date', rows: [5, 6], values: ['2026-06-08', '2026-06-09'] }])
  })

  it('numbers sheet rows as the user sees them — 1-based, past the header row', () => {
    expect(groupInvoices('csv', mapped)[0].sheetRows).toEqual([2, 3])
  })

  it('does not flag a conflict on a field that is not mapped', () => {
    const withoutDate = { ...mapped, issue_date: null }
    expect(groupInvoices('csv', withoutDate).every((x) => !x.quarantined)).toBe(true)
  })

  it('treats a single-invoice file as one group, not a review case', () => {
    const oneOnly = { ...mapped, invoice_number: 'Currency' } // every row shares NGN
    expect(groupInvoices('csv', oneOnly)).toHaveLength(1)
  })
})
