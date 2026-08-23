// The audit table's chrome: the scroll container, the head row, and the body slot.
//
// MembersTable.tsx's shape, for the same reasons: a CSS grid rather than a <table> (the
// screen idiom here), `overflowX: 'auto'` on the outer div, and `minWidth` restated on the
// card AND on every direct child. The restatement is what makes a row refuse to collapse
// -- the container alone would scroll while the rows squeezed.
//
// The geometry constants come from AuditRow.tsx, which owns them (the shipped
// REVIEW_GRID_COLUMNS precedent), so head and body cannot drift.

import type { ReactNode } from 'react'

import { AUDIT_COLS, AUDIT_GRID_GAP, AUDIT_TABLE_MIN_WIDTH } from './AuditRow'

// The trailing '' is the chevron rail: at 44px no uppercase 10.5px label fits, and no
// disclosure column in this app is labelled.
export const AUDIT_HEADS = ['Who', 'What', 'Company', 'When', '']

const ELLIPSIS = { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' } as const

export function AuditTable({ children }: { children: ReactNode }) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <div
        data-testid="audit-table"
        style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden', minWidth: AUDIT_TABLE_MIN_WIDTH }}
      >
        {/* Not `.pf-list-head`: that class collapses to one column at <=480px
            (platform.css:264-276), which would fight the minWidth this table depends on. */}
        <div
          data-testid="audit-table-head"
          style={{ display: 'grid', gridTemplateColumns: AUDIT_COLS, gap: AUDIT_GRID_GAP, padding: '11px 18px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: AUDIT_TABLE_MIN_WIDTH }}
        >
          {AUDIT_HEADS.map((h, i) => (
            <span key={i} className="label" style={ELLIPSIS}>
              {h}
            </span>
          ))}
        </div>
        {children}
      </div>
    </div>
  )
}
