// lineItems.ts (EXTR-13-05, Mode A): the browser's pure line-item layer -- parse, group,
// recompute, flag, sum, remap. No React, no DOM, no network call, nothing persisted (EXTR-14
// owns learned mappings). extractionReview.ts's own posture: everything decidable without a
// DOM lives here, so it has an oracle without one.
//
// RED-first stub (Stage 2.5): every function throws before computing a result, and
// LINE_TOLERANCE is deliberately wrong. Stage 3 replaces both. A throw here is the same
// valid RED reason invoiceDraft.test.ts's stub already established for this repo.

import type { ExtractionFieldState, ExtractionRegion, ExtractionReason } from './extractionReview'

export type LineRole = 'description' | 'quantity' | 'unit_price' | 'line_total'

// Mirrors extraction.LineRoles order (lineitems.go).
export const LINE_ROLES: readonly LineRole[] = ['description', 'quantity', 'unit_price', 'line_total']

// Deliberately wrong -- reconcile.go's reconcileTolerance is "0.01". Spec 18 pins this once
// Stage 3 lands the real value.
export const LINE_TOLERANCE = '0'

export interface LineCell {
  name: string | null
  value: string
  region: ExtractionRegion | null
  reason: ExtractionReason
}

export interface LineRow {
  key: string
  cells: Record<LineRole, LineCell>
}

export type RowArithmetic = 'ok' | 'flagged' | 'unchecked'

export interface LineSumState {
  sum: string
  printed: string | null
  agrees: boolean | null
}

export interface LineItemInput {
  description: string | null
  quantity: string | null
  unit_price: string | null
  line_total: string | null
}

function notImplemented(name: string): never {
  throw new Error(`${name} not implemented`)
}

export function lineFieldName(_index: number, _role: LineRole): string {
  return notImplemented('lineFieldName')
}

export function parseLineFieldName(_name: string): { index: number; role: LineRole } | null {
  return notImplemented('parseLineFieldName')
}

export function linesFromFields(_fields: readonly ExtractionFieldState[]): LineRow[] {
  return notImplemented('linesFromFields')
}

export function rowArithmetic(_row: LineRow): RowArithmetic {
  return notImplemented('rowArithmetic')
}

export function lineSumState(_rows: readonly LineRow[], _printedSubtotal: string | null): LineSumState | null {
  return notImplemented('lineSumState')
}

export function remapRoles(_rows: readonly LineRow[], _from: LineRole, _to: LineRole): LineRow[] {
  return notImplemented('remapRoles')
}

export function addRow(_rows: readonly LineRow[]): LineRow[] {
  return notImplemented('addRow')
}

export function removeRow(_rows: readonly LineRow[], _at: number): LineRow[] {
  return notImplemented('removeRow')
}

export function linesToPost(_rows: readonly LineRow[]): LineItemInput[] {
  return notImplemented('linesToPost')
}

export function lineSetChanged(_wireRows: readonly LineRow[], _draftRows: readonly LineRow[]): boolean {
  return notImplemented('lineSetChanged')
}
