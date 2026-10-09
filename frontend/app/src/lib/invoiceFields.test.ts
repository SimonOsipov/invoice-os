import { describe, expect, it } from 'vitest'
import { CANON } from '../data'
import type { Draft } from '../types'
import { HEADER_FIELDS } from './extractionReview'
import { EDIT_FIELD_KEYS, type InvoiceEditInput } from './invoices'
import { labelOf, type ImportKey } from './invoiceFields'

describe('invoiceFields', () => {
  it('labelOf reads the label of a header and a line field', () => {
    expect(labelOf('invoice_number')).toBe('Invoice number')
    expect(labelOf('line_tax')).toBe('Line tax')
  })

  it('CANON is the eleven import fields, invoice_number alone required', () => {
    expect(CANON).toStrictEqual([
      { key: 'invoice_number', required: true },
      { key: 'issue_date' },
      { key: 'buyer_tin' },
      { key: 'buyer_name' },
      { key: 'currency' },
      { key: 'subtotal' },
      { key: 'vat' },
      { key: 'total' },
      { key: 'line_description' },
      { key: 'line_quantity' },
      { key: 'line_unit_price' },
    ])
  })

  it('CANON leaves out fields off the import path', () => {
    const keys: string[] = CANON.map((c) => c.key)
    for (const k of ['supplier_tin', 'supplier_name', 'line_total', 'line_tax', 'description']) {
      expect(keys).not.toContain(k)
    }
  })

  it('ImportKey rejects a field off the import path', () => {
    // @ts-expect-error supplier_tin is not an ImportKey
    const bad: Partial<Record<ImportKey, string[]>> = { supplier_tin: [] }
    expect(bad).toBeTruthy()
  })

  it('Draft is the five form keys plus items', () => {
    const ok: Draft = { number: '', buyer: '', buyerTin: '', date: '', currency: '', items: [] }
    // @ts-expect-error buyerTin missing
    const missing: Draft = { number: '', buyer: '', date: '', currency: '', items: [] }
    // @ts-expect-error supplier_tin is not a Draft key
    const extra: Draft = { number: '', buyer: '', buyerTin: '', date: '', currency: '', items: [], supplier_tin: '' }
    expect([ok, missing, extra]).toHaveLength(3)
  })

  it('EDIT_FIELD_KEYS is the nine editable header fields in order', () => {
    expect(EDIT_FIELD_KEYS).toStrictEqual([
      'issue_date',
      'supplier_tin',
      'supplier_name',
      'buyer_tin',
      'buyer_name',
      'currency',
      'subtotal',
      'vat',
      'total',
    ])
    expect(EDIT_FIELD_KEYS).not.toContain('invoice_number')
  })

  it('InvoiceEditInput takes the edit keys and the number, not a line key', () => {
    const ok: InvoiceEditInput = { invoice_number: 'X', supplier_tin: null }
    // @ts-expect-error line_total is a line key, not an invoice edit key
    const bad: InvoiceEditInput = { line_total: '1' }
    expect([ok, bad]).toHaveLength(2)
  })

  it('HEADER_FIELDS is the ten extraction header fields in order', () => {
    expect(HEADER_FIELDS).toStrictEqual([
      'invoice_number',
      'issue_date',
      'supplier_tin',
      'supplier_name',
      'buyer_tin',
      'buyer_name',
      'currency',
      'subtotal',
      'vat',
      'total',
    ])
  })
})
