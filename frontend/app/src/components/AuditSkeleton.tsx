// Loading placeholder rows in the REAL column geometry, so nothing moves when data lands.
// A centred spinner would say only "wait"; this says what is coming and where.
//
// The geometry comes from AuditRow's exported constants -- restating the template here is
// exactly the bug AuditSkeleton.test.tsx asserts against, because a restatement drifts
// silently and only shows up as a jump on the running page.
//
// The `shimmer` keyframe is global (styles/platform.css:22); the shipped consumers are
// SourceDocumentStates.tsx and ImportProgress.tsx.

import { AUDIT_COLS, AUDIT_GRID_GAP, AUDIT_TABLE_MIN_WIDTH } from './AuditRow'

const ROWS = 8

// Uneven widths: equal bars read as a rendered table of identical values rather than as
// pending content.
const WIDTHS = ['62%', '78%', '54%', '70%', '0%']

export function AuditSkeleton() {
  return (
    <>
      {Array.from({ length: ROWS }, (_, i) => (
        <div
          key={i}
          data-testid="audit-skeleton-row"
          aria-hidden
          style={{ display: 'grid', gridTemplateColumns: AUDIT_COLS, gap: AUDIT_GRID_GAP, padding: '12px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: AUDIT_TABLE_MIN_WIDTH }}
        >
          {WIDTHS.map((w, c) => (
            <span
              key={c}
              style={{
                height: 10,
                width: w,
                borderRadius: 99,
                background: 'linear-gradient(90deg, var(--bg-3) 0%, var(--bg-4) 50%, var(--bg-3) 100%)',
                backgroundSize: '200% 100%',
                animation: 'shimmer 1.4s linear infinite',
                // Staggered, so the table reads as one surface loading rather than eight
                // independent bars.
                animationDelay: `${i * 0.06}s`,
              }}
            />
          ))}
        </div>
      ))}
    </>
  )
}
