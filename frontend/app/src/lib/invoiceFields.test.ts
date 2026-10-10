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

  it('CANON is the import fields, invoice_number alone required', () => {
    const keys = [
      'invoice_number', 'issue_date', 'buyer_tin', 'buyer_name', 'currency', 'subtotal', 'vat', 'total',
      'line_description', 'line_quantity', 'line_unit_price',
      'invoice_kind', 'tax_currency_code', 'due_date', 'issue_time', 'tax_point_date', 'payment_status',
      'buyer_email', 'buyer_telephone', 'buyer_street', 'buyer_city', 'buyer_postal_zone', 'buyer_country', 'buyer_state', 'buyer_lga',
      'line_total', 'line_tax',
      'line_tax_category', 'line_hsn_code', 'line_isic_code', 'line_product_category', 'line_service_category',
      'line_sellers_item_identification', 'line_price_unit', 'line_tax_percent', 'line_base_quantity',
    ]
    expect(CANON).toStrictEqual(keys.map((key) => (key === 'invoice_number' ? { key, required: true } : { key })))
  })

  it('CANON leaves out fields off the import path', () => {
    const keys: string[] = CANON.map((c) => c.key)
    for (const k of ['supplier_tin', 'supplier_name', 'supplier_email', 'description', 'line_line_total']) {
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
