// The fields pane: the artboard's right column. A header over a scrollable body holding one
// cell per wire field — label, value, and the selection the document pane follows.
// Pinned by ExtractionFields.test.tsx.

import type { CSSProperties } from 'react'

import type { ExtractionFieldState } from '../lib/extractionReview'

const TITLE = 'The invoice as it will be filed'
const NO_REGION = 'NO REGION'
const NO_FIELDS = 'We read no fields from this document.'

// internal/invoice/store.go:205-221 overwrites both supplier fields from the signed-in entity
// on every Store.Create, so neither value below is what gets filed.
const SUPPLIER_NOTE = 'The supplier we file comes from your client record, not from this document.'

const SUPPLIER_FIELDS = ['supplier_tin', 'supplier_name']

const PANE: CSSProperties = {
  width: 620,
  flex: '1 1 620px',
  minWidth: 470,
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

const EMPTY_PANEL: CSSProperties = {
  padding: '14px 16px',
  border: '1px dashed var(--line-3)',
  borderRadius: 'var(--radius-md)',
  background: 'transparent',
  fontSize: 12.5,
  color: 'var(--fg-3)',
}

export function ExtractionFields({
  fields,
  selected,
  onSelect,
}: {
  fields: ExtractionFieldState[]
  selected: string | null
  onSelect: (name: string) => void
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
                return (
                  <button
                    key={f.name}
                    type="button"
                    data-testid={`extraction-field-${f.name}`}
                    aria-pressed={on}
                    // Unguarded: re-reporting the selected name is how a reader who scrolled
                    // the document away gets back to the region in one action.
                    onClick={() => onSelect(f.name)}
                    style={on ? SELECTED_CELL : CELL}
                  >
                    <span style={LABEL_STRIP}>
                      <span style={LABEL}>{f.name}</span>
                      {f.region === null ? (
                        <span className="mono" style={PILL}>
                          {NO_REGION}
                        </span>
                      ) : null}
                    </span>
                    <span style={VALUE}>{f.value === null || f.value === '' ? '—' : f.value}</span>
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
