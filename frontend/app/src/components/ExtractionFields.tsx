// The fields pane: the artboard's right column. A header over a scrollable body holding one
// cell per wire field — label, value, why it is in doubt, what a person changed, and the
// selection the document pane follows.
// Pinned by ExtractionFields.test.tsx.

import type { CSSProperties } from 'react'

import { correctedMarker, fieldLabel, fieldNote, reasonPill } from '../lib/extractionReview'
import type { DraftEntries, ExtractionCandidate, ExtractionFieldState } from '../lib/extractionReview'

const TITLE = 'The invoice as it will be filed'
const NO_REGION = 'NO REGION'
const NO_FIELDS = 'Nothing was extracted from this document.'

// internal/invoice/store.go:205-221 overwrites both supplier fields from the signed-in entity
// on every Store.Create, so neither value below is what gets filed.
const SUPPLIER_NOTE = 'The supplier filed on this invoice comes from your client record, not from this document.'

const SUPPLIER_FIELDS = ['supplier_tin', 'supplier_name']

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
const LABEL_STRIP: CSSProperties = { display: 'flex', alignItems: 'center', gap: 8, minHeight: 18 }

const LABEL: CSSProperties = { fontSize: 12, fontWeight: 500, color: 'var(--fg-2)' }

const VALUE: CSSProperties = { fontSize: 13, fontWeight: 500, color: 'var(--fg-1)', overflowWrap: 'anywhere' }

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

// draft and the three write callbacks are the seam EXTR-12-07's controls hang off. Optional
// while the cell still renders a read-only value span, so ExtractionReview needs no change to
// widen it.
export function ExtractionFields({
  fields,
  selected,
  onSelect,
}: {
  fields: ExtractionFieldState[]
  selected: string | null
  draft?: DraftEntries
  onSelect: (name: string) => void
  onType?: (name: string, value: string) => void
  onChoose?: (name: string, candidate: ExtractionCandidate) => void
  onUndo?: (name: string) => void
}) {
  const supplier = fields.some((f) => SUPPLIER_FIELDS.includes(f.name))

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
              {fields.map((f) => {
                const on = f.name === selected
                // A settled field stops shouting: no pill of either kind, no note, a marker
                // instead. mock.go gives total, subtotal and buyer_tin no region and a typed
                // correction leaves the region alone, so the cue outlives the reason otherwise.
                const settled = correctedMarker(f.corrected, f.region)
                const note = settled === null ? fieldNote(f.reason, f.name) : null
                // One slot, and the reason outranks the region cue.
                const cue = f.region === null ? NO_REGION : null
                const pill = settled === null ? (reasonPill(f.reason) ?? cue) : null
                return (
                  <button
                    key={f.name}
                    type="button"
                    data-testid={`extraction-field-${f.name}`}
                    aria-pressed={on}
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
                    <span data-testid={`extraction-control-${f.name}`} style={CONTROL}>
                      <span style={VALUE}>{f.value === null || f.value === '' ? '—' : f.value}</span>
                      {settled === null ? null : (
                        <span data-testid={`extraction-marker-${f.name}`} style={MARKER} />
                      )}
                    </span>
                    {note === null ? null : <span style={FIELD_NOTE}>{note}</span>}
                    {settled === null ? null : (
                      <span style={CHANGED_ROW}>
                        <span className="mono" style={CHANGED_LABEL}>
                          {settled.label}
                        </span>
                        {settled.was === null ? null : <span style={WAS_LINE}>{settled.was}</span>}
                      </span>
                    )}
                  </button>
                )
              })}
            </div>
            {supplier ? <p style={NOTE}>{SUPPLIER_NOTE}</p> : null}
          </>
        )}
      </div>
    </div>
  )
}
