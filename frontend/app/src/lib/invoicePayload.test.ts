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

import { buildInvoicePayload, emptyInvoiceForm, presetForm, type InvoiceFormState, type LineItemRow } from './invoicePayload'

// A fully-populated, rule-clean form — the literal source for B4-B8's per-field
// overrides. Deliberately independent of presetForm('clean') (which is stubbed to
// throw), matching the Clean preset's values so overriding a single field isolates
// exactly that field's assembly behavior.
function baseForm(overrides: Partial<InvoiceFormState> = {}): InvoiceFormState {
  const lineItems: LineItemRow[] = [{ id: 'li-1', description: 'Widget', quantity: '10', unitPrice: '100' }]
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
        line_items: [{ id: 'li-1', description: 'Widget', quantity: 10, unit_price: 100 }],
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
      { id: 'dup', description: 'A', quantity: 1, unit_price: -10 },
      { id: 'dup', description: 'B', quantity: 2, unit_price: 50 },
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
          { id: 'dup', description: 'A', quantity: 1, unit_price: -10 },
          { id: 'dup', description: 'B', quantity: 2, unit_price: 50 },
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
    const result = buildInvoicePayload(baseForm({ lineItems: [{ id: 'x', description: '', quantity: '', unitPrice: '' }] }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.line_items).toEqual([{ id: 'x' }])
  })

  it('B8: a row with a blank id drops the id key, keeping only description (so has(x.id) is false — never false-triggers the duplicate rule)', () => {
    const result = buildInvoicePayload(baseForm({ lineItems: [{ id: '', description: 'A', quantity: '', unitPrice: '' }] }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.line_items).toEqual([{ description: 'A' }])
  })
})

describe('buildInvoicePayload line-item quantity/unit_price (new numeric fields)', () => {
  it('numeric-string quantity/unitPrice assemble to JS numbers under keys quantity/unit_price', () => {
    const result = buildInvoicePayload(baseForm({ lineItems: [{ id: 'li-1', description: 'Widget', quantity: '10', unitPrice: '100' }] }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.line_items).toEqual([{ id: 'li-1', description: 'Widget', quantity: 10, unit_price: 100 }])
  })

  it('a negative unitPrice ("-10") is preserved as the JS number -10 (so line-cost-non-negative fires honestly)', () => {
    const result = buildInvoicePayload(baseForm({ lineItems: [{ id: 'a', description: 'A', quantity: '1', unitPrice: '-10' }] }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.line_items).toEqual([{ id: 'a', description: 'A', quantity: 1, unit_price: -10 }])
  })

  it('blank quantity/unitPrice are omitted; a non-numeric quantity assembles as the raw trimmed string', () => {
    const blank = buildInvoicePayload(baseForm({ lineItems: [{ id: 'a', description: 'A', quantity: '', unitPrice: '' }] })).invoice as Record<string, unknown>
    expect(blank.line_items).toEqual([{ id: 'a', description: 'A' }])

    const bad = buildInvoicePayload(baseForm({ lineItems: [{ id: 'a', description: 'A', quantity: 'two', unitPrice: '100' }] })).invoice as Record<string, unknown>
    expect(bad.line_items).toEqual([{ id: 'a', description: 'A', quantity: 'two', unit_price: 100 }])
  })
})

// --- QA (M3-09-02, Mode B) adversarial/edge coverage below. B1-B8 above are the
// architect's RED specs (authored pre-implementation); everything below closes gaps
// they leave: emptyInvoiceForm (AC-3, untested by B1-B8), numericField's falsy-0 /
// whitespace-trim / non-canonical-numeric edges, the fully-blank line-item row's
// documented (not accidental) shape, and presetForm's fresh-copy guarantee.

describe('emptyInvoiceForm (AC-3, QA adversarial — untested by B1-B8)', () => {
  it('returns all blank scalar fields and an empty line-items array', () => {
    const form = emptyInvoiceForm()

    expect(form).toEqual({
      supplierName: '',
      supplierTin: '',
      buyerTin: '',
      invoiceNumber: '',
      issueDate: '',
      currency: '',
      subtotal: '',
      vat: '',
      total: '',
      lineItems: [],
    })
  })

  it("assembles to {invoice:{}} — the same shape presetForm('minimal') produces", () => {
    const result = buildInvoicePayload(emptyInvoiceForm())

    expect(result).toEqual({ invoice: {} })
  })

  it('returns a fresh lineItems array each call (mutating one call does not leak into the next)', () => {
    const first = emptyInvoiceForm()
    first.lineItems.push({ id: 'y', description: 'x', quantity: '', unitPrice: '' })

    const second = emptyInvoiceForm()

    expect(second.lineItems).toEqual([])
  })
})

describe('buildInvoicePayload numeric edge cases (QA adversarial)', () => {
  it('a numeric-string "0" subtotal assembles as the JS number 0 — NOT omitted (0 is finite, must still fire subtotal-required/subtotal-non-negative honestly)', () => {
    const result = buildInvoicePayload(baseForm({ subtotal: '0' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice).toHaveProperty('subtotal')
    expect(invoice.subtotal).toBe(0)
    expect(typeof invoice.subtotal).toBe('number')
  })

  it('a whitespace-only supplierName ("   ") is omitted — proves trim(), not a bare `=== \'\'` check', () => {
    const result = buildInvoicePayload(baseForm({ supplierName: '   ' }))
    const invoice = result.invoice as Record<string, unknown>

    // supplierTin is still set (from baseForm), so `supplier` survives with only `tin` —
    // isolates that `name` specifically was trimmed-to-blank and dropped.
    expect(invoice.supplier).toEqual({ tin: '12345678-1234' })
    expect(invoice.supplier).not.toHaveProperty('name')
  })

  it('a numeric string with surrounding whitespace (" 1000 ") assembles as the trimmed JS number 1000', () => {
    const result = buildInvoicePayload(baseForm({ subtotal: ' 1000 ' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.subtotal).toBe(1000)
    expect(typeof invoice.subtotal).toBe('number')
  })

  it('scientific notation ("1e3") is finite, so it assembles as the JS number 1000 (documented "Number(t) when finite" behavior)', () => {
    const result = buildInvoicePayload(baseForm({ subtotal: '1e3' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.subtotal).toBe(1000)
    expect(typeof invoice.subtotal).toBe('number')
  })

  it('a non-numeric string with a thousands comma ("12,00") is NOT finite, so it assembles as the raw trimmed string (fires range/tax_math honestly, not "required")', () => {
    const result = buildInvoicePayload(baseForm({ subtotal: '12,00' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.subtotal).toBe('12,00')
    expect(typeof invoice.subtotal).toBe('string')
  })

  it('a negative numeric total ("-5"), isolated via baseForm (no other field changed), assembles as the JS number -5', () => {
    const result = buildInvoicePayload(baseForm({ total: '-5' }))
    const invoice = result.invoice as Record<string, unknown>

    expect(invoice.total).toBe(-5)
    expect(typeof invoice.total).toBe('number')
  })
})

describe('buildInvoicePayload fully-blank line-item row (QA adversarial)', () => {
  it('a row with BOTH description and id blank assembles to an empty object {} inside line_items — NOT dropped from the array', () => {
    const result = buildInvoicePayload(baseForm({ lineItems: [{ id: '', description: '', quantity: '', unitPrice: '' }] }))
    const invoice = result.invoice as Record<string, unknown>

    // Documented/intentional, not a latent bug: line-items-required only checks the
    // ARRAY is present (a non-empty array passes regardless of row content — System
    // Design rule #15), and no-duplicate-line-items only compares present `id` values
    // (System Design rule #17 + the blank-id-dropped rationale in B8) — an id-less row
    // can never collide. So {} is inert against both seeded rules; buildInvoicePayload
    // has no documented rule to drop a wholly-blank row from the array (only per-FIELD
    // dropping is specified), so this is the correct, spec-consistent shape.
    expect(invoice.line_items).toEqual([{}])
  })
})

describe("presetForm('clean') fresh-copy guarantee (QA adversarial)", () => {
  it('mutating the object returned by one call does not affect a subsequent call (scalar + nested lineItems row + array push)', () => {
    const first = presetForm('clean')
    first.supplierName = 'MUTATED'
    first.lineItems[0].description = 'MUTATED'
    first.lineItems.push({ id: 'extra', description: 'extra', quantity: '1', unitPrice: '1' })

    const second = presetForm('clean')

    expect(second.supplierName).toBe('Acme Ltd')
    expect(second.lineItems).toEqual([{ id: 'li-1', description: 'Widget', quantity: '10', unitPrice: '100' }])
  })
})
