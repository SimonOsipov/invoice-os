// RED specs (M3-09-02, B1-B8) — pin the invoice form model, payload assembly
// (numeric coercion, omit-empty, buyer/supplier/line_items omission, the `{invoice}`
// envelope) and the three concrete example presets before the executor implements the
// bodies in invoicePayload.ts.
//
// Every spec below currently fails because invoicePayload.ts's stub bodies throw `new
// Error('not implemented')` before returning anything — that IS the correct RED reason
// (assertion / not-implemented), not an import/compile/setup error. These are pure (no
// React, no fetch) functions — no vi.stubGlobal/mocking needed here, mirroring
// entityForm.test.ts. B4-B8 hand-construct `form` per their own Given columns (a
// `baseForm` literal, not `emptyInvoiceForm()`) so each test stays independent of
// emptyInvoiceForm's (also-stubbed) behavior — same convention as entityForm.test.ts's
// F9-F11.
//
// The Clean / Has-violations / Minimal JSON below is transcribed verbatim from the
// Obsidian M3-09 story, System Design § "The three preset payloads (concrete — the
// `Test-first` specs pin these)".
import { describe, expect, it } from 'vitest'

import { buildInvoicePayload, presetForm, type InvoiceFormState, type LineItemRow } from './invoicePayload'

// A fully-populated, rule-clean form — the literal source for B4-B8's per-field
// overrides. Deliberately independent of presetForm('clean') (which is stubbed to
// throw), matching the Clean preset's values so overriding a single field isolates
// exactly that field's assembly behavior.
function baseForm(overrides: Partial<InvoiceFormState> = {}): InvoiceFormState {
  const lineItems: LineItemRow[] = [{ description: 'Widget', id: 'li-1' }]
  return {
    supplierName: 'Acme Ltd',
    supplierTin: '12345678-1234',
    buyerTin: '87654321-4321',
    invoiceNumber: 'INV-001',
    issueDate: '2026-07-12',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    lineItems,
    ...overrides,
  }
}

describe('buildInvoicePayload presets (B1-B3)', () => {
  it('B1: the clean preset assembles to the exact Clean JSON (System Design)', () => {
    const result = buildInvoicePayload(presetForm('clean'))

    expect(result).toEqual({
      invoice: {
        supplier: { name: 'Acme Ltd', tin: '12345678-1234' },
        buyer: { tin: '87654321-4321' },
        invoice_number: 'INV-001',
        issue_date: '2026-07-12',
        currency: 'NGN',
        subtotal: 1000,
        vat: 75,
        total: 1075,
        line_items: [{ description: 'Widget', id: 'li-1' }],
      },
    })
  })

  it('B2: the has-violations preset assembles to the exact Has-violations JSON (System Design)', () => {
    const result = buildInvoicePayload(presetForm('has-violations'))
    const invoice = result.invoice as Record<string, unknown>

    // supplier has ONLY tin (no name key) — supplier-name-required must fire.
    expect(invoice.supplier).toEqual({ tin: 'BAD' })
    expect(invoice.supplier).not.toHaveProperty('name')
    // buyer omitted entirely (no buyerTin entered).
    expect(invoice).not.toHaveProperty('buyer')
    expect(invoice.currency).toBe('USD')
    expect(invoice.total).toBe(-5)
    expect(typeof invoice.total).toBe('number')
    expect(invoice.line_items).toEqual([
      { description: 'A', id: 'dup' },
      { description: 'B', id: 'dup' },
    ])

    expect(result).toEqual({
      invoice: {
        supplier: { tin: 'BAD' },
        invoice_number: 'INV-002',
        issue_date: '2026-07-12',
        currency: 'USD',
        subtotal: 1000,
        vat: 100,
        total: -5,
        line_items: [
          { description: 'A', id: 'dup' },
          { description: 'B', id: 'dup' },
        ],
      },
    })
  })

  it('B3: the minimal preset assembles to exactly {invoice:{}}', () => {
    const result = buildInvoicePayload(presetForm('minimal'))

    expect(result).toEqual({ invoice: {} })
  })
})

describe('buildInvoicePayload numeric coercion (B4)', () => {
  it('B4a: a numeric-string subtotal ("1000") assembles to the JSON number 1000', () => {
    const result = buildInvoicePayload(baseForm({ subtotal: '1000' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.subtotal).toBe(1000)
    expect(typeof invoice.subtotal).toBe('number')
  })

  it('B4b: a blank subtotal ("") omits the subtotal key entirely', () => {
    const result = buildInvoicePayload(baseForm({ subtotal: '' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice).not.toHaveProperty('subtotal')
  })

  it('B4c: a non-numeric subtotal ("abc") assembles as the raw trimmed string (honest range/tax_math trigger, not a misleading "required")', () => {
    const result = buildInvoicePayload(baseForm({ subtotal: 'abc' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.subtotal).toBe('abc')
    expect(typeof invoice.subtotal).toBe('string')
  })
})

describe('buildInvoicePayload optional-object omission (B5-B6)', () => {
  it('B5: a blank buyerTin omits the buyer key entirely', () => {
    const result = buildInvoicePayload(baseForm({ buyerTin: '' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice).not.toHaveProperty('buyer')
  })

  it('B6: zero line-item rows omits the line_items key entirely', () => {
    const result = buildInvoicePayload(baseForm({ lineItems: [] }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice).not.toHaveProperty('line_items')
  })
})

describe('buildInvoicePayload per-row blank-field dropping (B7-B8)', () => {
  it('B7: a row with a blank description drops the description key, keeping only id', () => {
    const result = buildInvoicePayload(baseForm({ lineItems: [{ description: '', id: 'x' }] }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.line_items).toEqual([{ id: 'x' }])
  })

  it('B8: a row with a blank id drops the id key, keeping only description (so has(x.id) is false — never false-triggers the duplicate rule)', () => {
    const result = buildInvoicePayload(baseForm({ lineItems: [{ description: 'A', id: '' }] }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.line_items).toEqual([{ description: 'A' }])
  })
})
