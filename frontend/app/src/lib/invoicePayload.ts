// Invoice form model, payload assembly & presets (M3-09-02). STUB — the executor
// implements the bodies next; every export below throws (or returns a wrong sentinel)
// so the RED specs in invoicePayload.test.ts (B1-B8) fail on a thrown/assertion
// mismatch, not an import or type error.
//
// buildInvoicePayload assembles the `{"invoice": {...}}` envelope the validation engine
// expects (Obsidian M3-09 story, System Design "Field <-> rule mapping" + "buildInvoicePayload
// rules"): numeric field -> ''->omit, else Number(t) when finite else the raw trimmed
// string (so non-numeric input honestly triggers range/tax_math, not a misleading
// "required"); string field -> ''->omit, else trimmed; `supplier`/`buyer` objects
// omitted when they'd be empty; `line_items` omitted when there are 0 rows; each
// line-item row -> an object of only its non-blank fields (a blank `id` is dropped so
// `has(x.id)` is false -> blank rows never false-trigger the duplicate rule).
//
// PRESETS/presetForm hold the three concrete example payloads (clean / has-violations /
// minimal) pinned in the System Design "The three preset payloads" subsection.
import type { InvoicePayload } from './validationApi'

export interface LineItemRow {
  description: string
  id: string
}

export interface InvoiceFormState {
  supplierName: string
  supplierTin: string
  buyerTin: string
  invoiceNumber: string
  issueDate: string
  currency: string
  subtotal: string
  vat: string
  total: string
  lineItems: LineItemRow[]
}

export type PresetKey = 'clean' | 'has-violations' | 'minimal'

export function emptyInvoiceForm(): InvoiceFormState {
  throw new Error('not implemented')
}

export function buildInvoicePayload(_form: InvoiceFormState): InvoicePayload {
  throw new Error('not implemented')
}

// Stubbed to a bad sentinel (not an empty object) so any test that reads a key off
// PRESETS directly (rather than through presetForm) fails loudly instead of silently
// resolving `undefined`.
export const PRESETS: Record<PresetKey, InvoiceFormState> = undefined as unknown as Record<
  PresetKey,
  InvoiceFormState
>

export function presetForm(_key: PresetKey): InvoiceFormState {
  throw new Error('not implemented')
}
