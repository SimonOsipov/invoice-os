// Loading placeholder rows in the REAL column geometry, so nothing moves when data lands.
// A centred spinner would say only "wait"; this says what is coming and where.
//
// The geometry comes from AuditRow's exported constants -- restating the template here is
// exactly the bug AuditSkeleton.test.tsx asserts against, because a restatement drifts
// silently and only shows up as a jump on the running page.
//
// `pulse` is the v2 keyframe in packages/design-tokens/v2/app-layer.css.

import { AUDIT_COLS, AUDIT_GRID_GAP, AUDIT_TABLE_MIN_WIDTH } from './AuditRow'

const ROWS = 8

const BAR = { background: 'var(--bg-3)', animation: 'pulse 1.4s linear infinite' } as const

// Uneven widths: equal bars read as a rendered table of identical values rather than as
// pending content.
const WIDTHS = ['62%', '78%', '100%', '100%', '0%']

export function AuditSkeleton() {
  return (
    <>
      {Array.from({ length: ROWS }, (_, i) => (
        <div
          key={i}
          data-testid="audit-skeleton-row"
          aria-hidden
          style={{ display: 'grid', gridTemplateColumns: AUDIT_COLS, gap: AUDIT_GRID_GAP, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: AUDIT_TABLE_MIN_WIDTH }}
        >
          {WIDTHS.map((w, c) => (
            <span key={c} style={{ display: 'flex', alignItems: 'center', gap: 9, minWidth: 0 }}>
              {c === 0 && <span style={{ ...BAR, width: 26, height: 26, flex: '0 0 auto', borderRadius: '50%' }} />}
              <span style={{ ...BAR, height: 10, width: w, borderRadius: 4 }} />
            </span>
          ))}
        </div>
      ))}
    </>
  )
}
