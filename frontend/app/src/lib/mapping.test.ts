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
import { canSubmitMapping, groupInvoices, initMapping, initMappingFromHeaders, recognize, toImportMapping } from './mapping'
import type { Mapping } from '../types'

// Inlined per M4-08-03 [test-fixture-inlined]: verbatim copy of FILE_DATA.csv.headers
// (data.tsx:192) so this file's HEADERS no longer depends on the fixture M4-08-06
// deletes. The FILE_DATA import above STAYS — the groupInvoices block below still
// reads FILE_DATA.csv.rows.length until M4-08-06 removes that block.
const HEADERS = ['Invoice No', 'Issue Date', 'Buyer TIN', 'Customer', 'Currency', 'Net', 'VAT', 'Total', 'Item', 'Qty', 'Unit Price']

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

describe('initMappingFromHeaders', () => {
  // MAP-09 (AC5): ported from the old initMapping('csv') cases — same two
  // assertions, header array instead of a fileId, no FILE_DATA dereference.
  it('returns a key for every canonical field', () => {
    const map = initMappingFromHeaders(HEADERS)
    expect(Object.keys(map).sort()).toEqual(CANON.map((c) => c.key).sort())
  })

  it('leaves exactly the four unrecognised fields to be placed by hand', () => {
    const unplaced = Object.keys(initMappingFromHeaders(HEADERS)).filter((k) => !initMappingFromHeaders(HEADERS)[k])
    expect(unplaced.sort()).toEqual(['buyer_name', 'invoice_number', 'line_description', 'subtotal'])
  })

  // MAP-01
  it('auto-places the seven alias fields on their exact header strings', () => {
    expect(initMappingFromHeaders(HEADERS)).toMatchObject({
      issue_date: 'Issue Date',
      buyer_tin: 'Buyer TIN',
      currency: 'Currency',
      vat: 'VAT',
      total: 'Total',
      line_quantity: 'Qty',
      line_unit_price: 'Unit Price',
    })
  })

  // MAP-02: invoice_number is never auto-placed, even though 'Invoice No' is present.
  it('never guesses invoice_number, even though a plausible column exists', () => {
    expect(HEADERS).toContain('Invoice No')
    expect(initMappingFromHeaders(HEADERS).invoice_number).toBeNull()
  })

  // MAP-03: an empty header array still returns all 11 CANON keys, all null.
  it('returns exactly the 11 CANON keys, every value null, for an empty header array', () => {
    const map = initMappingFromHeaders([])
    expect(Object.keys(map).sort()).toEqual(CANON.map((c) => c.key).sort())
    expect(Object.values(map).every((v) => v === null)).toBe(true)
  })

  // MAP-11: a blank header can never auto-place (norm('') matches no ALIAS entry),
  // and must not corrupt placement of a real header alongside it.
  it('never places a field on a blank header', () => {
    const map = initMappingFromHeaders(['', 'Total', ''])
    expect(Object.keys(map).sort()).toEqual(CANON.map((c) => c.key).sort())
    expect(map.total).toBe('Total')
    expect(Object.values(map)).not.toContain('')
  })

  // MAP-12: duplicate headers resolve to the first occurrence, matching the
  // server's first-match resolveMapping behaviour; still exactly 11 keys back.
  it('resolves duplicate headers to the first occurrence', () => {
    const map = initMappingFromHeaders(['VAT', 'VAT', 'Total'])
    expect(map.vat).toBe('VAT')
    expect(map.total).toBe('Total')
    expect(Object.keys(map).sort()).toEqual(CANON.map((c) => c.key).sort())
  })
})

describe('toImportMapping', () => {
  // MAP-04: only placed fields survive, asserted by value AND key count so an
  // undefined-valued key (hidden by JSON.stringify/toEqual on its own) can't pass.
  it('emits only placed fields', () => {
    const result = toImportMapping({ invoice_number: 'A', total: 'B', buyer_name: null, vat: null })
    expect(result).toEqual({ invoice_number: 'A', total: 'B' })
    expect(Object.keys(result)).toHaveLength(2)
  })

  // MAP-05: the exact map[string]string coercion trap — Go unmarshals JSON null
  // into a string map as "", so the literal substring must never appear.
  it('never serializes to JSON containing the substring null', () => {
    const result = toImportMapping({ invoice_number: 'A', total: null, buyer_name: null })
    expect(JSON.stringify(result)).not.toContain('null')
  })

  // MAP-06: the load-bearing spec. A `v !== null` impl passes MAP-04 and MAP-05
  // but ships '', which can silently match a blank-named column (§B, PRV-15).
  it('drops empty-string values, not just null', () => {
    const result = toImportMapping({ invoice_number: 'A', buyer_name: '' })
    expect(result).toEqual({ invoice_number: 'A' })
  })

  // MAP-07: a fully-placed mapping round-trips unchanged, and the input is not mutated.
  it('passes through a fully-placed mapping unchanged, without mutating the input', () => {
    const full: Mapping = {
      invoice_number: 'Invoice No',
      issue_date: 'Issue Date',
      buyer_tin: 'Buyer TIN',
      buyer_name: 'Customer',
      currency: 'Currency',
      subtotal: 'Net',
      vat: 'VAT',
      total: 'Total',
      line_description: 'Item',
      line_quantity: 'Qty',
      line_unit_price: 'Unit Price',
    }
    const snapshot = { ...full }
    const result = toImportMapping(full)
    expect(Object.keys(result).sort()).toEqual(CANON.map((c) => c.key).sort())
    expect(result).toEqual(full)
    expect(full).toEqual(snapshot)
  })

  // MAP-13: two fields sharing one header value both survive — no dedupe-by-value.
  it('keeps two keys that share the same header value', () => {
    const result = toImportMapping({ invoice_number: 'A', subtotal: 'Amount', total: 'Amount' })
    expect(result).toEqual({ invoice_number: 'A', subtotal: 'Amount', total: 'Amount' })
  })

  // MAP-14: no CANON allow-list — an unrecognized key survives so the server's
  // deliberate unknown-key 400 (service.go:146-153) still fires, loudly, server-side.
  it('does not filter unrecognized keys', () => {
    const result = toImportMapping({ invoice_number: 'A', totla: 'B' } as Mapping)
    expect(result).toEqual({ invoice_number: 'A', totla: 'B' })
  })
})

describe('canSubmitMapping', () => {
  // MAP-08
  it('gates on invoice_number alone', () => {
    expect(canSubmitMapping(null)).toBe(false)

    const allButInvoiceNumber: Mapping = {
      invoice_number: null,
      issue_date: 'Issue Date',
      buyer_tin: 'Buyer TIN',
      buyer_name: 'Customer',
      currency: 'Currency',
      subtotal: 'Net',
      vat: 'VAT',
      total: 'Total',
      line_description: 'Item',
      line_quantity: 'Qty',
      line_unit_price: 'Unit Price',
    }
    expect(canSubmitMapping(allButInvoiceNumber)).toBe(false)

    const onlyInvoiceNumber: Mapping = {
      invoice_number: 'Invoice No',
      issue_date: null,
      buyer_tin: null,
      buyer_name: null,
      currency: null,
      subtotal: null,
      vat: null,
      total: null,
      line_description: null,
      line_quantity: null,
      line_unit_price: null,
    }
    expect(canSubmitMapping(onlyInvoiceNumber)).toBe(true)
  })

  // MAP-10: duplicate-target mappings are legal server-side; canSubmitMapping must
  // not add a uniqueness rule stricter than resolveMapping (Core AC3).
  it('allows three fields pointing at the same header', () => {
    expect(canSubmitMapping({ invoice_number: 'Amount', total: 'Amount', subtotal: 'Amount' })).toBe(true)
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
