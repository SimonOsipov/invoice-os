// Invoice form model, payload assembly & presets (M3-09-02).
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
// minimal) pinned in the System Design "The three preset payloads" subsection. presetForm
// returns a fresh copy (row objects included) each call so loading a preset into form
// state can never let a later edit mutate the shared PRESETS source.
import type { InvoicePayload } from './validationApi'

export interface LineItemRow {
  id: string
  description: string
  quantity: string
  unitPrice: string
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
  return {
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
  }
}

// ''->undefined (omit); else Number(t) when finite, else the raw trimmed string (so a
// non-numeric entry honestly fires the engine's range/tax_math rules instead of a
// misleading "required").
function numericField(value: string): number | string | undefined {
  const t = value.trim()
  if (!t) return undefined
  const n = Number(t)
  return Number.isFinite(n) ? n : t
}

// A row -> an object of only its non-blank fields. A blank `id` is dropped so
// `has(x.id)` is false downstream -- a blank row never false-triggers the duplicate
// rule. quantity/unit_price go through numericField (number when finite, else raw
// trimmed string) so they feed line-cost-non-negative / line-items-sum-subtotal
// honestly; blank ones are omitted.
function buildLineItem(row: LineItemRow): Record<string, unknown> {
  const item: Record<string, unknown> = {}
  const id = row.id.trim()
  const description = row.description.trim()
  if (id) item.id = id
  if (description) item.description = description
  const quantity = numericField(row.quantity)
  if (quantity !== undefined) item.quantity = quantity
  const unitPrice = numericField(row.unitPrice)
  if (unitPrice !== undefined) item.unit_price = unitPrice
  return item
}

export function buildInvoicePayload(form: InvoiceFormState): InvoicePayload {
  const invoice: Record<string, unknown> = {}

  const supplier: Record<string, string> = {}
  const supplierName = form.supplierName.trim()
  const supplierTin = form.supplierTin.trim()
  if (supplierName) supplier.name = supplierName
  if (supplierTin) supplier.tin = supplierTin
  if (Object.keys(supplier).length > 0) invoice.supplier = supplier

  const buyerTin = form.buyerTin.trim()
  if (buyerTin) invoice.buyer = { tin: buyerTin }

  const invoiceNumber = form.invoiceNumber.trim()
  if (invoiceNumber) invoice.invoice_number = invoiceNumber

  const issueDate = form.issueDate.trim()
  if (issueDate) invoice.issue_date = issueDate

  const currency = form.currency.trim()
  if (currency) invoice.currency = currency

  const subtotal = numericField(form.subtotal)
  if (subtotal !== undefined) invoice.subtotal = subtotal

  const vat = numericField(form.vat)
  if (vat !== undefined) invoice.vat = vat

  const total = numericField(form.total)
  if (total !== undefined) invoice.total = total

  if (form.lineItems.length > 0) invoice.line_items = form.lineItems.map(buildLineItem)

  return { invoice }
}

// The three concrete example payloads pinned in the System Design "The three preset
// payloads" subsection (see buildInvoicePayload presets B1-B3): clean assembles to a
// fully rule-clean invoice, has-violations to one that fires supplier-name-required /
// range / duplicate-line-item-id, minimal to `{invoice: {}}`.
export const PRESETS: Record<PresetKey, InvoiceFormState> = {
  clean: {
    supplierName: 'Acme Ltd',
    supplierTin: '12345678-1234',
    buyerTin: '87654321-4321',
    invoiceNumber: 'INV-001',
    issueDate: '2026-07-12',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    // 10 × ₦100 = ₦1,000 = subtotal (line-items-sum-subtotal passes); cost ≥ 0.
    lineItems: [{ id: 'li-1', description: 'Widget', quantity: '10', unitPrice: '100' }],
  },
  'has-violations': {
    supplierName: '',
    supplierTin: 'BAD',
    buyerTin: '',
    invoiceNumber: 'INV-002',
    issueDate: '2026-07-12',
    currency: 'USD',
    subtotal: '1000',
    vat: '100',
    total: '-5',
    // Duplicate id 'dup'; a negative cost (line-cost-non-negative fires); and the
    // amounts (1×-10 + 2×50 = 90) don't reconcile to subtotal 1000
    // (line-items-sum-subtotal fires).
    lineItems: [
      { id: 'dup', description: 'A', quantity: '1', unitPrice: '-10' },
      { id: 'dup', description: 'B', quantity: '2', unitPrice: '50' },
    ],
  },
  minimal: {
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
  },
}

export function presetForm(key: PresetKey): InvoiceFormState {
  const preset = PRESETS[key]
  return { ...preset, lineItems: preset.lineItems.map((row) => ({ ...row })) }
}
