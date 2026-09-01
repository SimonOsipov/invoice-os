// Extraction review data + pure logic layer (EXTR-11-04). Everything the review screen
// decides without a DOM lives here, so it has an oracle without one.

import type { CSSProperties } from 'react'

import { ApiError } from '@invoice-os/api-client'

import { formatLabel } from '../components/SourceDocumentStates'
import { fmtTimeWAT } from './format'
import type { AuthedFetch } from './portfolio'
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

export async function getExtractionDetail(authedFetch: AuthedFetch, base: string, jobId: string): Promise<ExtractionDetail> {
  return authedFetch<ExtractionDetail>(`${base}/api/submission/v1/extractions/${encodeURIComponent(jobId)}`)
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
