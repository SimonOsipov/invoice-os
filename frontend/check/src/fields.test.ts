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
  })
})
