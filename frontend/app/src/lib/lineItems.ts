// The browser's pure line-item layer -- parse, group, recompute, flag, sum, remap. No React,
// no DOM, no network call, nothing persisted (EXTR-14 owns learned mappings). Everything the
// grid decides without a DOM lives here, so it has an oracle without one.

import type { ExtractionFieldState, ExtractionRegion, ExtractionReason } from './extractionReview'
import { addScaled, mulScaled, parseScaled, renderScaled, type Scaled } from './invoices'

export type LineRole = 'description' | 'quantity' | 'unit_price' | 'line_total'

// Mirrors extraction.LineRoles order (lineitems.go).
export const LINE_ROLES: readonly LineRole[] = ['description', 'quantity', 'unit_price', 'line_total']

// reconcile.go's reconcileTolerance, pinned to the Go literal by a source-reading spec.
export const LINE_TOLERANCE = '0.01'

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

// internal/extraction/handlers_lineitems.go, LineItemInput -- the POST body's element and the
// 201 body's. wireMirrors.test.ts pins the key set against Go and e2e/api/client.ts.
export interface LineItemInput {
  description: string | null
  quantity: string | null
  unit_price: string | null
  line_total: string | null
}

// -- exact-decimal helpers over invoices.ts's Scaled kit -----------------------------------

function subScaled(a: Scaled, b: Scaled): Scaled {
  return addScaled(a, { u: -b.u, s: b.s })
}

function cmpScaled(a: Scaled, b: Scaled): -1 | 0 | 1 {
  const s = Math.max(a.s, b.s)
  const au = a.u * 10n ** BigInt(s - a.s)
  const bu = b.u * 10n ** BigInt(s - b.s)
  if (au < bu) return -1
  return au > bu ? 1 : 0
}

function absScaled(a: Scaled): Scaled {
  return a.u < 0n ? { u: -a.u, s: a.s } : a
}

// Strictly greater, mirroring exceedsTolerance's GreaterThan: exactly 0.01 does not flag.
function exceedsTolerance(diff: Scaled): boolean {
  const tolerance = parseScaled(LINE_TOLERANCE)
  if (tolerance === null) return false
  return cmpScaled(absScaled(diff), tolerance) > 0
}

// -- field names ----------------------------------------------------------------------------

const LINE_FIELD_RE = /^line_items\[([1-9][0-9]*)\]\.(description|quantity|unit_price|line_total)$/

export function lineFieldName(index: number, role: LineRole): string {
  return `line_items[${index}].${role}`
}

export function parseLineFieldName(name: string): { index: number; role: LineRole } | null {
  const m = LINE_FIELD_RE.exec(name)
  if (m === null) return null
  return { index: Number(m[1]), role: m[2] as LineRole }
}

// -- grouping -------------------------------------------------------------------------------

function blankCell(name: string | null): LineCell {
  return { name, value: '', region: null, reason: '' }
}

// The wire arrives lexicographic (reader.go's ORDER BY ties on created_at), so
// line_items[10].* precedes line_items[2].*. Sort by the parsed numeric index; a hole in the
// indices closes in ordinal terms only -- a cell's own name is never renumbered.
export function linesFromFields(fields: readonly ExtractionFieldState[]): LineRow[] {
  const byIndex = new Map<number, LineRow>()
  for (const field of fields) {
    const parsed = parseLineFieldName(field.name)
    if (parsed === null) continue
    let row = byIndex.get(parsed.index)
    if (row === undefined) {
      const index = parsed.index
      row = {
        key: `i${index}`,
        cells: {
          description: blankCell(lineFieldName(index, 'description')),
          quantity: blankCell(lineFieldName(index, 'quantity')),
          unit_price: blankCell(lineFieldName(index, 'unit_price')),
          line_total: blankCell(lineFieldName(index, 'line_total')),
        },
      }
      byIndex.set(index, row)
    }
    row.cells[parsed.role] = {
      name: field.name,
      value: field.value ?? '',
      region: field.region,
      reason: field.reason,
    }
  }
  return [...byIndex.keys()].sort((a, b) => a - b).map((index) => byIndex.get(index) as LineRow)
}

// -- arithmetic -----------------------------------------------------------------------------

// reconcileLines' rule: all three cells must be present and parseable, else the check does not
// run. An unparseable value is never folded to zero.
export function rowArithmetic(row: LineRow): RowArithmetic {
  const quantity = parseScaled(row.cells.quantity.value)
  const unitPrice = parseScaled(row.cells.unit_price.value)
  const lineTotal = parseScaled(row.cells.line_total.value)
  if (quantity === null || unitPrice === null || lineTotal === null) return 'unchecked'
  return exceedsTolerance(subScaled(mulScaled(quantity, unitPrice), lineTotal)) ? 'flagged' : 'ok'
}

// null mirrors haveLineTotal: no row carried a parseable line total, so there is no sum to
// report. printed/agrees are null together when the printed subtotal is absent or unparseable.
export function lineSumState(rows: readonly LineRow[], printedSubtotal: string | null): LineSumState | null {
  let sum: Scaled = { u: 0n, s: 0 }
  let any = false
  for (const row of rows) {
    const total = parseScaled(row.cells.line_total.value)
    if (total === null) continue
    sum = addScaled(sum, total)
    any = true
  }
  if (!any) return null
  const printed = printedSubtotal === null ? null : parseScaled(printedSubtotal)
  if (printed === null) return { sum: renderScaled(sum), printed: null, agrees: null }
  return {
    sum: renderScaled(sum),
    printed: renderScaled(printed),
    agrees: !exceedsTolerance(subScaled(sum, printed)),
  }
}

// -- row edits ------------------------------------------------------------------------------

// The whole cell moves, not just the value: the operator is saying this column was misread, so
// the text and the box it came from both belong to the other role. Swapping values alone would
// leave the moved text pointing at the old column's region.
export function remapRoles(rows: readonly LineRow[], from: LineRole, to: LineRole): LineRow[] {
  return rows.map((row) => ({
    ...row,
    cells: { ...row.cells, [from]: row.cells[to], [to]: row.cells[from] },
  }))
}

function freshKey(rows: readonly LineRow[]): string {
  const used = new Set(rows.map((row) => row.key))
  let n = 1
  while (used.has(`n${n}`)) n += 1
  return `n${n}`
}

export function addRow(rows: readonly LineRow[]): LineRow[] {
  return [
    ...rows,
    {
      key: freshKey(rows),
      cells: {
        description: blankCell(null),
        quantity: blankCell(null),
        unit_price: blankCell(null),
        line_total: blankCell(null),
      },
    },
  ]
}

// Total: an out-of-range position returns a fresh copy unchanged. 0-based, like the array it
// indexes; the 1..N the grid renders is `at + 1`.
export function removeRow(rows: readonly LineRow[], at: number): LineRow[] {
  if (at < 0 || at >= rows.length) return [...rows]
  return rows.filter((_row, i) => i !== at)
}

// -- the wire body --------------------------------------------------------------------------

// Trim decides, never rewrites -- canonField's rule. A blank cell posts as null; every other
// cell posts verbatim, spaces intact.
function postValue(cell: LineCell): string | null {
  return cell.value.trim() === '' ? null : cell.value
}

export function linesToPost(rows: readonly LineRow[]): LineItemInput[] {
  return rows
    .map((row) => ({
      description: postValue(row.cells.description),
      quantity: postValue(row.cells.quantity),
      unit_price: postValue(row.cells.unit_price),
      line_total: postValue(row.cells.line_total),
    }))
    .filter((line) => LINE_ROLES.some((role) => line[role] !== null))
}

// diffLineItems' shape as a boolean, because Save's `disabled` is what consumes it: positional
// over the four roles, both sides canonicalised, different lengths mean changed.
export function lineSetChanged(wireRows: readonly LineRow[], draftRows: readonly LineRow[]): boolean {
  if (wireRows.length !== draftRows.length) return true
  return !wireRows.every((wire, i) =>
    LINE_ROLES.every((role) => postValue(wire.cells[role]) === postValue(draftRows[i].cells[role])),
  )
}
