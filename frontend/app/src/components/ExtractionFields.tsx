// The fields pane: the artboard's right column. A header over a scrollable body holding one
// cell per HEADER wire field — label, value, why it is in doubt, what a person changed, and the
// selection the document pane follows — with LineItemGrid below them for the line block.
// Pinned by ExtractionFields.test.tsx.

import type { CSSProperties } from 'react'

import { crosshairGlyph } from '../glyphs'
import { applyDraft, correctedMarker, fieldLabel, fieldNote, reasonPill, regionPhrase } from '../lib/extractionReview'
import type { DraftEntries, ExtractionCandidate, ExtractionFieldState } from '../lib/extractionReview'
import { linesFromFields } from '../lib/lineItems'
import type { LineRole, LineRow } from '../lib/lineItems'
import { LineItemGrid } from './LineItemGrid'

const TITLE = 'The invoice as it will be filed'
const NO_REGION = 'NO REGION'
const NO_FIELDS = 'Nothing was extracted from this document.'
const UNDO = 'Undo'
const INVOICE_NUMBER_LOCKED = "The invoice number is this invoice's identity and cannot be changed here."

// The artboard's missing-field labels (`:661-663`). POINT_ARMED replaces its click-arm string:
// our one gesture is a drag, and under the 24x12 floor a click returns nothing.
const POINT_IDLE = 'Not found — point at it on the document'
const POINT_ARMED = 'Waiting — drag a box around it on the document'
const POINT_PAGELESS = 'Not found — type it in'
const POINT_CANCEL = 'Stop pointing'

// internal/invoice/store.go:205-221 overwrites both supplier fields from the signed-in entity
// on every Store.Create, so neither value below is what gets filed.
const SUPPLIER_NOTE = 'The supplier filed on this invoice comes from your client record, not from this document.'

const SUPPLIER_FIELDS = ['supplier_tin', 'supplier_name']

// The line block and its cells belong to LineItemGrid. The block row itself carries no value and
// no region, so neither surface renders it. EXTR11-E2E-02a and EXTR12-E2E-07 count rendered
// `extraction-field-*` against the header half of the wire, which is what this keeps true.
const LINE_FIELD_RE = /^line_items(\[[1-9][0-9]*\]\.(description|quantity|unit_price|line_total))?$/

function isHeaderField(name: string): boolean {
  return !LINE_FIELD_RE.test(name)
}

// internal/extraction/handlers_correction.go, lockedFields: a correction on any of the three is
// a 422, and updateContentTx re-derives the two supplier fields from the client entity anyway.
// readOnly, never disabled -- a disabled input leaves the tab order and fires no focus, so the
// cell would stop being reachable or selectable by keyboard.
const LOCKED_FIELDS = ['invoice_number', ...SUPPLIER_FIELDS]

// SUPPLIER_NOTE already says why the two supplier cells are locked, at pane level.
const LOCK_REASONS: Record<string, string> = { invoice_number: INVOICE_NUMBER_LOCKED }

const PANE: CSSProperties = {
  width: 620,
  flex: '1 1 620px',
  minWidth: 470,
  minHeight: 0,
  display: 'flex',
  flexDirection: 'column',
  background: 'var(--bg-1)',
}

const HEADER: CSSProperties = {
  flex: 'none',
  padding: '13px 20px',
  background: 'var(--bg-2)',
  borderBottom: '1px solid var(--line-1)',
}

const TITLE_TEXT: CSSProperties = { fontSize: 15, fontWeight: 600, letterSpacing: '-0.02em' }

// y only, never the shorthand: a row that spills its column has to scroll rather than hide.
const BODY: CSSProperties = { flex: 1, minHeight: 0, overflowY: 'auto', padding: '16px 20px 24px' }

const GRID: CSSProperties = { display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '14px 16px' }

// `minWidth: 0` with the value's `overflowWrap`: a grid item's automatic minimum is
// content-based, so one long unbroken value would otherwise push the row past its column.
const CELL: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: 6,
  minWidth: 0,
  padding: '6px 8px',
  border: 0,
  borderRadius: 'var(--radius-md)',
  background: 'transparent',
  fontFamily: 'var(--font-sans)',
  textAlign: 'left',
  cursor: 'pointer',
}

// SourceDocumentSheet.tsx:317's marked-row treatment, in the same amber the document pane
// paints the region with.
const SELECTED_CELL: CSSProperties = { ...CELL, background: 'var(--accent-10)', boxShadow: 'inset 2px 0 0 var(--accent)' }

// The `min-height` keeps a row without a pill the same height as its neighbour with one.
// `flex-wrap` is ours, not the artboard's (`:293`): at the pane's 470px floor the label is at
// min-content and the pill declares `flex: none`, so a nowrap strip spills its column by 7.8px
// on `issue_date` and 6.0px on `vat`. Pinned by `wraps the label strip` and EXTR12-E2E-07.
const LABEL_STRIP: CSSProperties = { display: 'flex', alignItems: 'center', gap: 8, minHeight: 18, flexWrap: 'wrap' }

const LABEL: CSSProperties = { fontSize: 12, fontWeight: 500, color: 'var(--fg-2)' }

// The artboard's own pill slot (`:296`), not RulePills.tsx — the amber triple is shared, the
// two font metrics are not.
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

const NOTE: CSSProperties = { margin: '12px 0 0', fontSize: 11.5, color: 'var(--fg-2)', lineHeight: 1.5 }

// The artboard's `:301` seat for the marker. EXTR-12-07 swaps the value span inside it for a
// full-width input and the wrapper's box is unchanged, so the marker's geometry does not move.
const CONTROL: CSSProperties = { position: 'relative', display: 'flex', alignItems: 'center' }

// `:307`. The artboard's `var(--accent)` there means teal, which is `--action` in the app layer.
const MARKER: CSSProperties = {
  position: 'absolute',
  right: 11,
  width: 7,
  height: 7,
  borderRadius: 2,
  background: 'var(--action)',
  pointerEvents: 'none',
}

// The artboard's own input (`:305`). The class carries the box and `width: 100%`; the inline
// padding is the room the marker needs, and NO inline width -- one would override the cell's
// `min-width: 0` and let a long value widen the grid track.
const INPUT: CSSProperties = { paddingRight: 30 }
const LOCKED_INPUT: CSSProperties = { ...INPUT, color: 'var(--fg-3)' }

// `:312-320`. `.pf-chip` is barred here: app-layer.css:275 forces `border-radius: pill` over the
// artboard's 10px card, and the pane's own walk forbids the class for that reason.
const CHIP_ROW: CSSProperties = { display: 'flex', gap: 8 }

const CHIP: CSSProperties = {
  flex: 1,
  minWidth: 0,
  textAlign: 'left',
  border: '1px solid var(--line-2)',
  background: 'var(--bg-2)',
  borderRadius: 10,
  padding: '8px 11px',
  fontFamily: 'var(--font-sans)',
  cursor: 'pointer',
  transition: 'background 120ms, border-color 120ms, color 120ms',
}

// The artboard's `a.border` hook, moved to the app layer's own teal: a drafted pick has to be
// visible before Save, which the artboard never has to show.
const CHIP_PICKED: CSSProperties = { ...CHIP, border: '1px solid var(--action)' }

// Block <span>s, not the artboard's <div>s: flow content is invalid inside a <button>, and the
// two lines still have to stack.
const CHIP_VALUE: CSSProperties = {
  display: 'block',
  fontSize: 13,
  fontWeight: 500,
  color: 'var(--fg-1)',
  overflowWrap: 'anywhere',
}

// `:317`. Uppercased in CSS, never in JavaScript: `text-transform` leaves textContent the phrase
// the copy table declares, so the pane's own residue sweep still reads it.
const CHIP_WHERE: CSSProperties = {
  display: 'block',
  fontSize: 8.5,
  color: 'var(--fg-3)',
  letterSpacing: '0.05em',
  marginTop: 4,
  textTransform: 'uppercase',
  overflowWrap: 'anywhere',
}

// `:337`, without `.pf-btn` -- same radius override.
const UNDO_BUTTON: CSSProperties = {
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

// `:324`, without `.pf-btn` (it forces a pill radius over the artboard's 10px card) and without
// its `width: 100%` (the cell is a flex column, so the button already stretches).
// The 1.5px is the artboard's, and load-bearing: `moves the border, the ground and the label
// together when it arms` pins both dashes, so rounding it to 1px reds.
const POINT_BUTTON: CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 9,
  minHeight: 38,
  padding: '0 12px',
  border: '1.5px dashed var(--line-3)',
  background: 'transparent',
  borderRadius: 10,
  color: 'var(--fg-2)',
  fontFamily: 'var(--font-sans)',
  fontSize: 12.5,
  fontWeight: 500,
  textAlign: 'left',
  cursor: 'pointer',
}

// `--ds-amber` in the artboard (`:658`) does not exist here; the DS amber IS `--accent`. All
// three declarations move together, so the armed state is legible without hover.
const POINT_ARMED_BUTTON: CSSProperties = {
  ...POINT_BUTTON,
  border: '1.5px dashed var(--accent)',
  background: 'var(--status-amber-bg)',
  color: 'var(--status-amber-text)',
}

const POINT_GLYPH: CSSProperties = { flex: 'none', display: 'inline-flex' }

// `:330` as a block <span>: <p> is flow content and invalid inside the cell's <button>.
const FIELD_NOTE: CSSProperties = {
  display: 'block',
  fontSize: 11.5,
  color: 'var(--fg-2)',
  lineHeight: 1.5,
  textWrap: 'pretty',
}

// `:333-336`. Undo is EXTR-12-07's.
const CHANGED_ROW: CSSProperties = { display: 'flex', alignItems: 'center', gap: 9, flexWrap: 'wrap' }
const CHANGED_LABEL: CSSProperties = { fontSize: 8.5, fontWeight: 700, letterSpacing: '0.07em', color: 'var(--action)' }
const WAS_LINE: CSSProperties = { fontSize: 11.5, color: 'var(--fg-3)' }

const EMPTY_PANEL: CSSProperties = {
  padding: '14px 16px',
  border: '1px dashed var(--line-3)',
  borderRadius: 'var(--radius-md)',
  background: 'transparent',
  fontSize: 12.5,
  color: 'var(--fg-3)',
}

// The pane holds no draft of its own: the shell owns the write, the way ExpandedFixPanel holds
// the draft above FixCardView. `fields` is the WIRE and `draft` the overlay, kept apart because
// the cell needs both -- the controls show the drafted value, while the chip row is the set of
// readings the extractor offered and must not fold a chosen one back in as a duplicate.
export function ExtractionFields({
  fields,
  selected,
  draft,
  armed,
  canPoint,
  onSelect,
  onType,
  onChoose,
  onUndo,
  onArm,
  onDisarm,
  lineRows,
  onLineEdit = () => {},
  onLineAdd = () => {},
  onLineRemove = () => {},
  onLineRemap = () => {},
}: {
  fields: ExtractionFieldState[]
  selected: string | null
  draft: DraftEntries
  /** The one field waiting for a box, or null. */
  armed: string | null
  /** False on a job with no page images: the button types instead of pointing. */
  canPoint: boolean
  onSelect: (name: string) => void
  onType: (name: string, value: string) => void
  onChoose: (name: string, candidate: ExtractionCandidate) => void
  onUndo: (name: string) => void
  onArm: (name: string) => void
  onDisarm: () => void
  /** The shell's line draft. Undefined until it holds one, and then the wire's own rows show. */
  lineRows?: LineRow[]
  onLineEdit?: (at: number, role: LineRole, value: string) => void
  onLineAdd?: () => void
  onLineRemove?: (at: number) => void
  onLineRemap?: (from: LineRole, to: LineRole) => void
}) {
  const header = fields.filter((f) => isHeaderField(f.name))
  const supplier = header.some((f) => SUPPLIER_FIELDS.includes(f.name))
  // Same length, same order: applyDraft maps the array (extractionReview.test.ts, "leaves a
  // field with no entry byte-identical").
  const drafted = applyDraft(header, draft)
  const wireLines = linesFromFields(fields)
  const subtotal = drafted.find((f) => f.name === 'subtotal')?.value ?? null

  return (
    <div data-testid="extraction-fields" style={PANE}>
      <div style={HEADER}>
        <span style={TITLE_TEXT}>{TITLE}</span>
      </div>

      <div style={BODY}>
        {fields.length === 0 ? (
          <div style={EMPTY_PANEL}>{NO_FIELDS}</div>
        ) : (
          <>
            <div style={GRID}>
              {header.map((wire, i) => {
                const f = drafted[i]
                const on = f.name === selected
                // A settled field stops shouting: no pill of either kind, no note, a marker
                // instead. mock.go gives total, subtotal and buyer_tin no region and a typed
                // correction leaves the region alone, so the cue outlives the reason otherwise.
                const settled = correctedMarker(f.corrected, f.region)
                const note = settled === null ? fieldNote(f.reason, f.name) : null
                // One slot, and the reason outranks the region cue.
                const cue = f.region === null ? NO_REGION : null
                const pill = settled === null ? (reasonPill(f.reason) ?? cue) : null
                // The gate is the REASON first -- a field can carry alternatives the extractor
                // ranked below a reading it is sure of, and those stay off the screen. The
                // second clause is the render invariant, not a weakening: no chips means there
                // is an input. Reconcile only sets `ambiguous` with two or more candidates
                // (reconcile.go), so an ambiguous field with nothing to choose between is a
                // shape the producer cannot emit.
                const candidates =
                  wire.reason === 'ambiguous' && wire.alternatives.length > 0
                    ? [{ value: wire.value, region: wire.region }, ...wire.alternatives]
                    : null
                const locked = LOCKED_FIELDS.includes(f.name)
                const lock = LOCK_REASONS[f.name] ?? null
                // The value the invoice holds, or the one the draft will settle it to. With no
                // chosen entry it falls back to the WIRE's own reading, so the row always says
                // which of the candidates is filed -- an ambiguous cell renders no input, and a
                // chip's position is not a signal anyone can read.
                const picked = draft[f.name]?.kind === 'chosen' ? draft[f.name].value : wire.value
                return (
                  // A <div>, not a <button>: a button may not contain interactive content, and
                  // `aria-pressed` is valid only on a button role — hence `aria-current`, which
                  // means the current item in a set and is valid anywhere.
                  <div
                    key={f.name}
                    data-testid={`extraction-field-${f.name}`}
                    aria-current={on}
                    // Unguarded, per AC-4: the shell bumps its nonce on a repeat click and
                    // ExtractionCanvas re-centres the region (`D-25`).
                    onClick={() => onSelect(f.name)}
                    style={on ? SELECTED_CELL : CELL}
                  >
                    <span style={LABEL_STRIP}>
                      <span style={LABEL}>{fieldLabel(f.name)}</span>
                      {pill === null ? null : (
                        <span className="mono" style={PILL}>
                          {pill}
                        </span>
                      )}
                    </span>
                    {candidates === null ? (
                      <span data-testid={`extraction-control-${f.name}`} style={CONTROL}>
                        <input
                          data-testid={`extraction-input-${f.name}`}
                          className="pf-input"
                          value={f.value ?? ''}
                          readOnly={locked}
                          aria-readonly={locked}
                          aria-label={fieldLabel(f.name)}
                          onChange={(e) => onType(f.name, e.target.value)}
                          onFocus={() => onSelect(f.name)}
                          style={locked ? LOCKED_INPUT : INPUT}
                        />
                        {settled === null ? null : (
                          <span data-testid={`extraction-marker-${f.name}`} style={MARKER} />
                        )}
                      </span>
                    ) : (
                      <span style={CHIP_ROW}>
                        {candidates.map((a, i) => (
                          <button
                            key={`${a.value ?? ''}-${i}`}
                            type="button"
                            data-testid={`extraction-chip-${f.name}-${i}`}
                            aria-current={picked === a.value}
                            onClick={() => onChoose(f.name, a)}
                            style={picked === a.value ? CHIP_PICKED : CHIP}
                          >
                            <span style={CHIP_VALUE}>{a.value ?? ''}</span>
                            {regionPhrase(a.region) === null ? null : (
                              <span
                                className="mono"
                                data-testid={`extraction-chip-where-${f.name}-${i}`}
                                style={CHIP_WHERE}
                              >
                                {regionPhrase(a.region)}
                              </span>
                            )}
                          </button>
                        ))}
                      </span>
                    )}
                    {wire.reason !== 'missing' ? null : (
                      <button
                        type="button"
                        data-testid={`extraction-point-${f.name}`}
                        // `aria-current`, never `aria-pressed`: the pane's own negative forbids
                        // the latter, and the armed field is the current one in a set of cells.
                        aria-current={armed === f.name}
                        // No pages, nothing to point at: the button types instead, and the
                        // cell's own bubbled onClick still selects. Never `disabled` -- the
                        // pane's keyboard walk requires every control to stay in the tab order.
                        onClick={canPoint ? () => onArm(f.name) : undefined}
                        style={armed === f.name ? POINT_ARMED_BUTTON : POINT_BUTTON}
                      >
                        <span style={POINT_GLYPH}>{crosshairGlyph}</span>
                        {!canPoint ? POINT_PAGELESS : armed === f.name ? POINT_ARMED : POINT_IDLE}
                      </button>
                    )}
                    {armed === f.name ? (
                      <button
                        type="button"
                        data-testid={`extraction-point-cancel-${f.name}`}
                        onClick={onDisarm}
                        style={UNDO_BUTTON}
                      >
                        {POINT_CANCEL}
                      </button>
                    ) : null}
                    {lock === null ? null : (
                      <span data-testid={`extraction-lock-${f.name}`} style={FIELD_NOTE}>
                        {lock}
                      </span>
                    )}
                    {note === null ? null : <span style={FIELD_NOTE}>{note}</span>}
                    {settled === null ? null : (
                      <span style={CHANGED_ROW}>
                        <span className="mono" style={CHANGED_LABEL}>
                          {settled.label}
                        </span>
                        {settled.was === null ? null : <span style={WAS_LINE}>{settled.was}</span>}
                        <button
                          type="button"
                          data-testid={`extraction-undo-${f.name}`}
                          onClick={() => onUndo(f.name)}
                          style={UNDO_BUTTON}
                        >
                          {UNDO}
                        </button>
                      </span>
                    )}
                  </div>
                )
              })}
            </div>
            {supplier ? <p style={NOTE}>{SUPPLIER_NOTE}</p> : null}
            {/* `onSelectCell` is the pane's own `onSelect`, so a line cell reaches
                ExtractionCanvas's find-by-name over the same channel a header cell does --
                nonce bump and all. */}
            <LineItemGrid
              rows={lineRows ?? wireLines}
              wireRows={wireLines}
              subtotal={subtotal}
              selected={selected}
              onSelectCell={onSelect}
              onEditCell={onLineEdit}
              onAddRow={onLineAdd}
              onRemoveRow={onLineRemove}
              onRemapRoles={onLineRemap}
            />
          </>
        )}
      </div>
    </div>
  )
}
