// Source-document previewer data + pure logic layer (DOC-02-04). Every decision the
// modal makes lives here as a pure exported function, so it has an oracle without a DOM.

import { ApiError } from '@invoice-os/api-client'
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
  authedFetch: AuthedFetch,
  base: string,
  invoiceId: string,
): Promise<SourceDocumentResponse> {
  return authedFetch<SourceDocumentResponse>(
    `${base}/api/invoice/v1/invoices/${encodeURIComponent(invoiceId)}/source-document`,
  )
}

export async function getDocumentSheet(authedFetch: AuthedFetch, base: string, documentId: string): Promise<DocumentSheet> {
  return authedFetch<DocumentSheet>(`${base}/api/invoice/v1/documents/${encodeURIComponent(documentId)}/sheet`)
}

// Bare fetch, not authedFetch: the response is bytes and apiFetch always res.json()s.
// GET /v1/documents/{id} fixes Content-Type: application/octet-stream + nosniff, so the
// bytes must be re-typed client-side before any renderer can accept them.
export async function fetchDocumentBytes(
  getToken: () => string | null,
  base: string,
  documentId: string,
  kind: DocumentKind,
  filename: string | null,
): Promise<DocumentBytes> {
  const res = await fetch(`${base}/api/invoice/v1/documents/${encodeURIComponent(documentId)}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) throw new ApiError('http', res.statusText, res.status)

  const blob = new Blob([await res.arrayBuffer()], { type: mimeFor(kind, filename) })
  const url = URL.createObjectURL(blob)
  let released = false
  return {
    url,
    release: () => {
      if (released) return
      released = true
      URL.revokeObjectURL(url)
    },
  }
}

function mimeFor(kind: DocumentKind, filename: string | null): string {
  if (kind === 'pdf') return 'application/pdf'
  if (kind !== 'image') return 'application/octet-stream'
  switch (fileExtension(filename)) {
    case 'jpg':
    case 'jpeg':
      return 'image/jpeg'
    case 'webp':
      return 'image/webp'
    default:
      return 'image/png'
  }
}

const EXTENSION_KINDS: Record<string, DocumentKind | undefined> = {
  csv: 'spreadsheet',
  xlsx: 'spreadsheet',
  pdf: 'pdf',
  png: 'image',
  jpg: 'image',
  jpeg: 'image',
  webp: 'image',
}

// text/plain is load-bearing: detectFormat maps it to csv, so /sheet WILL 200 for such a
// file and calling it unrenderable would deny a sheet the server is serving.
const CONTENT_TYPE_KINDS: Record<string, DocumentKind | undefined> = {
  'text/csv': 'spreadsheet',
  'text/plain': 'spreadsheet',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 'spreadsheet',
  'application/pdf': 'pdf',
  'image/png': 'image',
  'image/jpeg': 'image',
  'image/webp': 'image',
}

function fileExtension(filename: string | null): string {
  const dot = filename ? filename.lastIndexOf('.') : -1
  return dot < 0 ? '' : (filename as string).slice(dot + 1).toLowerCase()
}

// Mirrors internal/importer/handlers.go:139-160 detectFormat: a RECOGNIZED extension is a
// verdict, an unrecognized one falls through to the content type (params stripped as
// mime.ParseMediaType does).
export function classifyDocument(filename: string | null, declaredContentType: string | null): DocumentKind {
  const byExtension = EXTENSION_KINDS[fileExtension(filename)]
  if (byExtension) return byExtension

  const mediaType = (declaredContentType ?? '').split(';')[0].trim().toLowerCase()
  return CONTENT_TYPE_KINDS[mediaType] ?? 'unrenderable'
}

export function sourceDocumentState(meta: SourceDocumentMeta, sheet: LoadStatus, bytes: LoadStatus): SourceDocumentState {
  if (meta.status === 'error') return 'failed'
  if (meta.status !== 'ready' || meta.value === null) return 'loading'

  const record = meta.value.document
  if (record === null) return 'no-source'

  const kind = classifyDocument(record.filename, record.declared_content_type)
  if (kind === 'unrenderable') return 'unrenderable' // nothing is fetched for it

  const channel = kind === 'spreadsheet' ? sheet : bytes
  if (channel === 'error') return 'failed'
  if (channel !== 'ready') return 'loading'
  return kind
}

const ROW_HEIGHT = 30
const OVERSCAN_ABOVE = 8
const OVERSCAN_BELOW = 22
const VIEWPORT_MIN = 240
const VIEWPORT_MAX = 1200

// end builds off `first`, not the clamped `start` — the design's `to +ceil(viewportH/30)+22`
// continues from the same base, and the spacer heights depend on it. The non-finite guard
// matters because an unmounted ref.current?.clientHeight is undefined -> NaN, and
// min(1200, max(240, NaN)) is NaN.
export function sheetWindow(scrollTop: number, measuredViewportH: number, total: number): SheetWindow {
  const h = Number.isFinite(measuredViewportH)
    ? Math.min(VIEWPORT_MAX, Math.max(VIEWPORT_MIN, measuredViewportH))
    : VIEWPORT_MIN
  const first = Math.floor(scrollTop / ROW_HEIGHT)
  const start = Math.max(0, Math.min(first - OVERSCAN_ABOVE, total))
  const end = Math.max(start, Math.min(first + Math.ceil(h / ROW_HEIGHT) + OVERSCAN_BELOW, total))
  return { start, end }
}

// Row 1 is the header; sheetRow(i) = i + 2, mirroring internal/importer/service.go:270.
const FIRST_DATA_SHEET_ROW = 2

// The number is bound BEFORE any filtering, which is what makes it structural: consumers
// filter and slice NumberedRow[], so no view mode can renumber.
export function numberSheetRows(rows: string[][]): NumberedRow[] {
  return rows.map((cells, i) => ({ sheetRow: i + FIRST_DATA_SHEET_ROW, cells }))
}

// source_rows is neither sorted nor deduped by the CHECK constraint ({7,3} and {3,3} are legal).
function sortedUnique(rows: number[] | null): number[] {
  if (!rows) return []
  return Array.from(new Set(rows)).sort((a, b) => a - b)
}

export function contiguousRanges(rows: number[] | null): Array<[number, number]> {
  const ranges: Array<[number, number]> = []
  for (const n of sortedUnique(rows)) {
    const last = ranges[ranges.length - 1]
    if (last && n === last[1] + 1) last[1] = n
    else ranges.push([n, n])
  }
  return ranges
}

// The sheet endpoint returns the first `rowsReturned` data rows in decode order, i.e. sheet
// rows 2 … rowsReturned+1. Rows past that are stored but off-screen, and the surface must
// say so instead of silently omitting them.
export function rowsWithinSheet(
  sourceRows: number[] | null,
  rowsReturned: number,
): { present: number[]; missing: number[] } {
  const present: number[] = []
  const missing: number[] = []
  for (const n of sortedUnique(sourceRows)) {
    if (n >= FIRST_DATA_SHEET_ROW && n <= rowsReturned + 1) present.push(n)
    else missing.push(n)
  }
  return { present, missing }
}

function joinRanges(ranges: Array<[number, number]>, conjunction: string): string {
  const parts = ranges.map(([from, to]) => (from === to ? String(from) : `${from}–${to}`))
  if (parts.length <= 1) return parts.join('')
  return `${parts.slice(0, -1).join(', ')}${conjunction}${parts[parts.length - 1]}`
}

function isSingleRow(ranges: Array<[number, number]>): boolean {
  return ranges.length === 1 && ranges[0][0] === ranges[0][1]
}

// Toolbar mono short form, e.g. "ROWS 44–47" (en dash).
export function rangeLabel(rows: number[] | null): string | null {
  const ranges = contiguousRanges(rows)
  if (ranges.length === 0) return null
  return `${isSingleRow(ranges) ? 'ROW' : 'ROWS'} ${joinRanges(ranges, ' AND ')}`
}

// Kind decides before rows. A null/[] source_rows renders the not-recorded sentence, never
// a guessed range.
export function describeSourceRows(rows: number[] | null, kind: DocumentKind): string {
  if (kind === 'image') return 'This photograph became this invoice.'
  if (kind === 'unrenderable') return 'Stored, but the previewer cannot render this format.'
  if (kind === 'pdf') return 'The page of this document that became this invoice was not recorded.'

  const ranges = contiguousRanges(rows)
  if (ranges.length === 0) return 'The rows of this file that became this invoice were not recorded.'
  return `${isSingleRow(ranges) ? 'Row' : 'Rows'} ${joinRanges(ranges, ' and ')} of this file became this invoice.`
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB']

// 1024-base: the design's "610 KB" is 624640/1024 exactly. Rounding happens before the
// integer check so 1048575 reads "1 MB", not "1.0 MB".
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '0 B'
  if (n < 1024) return `${n} B`

  let value = n / 1024
  let unit = 1
  while (unit < BYTE_UNITS.length - 1 && Math.round(value * 10) / 10 >= 1024) {
    value /= 1024
    unit += 1
  }
  const rounded = Math.round(value * 10) / 10
  return `${Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1)} ${BYTE_UNITS[unit]}`
}
