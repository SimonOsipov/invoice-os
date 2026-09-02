// The line-item grid: every parsed row, its arithmetic flag, the role remap and the empty
// state. Stateless -- the shell owns the draft and passes it down, the way ExtractionFields
// holds none above ExtractionReview.
// Pinned by LineItemGrid.test.tsx.

import type { CSSProperties } from 'react'

import { reasonPill } from '../lib/extractionReview'
import { LINE_ROLES, lineSumState, rowArithmetic } from '../lib/lineItems'
import type { LineRole, LineRow } from '../lib/lineItems'

const EMPTY_FOUND = 'We found no line items on this document.'
// internal/invoice's `line-items-required` rule (migrations/20260711121327_seed_mbs_v1.sql:28)
// is an error, not a warning, so an empty grid has to say what it costs.
const EMPTY_CONSEQUENCE = 'An invoice cannot be filed until it has at least one line, so add one here.'
const ADD = 'Add a line'
const REMOVE = 'Remove'

const ROLE_LABEL: Record<LineRole, string> = {
  description: 'Description',
  quantity: 'Quantity',
  unit_price: 'Unit price',
  line_total: 'Line total',
}

function sumSentence(sum: string, printed: string | null): string {
  return printed === null
    ? `These lines add up to ${sum}. This document prints no subtotal to check them against.`
    : `These lines add up to ${sum}. The document prints ${printed}.`
}

// The four roles plus the flag and the remove column. tableLayout: 'fixed' with a floor is what
// makes the scrollbox below have something to scroll.
const COLUMN_WIDTH: Record<LineRole, number> = { description: 240, quantity: 100, unit_price: 130, line_total: 130 }
const FLAG_WIDTH = 150
const REMOVE_WIDTH = 84

const WRAP: CSSProperties = { display: 'flex', flexDirection: 'column', gap: 10, marginTop: 16 }

// x only, and on this node alone: the grid's overflow is absorbed here rather than by the pane
// body or the page. The deployed oracle is EXTR13-LAYOUT-01/02, not this declaration.
const SCROLL: CSSProperties = { overflowX: 'auto', minWidth: 0 }

const TABLE: CSSProperties = {
  width: '100%',
  minWidth: Object.values(COLUMN_WIDTH).reduce((a, b) => a + b, 0) + FLAG_WIDTH + REMOVE_WIDTH,
  tableLayout: 'fixed',
  borderCollapse: 'collapse',
}

const TH: CSSProperties = { padding: '0 6px 6px', textAlign: 'left', verticalAlign: 'bottom' }

// ExtractionFields' CELL (`:66`), as a table cell: the same radius and ground, so a selected
// line reads as the same object a selected header field does.
const CELL: CSSProperties = {
  padding: '4px 6px',
  border: 0,
  borderRadius: 'var(--radius-md)',
  background: 'transparent',
  verticalAlign: 'middle',
  cursor: 'pointer',
}

// SELECTED_CELL (`ExtractionFields.tsx:82`), unchanged: the document pane paints the region in
// the same amber.
const SELECTED_CELL: CSSProperties = { ...CELL, background: 'var(--accent-10)', boxShadow: 'inset 2px 0 0 var(--accent)' }

// CONTROL (`:111`) and MARKER (`:114-122`) -- EXTR-12's corrected-cell treatment, not a second
// one.
const CONTROL: CSSProperties = { position: 'relative', display: 'flex', alignItems: 'center' }

const MARKER: CSSProperties = {
  position: 'absolute',
  right: 11,
  width: 7,
  height: 7,
  borderRadius: 2,
  background: 'var(--action)',
  pointerEvents: 'none',
}

// INPUT (`:127`): the class carries the box and `width: 100%`; the padding is the marker's room
// and there is no inline width.
const INPUT: CSSProperties = { paddingRight: 30 }

// The role selector is the column heading. `.pf-input` for the box; no `.pf-btn`, which forces
// a pill radius (app-layer.css:275).
const ROLE_SELECT: CSSProperties = { paddingRight: 8, fontSize: 11.5 }

// LABEL_STRIP (`:88`) and PILL (`:94-105`), so a flag wraps rather than spilling its column.
const LABEL_STRIP: CSSProperties = { display: 'flex', alignItems: 'center', gap: 8, minHeight: 18, flexWrap: 'wrap' }

const PILL: CSSProperties = {
  flex: 'none',
  fontSize: 8.5,
  fontWeight: 700,
  letterSpacing: '0.07em',
  color: 'var(--status-amber-text)',
  background: 'var(--status-amber-bg)',
  border: '1px solid var(--status-amber-border)',
  borderRadius: 999,
  padding: '2px 8px',
  whiteSpace: 'nowrap',
}

// UNDO_BUTTON (`:174-184`): the pane's secondary action is underlined teal, never `.pf-btn`.
const TEXT_BUTTON: CSSProperties = {
  border: 0,
  background: 'transparent',
  padding: 0,
  fontFamily: 'var(--font-sans)',
  fontSize: 11.5,
  color: 'var(--action)',
  cursor: 'pointer',
  textDecoration: 'underline',
  transition: 'background 120ms ease-out',
}

const SUM_LINE: CSSProperties = { fontSize: 11.5, color: 'var(--fg-2)', lineHeight: 1.5 }

// EMPTY_PANEL (`:232-239`): MemberParts.tsx:55-57's rule, a dashed edge over a transparent
// ground.
const EMPTY_PANEL: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: 4,
  padding: '14px 16px',
  border: '1px dashed var(--line-3)',
  borderRadius: 'var(--radius-md)',
  background: 'transparent',
  fontSize: 12.5,
  color: 'var(--fg-3)',
}

const ADD_ROW: CSSProperties = { display: 'flex' }

export function LineItemGrid({
  rows,
  wireRows,
  subtotal,
  selected,
  onSelectCell,
  onEditCell,
  onAddRow,
  onRemoveRow,
  onRemapRoles,
}: {
  /** The draft the shell holds. */
  rows: LineRow[]
  /** The wire's own rows, positionally, so a changed cell can be marked. */
  wireRows: LineRow[]
  /** The drafted `subtotal` header value, or null. */
  subtotal: string | null
  selected: string | null
  onSelectCell: (wireName: string) => void
  /** `at` is 0-based, like the array; the 1..N rendered is `at + 1`. */
  onEditCell: (at: number, role: LineRole, value: string) => void
  onAddRow: () => void
  onRemoveRow: (at: number) => void
  onRemapRoles: (from: LineRole, to: LineRole) => void
}) {
  const sum = lineSumState(rows, subtotal)

  return (
    // Rendered whether or not the wire carried a line block: a real zero-line extraction puts
    // nothing on the wire, so a presence-gated grid would say nothing exactly when the invoice
    // cannot be filed.
    <div data-testid="line-item-grid" style={WRAP}>
      {rows.length === 0 ? (
        <div data-testid="line-item-empty" style={EMPTY_PANEL}>
          <span>{EMPTY_FOUND}</span>
          <span>{EMPTY_CONSEQUENCE}</span>
        </div>
      ) : (
        <>
          <div data-testid="line-item-scroll" style={SCROLL}>
            <table style={TABLE}>
              <thead>
                <tr>
                  {LINE_ROLES.map((role) => (
                    <th key={role} style={{ ...TH, width: COLUMN_WIDTH[role] }}>
                      {/* The selector reads its own column's role every render: remapRoles moves
                          the CELLS and the four columns keep LINE_ROLES order, so there is no
                          column order to hold. */}
                      <select
                        data-testid={`line-item-role-${role}`}
                        className="pf-input"
                        aria-label={`${ROLE_LABEL[role]} column`}
                        value={role}
                        onChange={(e) => onRemapRoles(role, e.target.value as LineRole)}
                        style={ROLE_SELECT}
                      >
                        {LINE_ROLES.map((other) => (
                          <option key={other} value={other}>
                            {ROLE_LABEL[other]}
                          </option>
                        ))}
                      </select>
                    </th>
                  ))}
                  <th style={{ ...TH, width: FLAG_WIDTH }} />
                  <th style={{ ...TH, width: REMOVE_WIDTH }} />
                </tr>
              </thead>
              <tbody>
                {rows.map((row, at) => {
                  const n = at + 1
                  const wire = wireRows[at] ?? null
                  const flagged = rowArithmetic(row) === 'flagged'
                  return (
                    <tr key={row.key} data-testid={`line-item-row-${n}`}>
                      {LINE_ROLES.map((role) => {
                        const cell = row.cells[role]
                        // A cell the extractor never named -- an added row -- resolves to no
                        // region, so ExtractionCanvas would render neither highlight nor
                        // banner. Clicking it focuses its input and selects nothing.
                        const on = cell.name !== null && cell.name === selected
                        // No wire counterpart means an appended row, which is never marked.
                        const changed = wire !== null && wire.cells[role].value !== cell.value
                        return (
                          <td
                            key={role}
                            data-testid={`line-item-cell-${n}-${role}`}
                            aria-current={on}
                            onClick={cell.name === null ? undefined : () => onSelectCell(cell.name as string)}
                            style={on ? SELECTED_CELL : CELL}
                          >
                            <span style={CONTROL}>
                              <input
                                data-testid={`line-item-input-${n}-${role}`}
                                className="pf-input"
                                value={cell.value}
                                aria-label={`Line ${n} ${ROLE_LABEL[role].toLowerCase()}`}
                                onChange={(e) => onEditCell(at, role, e.target.value)}
                                style={INPUT}
                              />
                              {changed ? <span data-testid={`line-item-marker-${n}-${role}`} style={MARKER} /> : null}
                            </span>
                          </td>
                        )
                      })}
                      <td style={CELL}>
                        <span style={LABEL_STRIP}>
                          {flagged ? (
                            <span className="mono" data-testid={`line-item-flag-${n}`} style={PILL}>
                              {reasonPill('inconsistent')}
                            </span>
                          ) : null}
                        </span>
                      </td>
                      <td style={CELL}>
                        {/* Never `disabled`, never hidden: the pane's keyboard walk requires
                            every control to stay in the tab order (extraction-point-*). */}
                        <button
                          type="button"
                          data-testid={`line-item-remove-${n}`}
                          aria-label={`Remove line ${n}`}
                          onClick={() => onRemoveRow(at)}
                          style={TEXT_BUTTON}
                        >
                          {REMOVE}
                        </button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
          {/* A settled sum stops shouting; a disagreeing one shows both numbers rather than
              asking the reader to work out which is which. */}
          {sum === null || sum.agrees === true ? null : (
            <p data-testid="line-item-sum" style={SUM_LINE}>
              {sumSentence(sum.sum, sum.printed)}
            </p>
          )}
        </>
      )}
      <span style={ADD_ROW}>
        <button type="button" data-testid="line-item-add" onClick={onAddRow} style={TEXT_BUTTON}>
          {ADD}
        </button>
      </span>
    </div>
  )
}
