// Credit and debit notes: wire helpers, the form's checks and its live total.
import { ApiError } from '@invoice-os/api-client'

import type { DraftLine } from '../types'
import { fmt } from './format'
import { draftLine } from './clients'
import {
  addScaled,
  documentSign,
  parseScaled,
  renderScaled,
  type InvoiceDocumentType,
  type InvoiceLineItem,
  type InvoiceRecord,
  type Scaled,
} from './invoices'
import type { AuthedFetch } from './portfolio'

export type NoteType = Exclude<InvoiceDocumentType, 'commercial_invoice'>

// Mirrors internal/invoice/notes.go NoteSummary (money is a decimal string, total may be null).
export interface NoteSummary {
  id: string
  document_type: InvoiceDocumentType
  invoice_number: string
  status: InvoiceRecord['status']
  total: string | null
}

// Mirrors internal/invoice/notes.go NotesOnInvoice.
export interface NotesOnInvoice {
  notes: NoteSummary[]
  credited_so_far: string
  still_creditable: string | null
}

export interface NoteIssueInput {
  document_type: NoteType
  invoice_number: string
  issue_date: string
  lines: { line_id: string; quantity: string; unit_price: string }[]
}

export const NOTE_COPY = {
  // Same sentence as internal/invoice/handlers.go NumberTakenReason.
  numberTaken: 'This invoice number is already in the register for this company. Enter a different number.',
  quantityPositive: 'Enter a quantity above 0.',
  quantityFormat: 'Enter a quantity as plain digits: above 0, at most 3 decimals, below 100,000,000,000.',
  unitPriceFormat: 'Enter a unit price as plain digits: 0 or more, at most 2 decimals, below 1,000,000,000,000.',
  unitPriceInvalid: 'Enter a unit price of 0 or more.',
  quantityOverOriginal: 'A credit note cannot take more than the original quantity.',
  unitPriceOverOriginal: 'A credit note cannot take more than the original unit price.',
} as const

// The server's 409 sentence for a taken number (handlers.go statusForErr, ErrDuplicateNumber).
const DUPLICATE_NUMBER_MESSAGE = 'duplicate invoice number'

export function getInvoiceNotes(authedFetch: AuthedFetch, base: string, id: string): Promise<NotesOnInvoice> {
  return authedFetch<NotesOnInvoice>(`${base}/api/invoice/v1/invoices/${id}/notes`)
}

export function issueNote(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
  body: NoteIssueInput,
): Promise<InvoiceRecord> {
  return authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices/${id}/notes`, { method: 'POST', body })
}

export function isNote(inv: Pick<InvoiceRecord, 'document_type'>): boolean {
  return inv.document_type === 'credit_note' || inv.document_type === 'debit_note'
}

// n is every note on the original, both types, every status.
export function suggestedNoteNumber(type: NoteType, originalNumber: string, noteCount: number): string {
  return `${type === 'credit_note' ? 'CN' : 'DN'}-${originalNumber}-${noteCount + 1}`
}

function compare(a: Scaled, b: Scaled): number {
  const diff = addScaled(a, { u: -b.u, s: b.s }).u
  return diff > 0n ? 1 : diff < 0n ? -1 : 0
}

// Mirrors internal/invoice/note_amounts.go parsePlain, noteQuantity and noteUnitPrice (plain text, scale, bound).
const PLAIN_DECIMAL = /^-?[0-9]+(\.[0-9]+)?$/
const NOTE_LIMITS = {
  quantity: { scale: 3, bound: { u: 10n ** 11n, s: 0 } as Scaled },
  unit_price: { scale: 2, bound: { u: 10n ** 12n, s: 0 } as Scaled },
}

// Quantity above 0, unit price 0 or more; a credit note is also capped at the original's value, by exact decimal.
// Text that is not a plain decimal is refused before parseScaled, which would expand an exponent without bound.
export function noteLineError(
  type: NoteType,
  field: 'quantity' | 'unit_price',
  value: string,
  original: string | null,
): string | null {
  const format = field === 'quantity' ? NOTE_COPY.quantityFormat : NOTE_COPY.unitPriceFormat
  if (!PLAIN_DECIMAL.test(value)) return format
  // parseScaled refuses leading zeros; the server accepts them.
  const entered = parseScaled(value.replace(/^(-?)0+(?=[0-9])/, '$1'))
  if (entered === null) return format
  if (field === 'quantity') {
    if (entered.u <= 0n) return NOTE_COPY.quantityPositive
  } else if (entered.u < 0n) {
    return NOTE_COPY.unitPriceInvalid
  }
  const { scale, bound } = NOTE_LIMITS[field]
  let { u, s } = entered
  while (s > scale && u % 10n === 0n) {
    u /= 10n
    s -= 1
  }
  if (s > scale || compare(entered, bound) >= 0) return format
  if (type !== 'credit_note' || original === null) return null
  const cap = parseScaled(original)
  if (cap !== null && compare(entered, cap) > 0) {
    return field === 'quantity' ? NOTE_COPY.quantityOverOriginal : NOTE_COPY.unitPriceOverOriginal
  }
  return null
}

// Exact kobo-preserving naira for a limit: ₦1,000.50, -₦100.00; null is a dash.
export function fmtNairaExact(wire: string | null): string {
  const scaled = wire === null ? null : parseScaled(wire)
  if (scaled === null) return '—'
  const [whole, frac] = renderScaled(scaled).split('.')
  const neg = whole.startsWith('-')
  const digits = neg ? whole.slice(1) : whole
  return `${neg ? '-' : ''}₦${digits.replace(/\B(?=(\d{3})+$)/g, ',')}.${frac}`
}

// A NULL tax_percent becomes '', so draftTotals counts the line as categorised with no tax, as the server does.
export function noteDraftLines(
  picked: ReadonlyArray<{ line: InvoiceLineItem; quantity: string; unit_price: string }>,
): DraftLine[] {
  return picked.map(({ line, quantity, unit_price }) =>
    draftLine({
      desc: line.description ?? '',
      qty: Number(quantity),
      price: Number(unit_price),
      tax_category: line.tax_category ?? '',
      tax_percent: line.tax_percent ?? '',
    }),
  )
}

// ASCII minus on a credit note, none on a debit note, a dash for no total.
export function signedNoteTotal(type: InvoiceDocumentType, total: string | null): string {
  if (total === null) return '—'
  return `${documentSign({ document_type: type }) === -1 ? '-' : ''}${fmt(Number(total))}`
}

export function isDuplicateNumberError(err: unknown): boolean {
  return err instanceof ApiError && err.status === 409 && err.message === DUPLICATE_NUMBER_MESSAGE
}
