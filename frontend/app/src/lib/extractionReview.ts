// Extraction review data + pure logic layer (EXTR-11-04). Everything the review screen
// decides without a DOM lives here, so it has an oracle without one.
// STUB — bodies throw until the feat commit; extractionReview.test.ts's RED specs pin the
// contract first.

import type { CSSProperties } from 'react'

import type { AuthedFetch } from './portfolio'
import type { DocumentBytes } from './sourceDocument'

// internal/extraction/reader.go:100-106. Normalised [0,1], TOP-LEFT origin, page 1-based.
// A zero-area box (x0 === x1) is legal — the migration permits it.
export interface ExtractionRegion {
  page: number
  x0: number
  y0: number
  x1: number
  y1: number
}

// internal/extraction/reader.go:110-114. The STORED grid, never recomputed from a page size.
export interface ExtractionPage {
  page: number
  width_px: number
  height_px: number
}

// internal/extraction/reader.go:118-122. region is null when the extractor pointed at nothing.
export interface ExtractionFieldState {
  name: string
  value: string | null
  region: ExtractionRegion | null
}

// internal/extraction/reader.go:127-132. stored_at is RFC3339 text, not a time.
export interface ExtractionDocument {
  filename: string | null
  content_type: string | null
  size_bytes: number
  stored_at: string
}

// internal/extraction/reader.go:137-144.
export interface ExtractionDetail {
  id: string
  document_id: string
  state: string
  document: ExtractionDocument
  pages: ExtractionPage[]
  fields: ExtractionFieldState[]
}

export async function getExtractionDetail(_authedFetch: AuthedFetch, _base: string, _jobId: string): Promise<ExtractionDetail> {
  throw new Error('not implemented')
}

// Bare fetch, not authedFetch: the response is bytes and apiFetch always res.json()s. Auth is
// a bearer header, so a bare <img src> cannot authenticate — the caller owns release().
export async function fetchPageImage(
  _getToken: () => string | null,
  _base: string,
  _jobId: string,
  _page: number,
): Promise<DocumentBytes> {
  throw new Error('not implemented')
}

export function highlightStyle(_region: ExtractionRegion): CSSProperties {
  throw new Error('not implemented')
}

export function pageFrameStyle(_page: ExtractionPage, _zoom: number): CSSProperties {
  throw new Error('not implemented')
}

export function docMetaLine(_document: ExtractionDocument, _pageCount: number): string {
  throw new Error('not implemented')
}

export function scrollRegionIntoView(_ground: HTMLElement | null, _fieldName: string): void {
  throw new Error('not implemented')
}
