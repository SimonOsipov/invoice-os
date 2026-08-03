// Source-document previewer data + pure logic layer (DOC-02-04). Every decision the
// modal makes lives here as a pure exported function, so it has an oracle without a DOM.
// STUB — bodies throw until the feat commit; sourceDocument.test.ts's RED specs pin the
// contract first.

import type { AuthedFetch } from './portfolio'

// internal/invoice/source_document.go:21-41. No omitempty: explicit null is the contract.
export interface SourceDocumentRecord {
  id: string
  filename: string | null
  declared_content_type: string | null
  size_bytes: number
  content_hash: string
  uploaded_at: string
  uploaded_by: string | null
  invoices_created: number
  other_invoice_rows: number[]
}

export interface SourceDocumentResponse {
  invoice_id: string
  source_rows: number[] | null // null = never recorded, distinct from []
  document: SourceDocumentRecord | null // null = manually created invoice
}

// internal/importer/handlers.go:543-552 (sheetResponse).
export interface DocumentSheet {
  format: string
  delimiter: string | null
  encoding: string | null
  columns: string[]
  rows: string[][]
  rows_total: number
  rows_returned: number
  truncated: boolean
}

export type DocumentKind = 'spreadsheet' | 'pdf' | 'image' | 'unrenderable'

export type SourceDocumentState = 'no-source' | 'loading' | 'spreadsheet' | 'pdf' | 'image' | 'unrenderable' | 'failed'

// Deliberately not AsyncState<T>: an empty sheet must stay distinguishable from a failure.
export type LoadStatus = 'idle' | 'loading' | 'ready' | 'error'

export interface SourceDocumentMeta {
  status: LoadStatus
  value: SourceDocumentResponse | null
}

// end is EXCLUSIVE (Array.slice).
export interface SheetWindow {
  start: number
  end: number
}

export interface NumberedRow {
  sheetRow: number
  cells: string[]
}

export interface DocumentBytes {
  url: string
  release: () => void
}

export async function getSourceDocument(
  _authedFetch: AuthedFetch,
  _base: string,
  _invoiceId: string,
): Promise<SourceDocumentResponse> {
  throw new Error('not implemented')
}

export async function getDocumentSheet(
  _authedFetch: AuthedFetch,
  _base: string,
  _documentId: string,
): Promise<DocumentSheet> {
  throw new Error('not implemented')
}

export async function fetchDocumentBytes(
  _getToken: () => string | null,
  _base: string,
  _documentId: string,
  _kind: DocumentKind,
  _filename: string | null,
): Promise<DocumentBytes> {
  throw new Error('not implemented')
}

export function classifyDocument(_filename: string | null, _declaredContentType: string | null): DocumentKind {
  throw new Error('not implemented')
}

export function sourceDocumentState(
  _meta: SourceDocumentMeta,
  _sheet: LoadStatus,
  _bytes: LoadStatus,
): SourceDocumentState {
  throw new Error('not implemented')
}

export function sheetWindow(_scrollTop: number, _measuredViewportH: number, _total: number): SheetWindow {
  throw new Error('not implemented')
}

export function numberSheetRows(_rows: string[][]): NumberedRow[] {
  throw new Error('not implemented')
}

export function contiguousRanges(_rows: number[] | null): Array<[number, number]> {
  throw new Error('not implemented')
}

export function rowsWithinSheet(_sourceRows: number[] | null, _rowsReturned: number): { present: number[]; missing: number[] } {
  throw new Error('not implemented')
}

// Toolbar mono short form, e.g. "ROWS 44–47" (en dash).
export function rangeLabel(_rows: number[] | null): string | null {
  throw new Error('not implemented')
}

export function describeSourceRows(_rows: number[] | null, _kind: DocumentKind): string {
  throw new Error('not implemented')
}

export function formatBytes(_n: number): string {
  throw new Error('not implemented')
}
