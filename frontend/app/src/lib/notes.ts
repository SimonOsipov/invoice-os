// Credit and debit notes: wire helpers, the form's checks and its live total.
import { ApiError } from '@invoice-os/api-client'

import type { DraftLine } from '../types'
import { fmt } from './format'
import { draftLine } from './clients'
import {
  addScaled,
  parseScaled,
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

// Quantity above 0, unit price 0 or more; a credit note is also capped at the original's value, by exact decimal.
export function noteLineError(
  type: NoteType,
  field: 'quantity' | 'unit_price',
  value: string,
  original: string | null,
): string | null {
  const entered = parseScaled(value)
  if (field === 'quantity') {
    if (entered === null || entered.u <= 0n) return NOTE_COPY.quantityPositive
  } else if (entered === null || entered.u < 0n) {
    return NOTE_COPY.unitPriceInvalid
  }
  if (type !== 'credit_note' || original === null) return null
  const cap = parseScaled(original)
  if (cap !== null && compare(entered, cap) > 0) {
    return field === 'quantity' ? NOTE_COPY.quantityOverOriginal : NOTE_COPY.unitPriceOverOriginal
  }
  return null
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

// Stand-in for CRDDBTNOTE-04's documentSign: -1 for a credit note, 1 otherwise.
export function noteSign(type: InvoiceDocumentType | null | undefined): 1 | -1 {
  return type === 'credit_note' ? -1 : 1
}

// Stand-in for CRDDBTNOTE-04's documentTypeBadge.
export function noteTypeBadge(
  type: InvoiceDocumentType | null | undefined,
): { label: string; title: string } | null {
  if (type === 'credit_note') return { label: 'CREDIT', title: 'Credit note' }
  if (type === 'debit_note') return { label: 'DEBIT', title: 'Debit note' }
  return null
}

// ASCII minus on a credit note, none on a debit note, a dash for no total.
export function signedNoteTotal(type: InvoiceDocumentType, total: string | null): string {
  if (total === null) return '—'
  return `${noteSign(type) === -1 ? '-' : ''}${fmt(Number(total))}`
}

export function isDuplicateNumberError(err: unknown): boolean {
  return err instanceof ApiError && err.status === 409 && err.message === DUPLICATE_NUMBER_MESSAGE
}
