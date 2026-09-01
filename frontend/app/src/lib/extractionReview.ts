// Extraction review data + pure logic layer (EXTR-11-04). Everything the review screen
// decides without a DOM lives here, so it has an oracle without one.

import type { CSSProperties } from 'react'

import { ApiError } from '@invoice-os/api-client'

import { formatLabel } from '../components/SourceDocumentStates'
import { fmtTimeWAT } from './format'
import type { AuthedFetch } from './portfolio'
import { EDIT_FIELD_LABELS } from './reviewBatch'
import { formatBytes, type DocumentBytes } from './sourceDocument'

// internal/extraction/reader.go, ExtractionRegion. Normalised [0,1], TOP-LEFT origin, page
// 1-based. A zero-area box (x0 === x1) is legal — the migration permits it.
export interface ExtractionRegion {
  page: number
  x0: number
  y0: number
  x1: number
  y1: number
}

// internal/extraction/reader.go, ExtractionPage. The STORED grid, never recomputed from a
// page size.
export interface ExtractionPage {
  page: number
  width_px: number
  height_px: number
}

// The four reason_code values extraction_field_results' CHECK admits, plus '' for a clean
// field. A union, not string: the wire cannot carry a fifth code.
export type ExtractionReason = '' | 'unreadable' | 'ambiguous' | 'inconsistent' | 'missing'

// internal/extraction/reader.go, ExtractionCandidate. One alternative reading; it carries no
// name, no reason and no alternatives of its own.
export interface ExtractionCandidate {
  value: string | null
  region: ExtractionRegion | null
}

// internal/extraction/reader.go, ExtractionCorrected. The human layer over one field: was is
// the reading the correction superseded and where is the anchor label it was taken from —
// both null when there is none.
export interface ExtractionCorrected {
  method: CorrectionMethod
  was: string | null
  where: string | null
}

// internal/extraction/reader.go, ExtractionFieldState. region is null when the extractor
// pointed at nothing. reason, alternatives and corrected are always present — Go has no
// omitempty here, so no key is optional; corrected is null on a field no human has touched.
export interface ExtractionFieldState {
  name: string
  value: string | null
  region: ExtractionRegion | null
  reason: ExtractionReason
  alternatives: ExtractionCandidate[]
  corrected: ExtractionCorrected | null
}

// internal/extraction/reader.go, ExtractionDocument. stored_at is RFC3339 text, not a time.
export interface ExtractionDocument {
  filename: string | null
  content_type: string | null
  size_bytes: number
  stored_at: string
}

// internal/extraction/reader.go, ExtractionDetail.
export interface ExtractionDetail {
  id: string
  document_id: string
  state: string
  document: ExtractionDocument
  pages: ExtractionPage[]
  fields: ExtractionFieldState[]
}

// internal/extraction/handlers_correction.go, CorrectionMethod. The four values the method
// CHECK admits.
export type CorrectionMethod = 'typed' | 'chosen' | 'pointed' | 'undone'

// internal/extraction/handlers_correction.go, CorrectionResponse -- the 201 body of the
// correction POST: what was appended plus the invoice it reached. A demotion is only visible on
// the next detail read.
export interface CorrectionResponse {
  id: string
  field_name: string
  value: string
  method: CorrectionMethod
  region: ExtractionRegion | null
  invoice_id: string
  created_at: string
}

// What the marker row over a settled field says: the method's uppercase label, and the
// provenance clause, which is null when the correction has none to state.
export interface CorrectedMarker {
  label: string
  was: string | null
}

// The extraction vocabulary is ten names wide, the edit form's nine: EDIT_FIELD_KEYS refuses
// invoice_number ([D9]), so the nine strings stay where they are and this overlay adds the
// tenth. Anything else -- document_text_layer, a reconciled line row -- renders its wire name.
const FIELD_LABELS: Record<string, string> = { ...EDIT_FIELD_LABELS, invoice_number: 'Invoice number' }

const REASON_PILLS: Record<Exclude<ExtractionReason, ''>, string> = {
  unreadable: "COULDN'T READ THIS CLEARLY",
  ambiguous: 'FOUND TWO POSSIBLE VALUES',
  inconsistent: "DOESN'T ADD UP",
  missing: 'NOT FOUND',
}

// Reconcile reuses one `inconsistent` code for the line-sum check and the entity match, so
// the note is what tells them apart.
const NOTE_SUBTOTAL = 'The line items we read do not add up to this subtotal.'
const NOTE_SUPPLIER =
  "This document's supplier doesn't match the client you picked. It is filed from your client record either way."
const NOTE_GENERIC = 'This value disagrees with the other numbers on the document.'

const SUPPLIER_MISMATCH_FIELDS = ['supplier_tin', 'supplier_name']

/** The curated label, or the raw wire name -- never a mechanical humanisation of it. */
export function fieldLabel(name: string): string {
  return FIELD_LABELS[name] ?? name
}

export function reasonPill(reason: ExtractionReason): string | null {
  return reason === '' ? null : REASON_PILLS[reason]
}

/** Keyed on the reason first: a clean subtotal carries no note. */
export function fieldNote(reason: ExtractionReason, name: string): string | null {
  if (reason !== 'inconsistent') return null
  if (name === 'subtotal') return NOTE_SUBTOTAL
  return SUPPLIER_MISMATCH_FIELDS.includes(name) ? NOTE_SUPPLIER : NOTE_GENERIC
}

/** The chip sub-label and the pointed was-line read the same phrase, so the two cannot drift. */
export function regionPhrase(region: ExtractionRegion | null): string | null {
  return region === null ? null : `page ${region.page}`
}

/**
 * What a settled field says instead of its reason. `was` is null where the correction has no
 * provenance to state -- omitting the clause invents no copy, where "We read null" would.
 * No default arm: a fifth CorrectionMethod must fail tsc here, not fall through silently.
 */
export function correctedMarker(
  corrected: ExtractionCorrected | null,
  region: ExtractionRegion | null,
): CorrectedMarker | null {
  if (corrected === null) return null
  switch (corrected.method) {
    case 'typed':
      return { label: 'YOU CHANGED THIS', was: corrected.was === null ? null : `We read ${corrected.was}` }
    case 'pointed': {
      // The anchor label when the correction carries one, else the region as a phrase --
      // regionPhrase, so the was-line and the chip sub-label cannot drift apart.
      const where = corrected.where ?? regionPhrase(region)
      return { label: 'YOU POINTED THIS OUT', was: where === null ? null : `Taken from ${where}` }
    }
    case 'chosen':
      return { label: 'YOU CHOSE THIS', was: 'We found more than one candidate' }
    case 'undone':
      // reader.go returns `corrected: null` for an undone field, so this never renders.
      return null
  }
}

export async function getExtractionDetail(authedFetch: AuthedFetch, base: string, jobId: string): Promise<ExtractionDetail> {
  return authedFetch<ExtractionDetail>(`${base}/api/submission/v1/extractions/${encodeURIComponent(jobId)}`)
}

// internal/extraction/handlers_correction.go, CorrectionRequest -- the POST body. Every key is
// always sent; region and anchor_label are null/'' for a correction nobody pointed at.
export interface CorrectionRequest {
  value: string
  method: CorrectionMethod
  region: ExtractionRegion | null
  anchor_label: string
}

// One POST the pane's Save owes, and the field it settles.
export interface CorrectionPost {
  field: string
  body: CorrectionRequest
}

// What one field carries in the shared draft. `chosen` also names the candidate, so the pane
// can move the highlight to that alternative's own box before anything is saved.
export interface DraftEntry {
  kind: CorrectionMethod
  value: string
  region: ExtractionRegion | null
}

export type DraftEntries = Record<string, DraftEntry>

// internal/extraction/vocabulary.go, HeaderFields -- the order one Save writes in, so the
// append-only table's seq follows the order the person reads.
export const HEADER_FIELDS: readonly string[] = [
  'invoice_number',
  'issue_date',
  'supplier_tin',
  'supplier_name',
  'buyer_tin',
  'buyer_name',
  'currency',
  'subtotal',
  'vat',
  'total',
]

/**
 * The pane's own view of the wire: the shared draft laid over it. A drafted field claims NO
 * correction -- nothing has been recorded, and the copy table has no pending string -- so the
 * affordance is the control itself. An undone entry resets the value to the reading the
 * correction superseded and drops the marker, the was-line and the Undo with it; where the
 * extractor read nothing that reset is the empty string, which is how an input says "no value"
 * -- and the column the server clears holds nothing either.
 */
export function applyDraft(fields: ExtractionFieldState[], entries: DraftEntries): ExtractionFieldState[] {
  return fields.map((f) => {
    const entry = entries[f.name]
    if (entry === undefined) return f
    if (entry.kind === 'undone') {
      return { ...f, value: f.corrected?.was ?? '', corrected: null }
    }
    // Only `chosen` moves the highlight: it names the alternative's own box, which is the
    // both-ways binding between the chip and the document.
    return { ...f, value: entry.value, region: entry.kind === 'chosen' ? entry.region : f.region }
  })
}

/** Where a field sits in the vocabulary; anything the wire adds later sorts after all of it. */
function vocabularyRank(name: string): number {
  const i = HEADER_FIELDS.indexOf(name)
  return i === -1 ? HEADER_FIELDS.length : i
}

/**
 * The draft turned into the POSTs one Save owes, in vocabulary order: each POST opens its own
 * transaction, so the append-only table's seq follows the order the person reads.
 *
 * An entry carrying the value the wire already holds is dropped -- diffEditInput's rule, and a
 * no-op recorded as a human decision is a lie the append-only table cannot take back. An UNDO
 * is exempt: it is a decision about the correction, not about the value, and the server ignores
 * the value it carries anyway.
 */
export function savableCorrections(fields: ExtractionFieldState[], entries: DraftEntries): CorrectionPost[] {
  const byName = new Map(fields.map((f) => [f.name, f]))
  return Object.keys(entries)
    .filter((name) => {
      const entry = entries[name]
      if (entry.kind === 'undone') return true
      // A trimmed-empty typed value is not sent: the boundary refuses a blank one, so the
      // round trip and its message buy nothing. fixEditPatch's rule (reviewBatch.ts).
      if (entry.kind === 'typed' && entry.value.trim() === '') return false
      return entry.value !== (byName.get(name)?.value ?? '')
    })
    .sort((a, b) => vocabularyRank(a) - vocabularyRank(b))
    .map((name) => ({
      field: name,
      // region stays null: only a pointed correction may carry one, and the server re-derives
      // a chosen candidate's box by matching its value.
      body: { value: entries[name].value, method: entries[name].kind, region: null, anchor_label: '' },
    }))
}

// authedFetch, never bare fetch: it is the seam that fires onUnauthorized/onSuspended.
export async function postFieldCorrection(
  authedFetch: AuthedFetch,
  base: string,
  jobId: string,
  field: string,
  body: CorrectionRequest,
): Promise<CorrectionResponse> {
  return authedFetch<CorrectionResponse>(
    `${base}/api/submission/v1/extractions/${encodeURIComponent(jobId)}/fields/${encodeURIComponent(field)}/corrections`,
    { method: 'POST', body },
  )
}

// Bare fetch, not authedFetch: the response is bytes and apiFetch always res.json()s. Auth is
// a bearer header, so a bare <img src> cannot authenticate — the caller owns release().
export async function fetchPageImage(
  getToken: () => string | null,
  base: string,
  jobId: string,
  page: number,
): Promise<DocumentBytes> {
  const res = await fetch(`${base}/api/submission/v1/extractions/${encodeURIComponent(jobId)}/pages/${page}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  // Before createObjectURL, never after: a refused page would pin a blob with no release().
  if (!res.ok) throw new ApiError('http', res.statusText, res.status)

  const url = URL.createObjectURL(new Blob([await res.arrayBuffer()], { type: 'image/png' }))
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

// (0.90 - 0.62) * 100 is 28.000000000000004 in doubles, and the deployed ratio oracle
// measures exactly that region.
function round4(n: number): number {
  return Number(n.toFixed(4))
}

// Percentages against the frame's padding box. No zoom argument: zoom moves the frame's
// width and every percentage follows.
export function highlightStyle(region: ExtractionRegion): CSSProperties {
  return {
    position: 'absolute',
    pointerEvents: 'none',
    left: `${round4(region.x0 * 100)}%`,
    top: `${round4(region.y0 * 100)}%`,
    width: `${round4((region.x1 - region.x0) * 100)}%`,
    height: `${round4((region.y1 - region.y0) * 100)}%`,
    background: 'oklch(72% .15 65 / .32)',
    // The ring paints outside the border box, so a zero-area region stays visible while
    // boundingBox() still measures exactly the region.
    boxShadow: '0 0 0 3px oklch(72% .15 65 / .32)',
    borderRadius: 3,
    transition: 'background 150ms ease-out, box-shadow 150ms ease-out',
  }
}

// The artboard's banded page card, aspect-locked to the STORED grid. padding is zero so the
// padding box and the content box coincide and no highlight can drift.
export function pageFrameStyle(page: ExtractionPage, zoom: number): CSSProperties {
  return {
    width: `${round4(zoom * 100)}%`,
    minWidth: `${round4(560 * zoom)}px`,
    maxWidth: `${round4(640 * zoom)}px`,
    aspectRatio: `${page.width_px} / ${page.height_px}`,
    margin: '0 auto 18px',
    position: 'relative',
    padding: 0,
    // The artboard's page card. Paper is white on both themes; System Design §3 tabulates
    // the rest of this card and omits only the background.
    background: '#fff',
    border: '1px solid var(--line-2)',
    boxShadow: '0 1px 3px oklch(20% .02 210 / .08)',
  }
}

export function docMetaLine(document: ExtractionDocument, pageCount: number): string {
  const pages = `${pageCount} ${pageCount === 1 ? 'PAGE' : 'PAGES'}`
  return [
    formatLabel(document.filename, document.content_type),
    pages,
    formatBytes(document.size_bytes),
    `STORED ${fmtTimeWAT(document.stored_at)} WAT`,
  ].join(' · ')
}

// Deferred, and resolved by attribute: the frames are a mapped list, so a per-item ref does
// not attach and the snip node exists only after the paint that created it.
export function scrollRegionIntoView(ground: HTMLElement | null, fieldName: string): void {
  if (!ground) return
  setTimeout(() => {
    const el = ground.querySelector(`[data-snip="${fieldName}"]`)
    if (!el) return
    const cr = ground.getBoundingClientRect()
    const er = el.getBoundingClientRect()
    const top = ground.scrollTop + (er.top - cr.top) - ground.clientHeight / 2 + er.height / 2
    ground.scrollTo({ top: Math.max(0, top), behavior: 'smooth' })
  }, 20)
}
