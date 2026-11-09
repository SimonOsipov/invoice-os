import { describe, expect, it } from 'vitest'
import { INVOICE_FIELDS } from '../../app/src/lib/invoiceFields'
import { ACCEPT, CHECK_FIELD_KEYS, fieldLabel, IMPORT_KEYS, initialMapping, labelOf, type Suggestion } from './fields'

const suggestion = (mapping: Record<string, string>): Suggestion => ({
  columns: ['Inv', 'STIN', 'Net'],
  sample_rows: [],
  header_row: 1,
  rows_total: 0,
  mapping,
  source: 'ai',
})

describe('fields', () => {
  it('accept_isTheFourAppTypes', () => {
    expect(ACCEPT).toBe('.csv,.xlsx,.pdf,.docx')
  })

  it('checkFieldKeys_areTheImportKeysThenTheTwoSupplierKeys', () => {
    expect(CHECK_FIELD_KEYS).toEqual([...IMPORT_KEYS, 'supplier_name', 'supplier_tin'])
    expect(new Set(CHECK_FIELD_KEYS).size).toBe(CHECK_FIELD_KEYS.length)
    expect(labelOf('supplier_tin')).toBe('Supplier TIN')
  })

  it('checkFieldKeys_supplierKeysAreNotImportKeys', () => {
    for (const key of ['supplier_name', 'supplier_tin']) {
      expect(INVOICE_FIELDS.find((f) => f.key === key)!.importKey).toBeNull()
    }
  })

  it('initialMapping_keepsOnlyHeadersInTheFile', () => {
    const m = initialMapping(suggestion({ invoice_number: 'Inv', supplier_tin: 'STIN', total: 'Gone' }))
    expect(m.invoice_number).toBe('Inv')
    expect(m.supplier_tin).toBe('STIN')
    expect(m.total).toBeNull()
    expect(Object.keys(m)).toEqual([...CHECK_FIELD_KEYS])
    for (const k of CHECK_FIELD_KEYS.filter((k) => k !== 'invoice_number' && k !== 'supplier_tin')) expect(m[k]).toBeNull()
  })

  it('initialMapping_dropsAKeyOutsideTheCheckList', () => {
    expect('entity_id' in initialMapping(suggestion({ entity_id: 'Inv' }))).toBe(false)
  })

  it('fieldLabel_labelsEveryCheckFieldUniquely', () => {
    const labels = CHECK_FIELD_KEYS.map(fieldLabel)
    expect(labels.length).toBeGreaterThan(0)
    expect(new Set(labels).size).toBe(labels.length)
    for (const k of CHECK_FIELD_KEYS) expect(fieldLabel(k), k).not.toBe(k)
    expect(fieldLabel('supplier_tin')).toBe('Supplier TIN')
    expect(fieldLabel('not_a_field')).toBe('not_a_field')
  })

  it('fieldLabel_labelsEveryLineKeyOnceWithItsAppLabel', () => {
    const lineKeys = CHECK_FIELD_KEYS.filter((k) => k.startsWith('line_'))
    expect(lineKeys.length).toBeGreaterThan(0)
    const want: Record<string, string> = {
      line_description: 'Description',
      line_quantity: 'Quantity',
      line_unit_price: 'Unit price',
      line_total: 'Line total',
      line_tax: 'Line tax',
      line_tax_category: 'Tax category',
      line_hsn_code: 'HSN code',
      line_isic_code: 'ISIC code',
      line_product_category: 'Product category',
      line_service_category: 'Service category',
      line_sellers_item_identification: "Seller's item identification",
      line_price_unit: 'Price unit',
      line_tax_percent: 'Tax percent',
      line_base_quantity: 'Base quantity',
    }
    expect(lineKeys.sort()).toEqual(Object.keys(want).sort())
    for (const k of lineKeys) {
      expect(fieldLabel(k), k).toBe(want[k])
      expect(fieldLabel(k), k).not.toMatch(/\b(\w+)\s+\1\b/i)
    }
  })
})
