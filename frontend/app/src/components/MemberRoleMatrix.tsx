// Settings › Members — the role-capability expander, and the firm-only `Client users`
// placeholder. Both sit below the roster (MEMB-01-05).
//
// Neither is about the roster. MembersView mounts them OUTSIDE its three-way ternary so
// they survive all three of its states: what the three roles can do is a statement about
// ROLES, which no search string and no roster size can change — the same rationale
// MembersView.tsx already states for the unassigned-positions notice above the table.
//
// Nothing is derived here. `CAPABILITY_ROWS`, `ACCESS_ROLES`, `CAPABILITY_FOOTNOTE` and
// `CLIENT_USERS_COPY` all come from lib/members.ts, where `environment: node` can spec
// them (§15.8); the last two were moved there by this subtask precisely because AC#1 wants
// the footnote verbatim and a screenshot cannot tell a paraphrase from the original.

import { useId, useState, type CSSProperties } from 'react'

import { chevDownGlyph, tickGlyph11 } from '../glyphs'
import { Icon } from '../icons'
import { ACCESS_ROLES, CAPABILITY_FOOTNOTE, CAPABILITY_ROWS, CLIENT_USERS_COPY } from '../lib/members'

// §6 names this in backticks as the affordance, not as prose, so AC#1's "capability rows
// and the footnote are §6 verbatim" clause does not reach it and it stays a component
// constant rather than joining the two strings in lib/members.ts.
const MATRIX_HEADING = 'What can each role do?'

// Every glyph in this app is `aria-hidden` (icons.tsx:25), so a bare tick is a cell that
// announces nothing at all. The word is carried as real, clipped TEXT rather than as an
// `aria-label` on the cell: a cell's accessible name is honoured inconsistently across
// screen readers, its content always is. Declared locally — a shared `.sr-only` utility
// would mean editing the design-system stylesheet, which is not a copy subtask's business.
const SR_ONLY: CSSProperties = { position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap' }

// Not the shared `crossGlyph` (stroke 3): the matrix draws a lighter cross.
const matrixCross = <Icon paths={['M18 6 6 18M6 6l12 12']} size={11} strokeWidth={2.4} />

const MATRIX_COLS = 'minmax(0,1fr) 90px 90px 90px'

/**
 * The collapsed "What can each role do?" expander: a card whose full-width header is the toggle,
 * with three role columns x the eight capability rows and §6's footnote in the revealed body.
 *
 * State is local. The toggle is a real `<button>` carrying `aria-expanded`; `useDismiss` is not
 * used, because closing an expander on an outside click would throw away the reading position.
 * A CSS grid with table ARIA roles: a cell means nothing without both its row and its column.
 */
export function MemberRoleMatrix() {
  const [open, setOpen] = useState(false)
  const headingId = useId()
  const bodyId = useId()

  return (
    <div style={{ marginTop: 14, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <button
        type="button"
        id={headingId}
        data-testid="role-matrix-toggle"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={() => setOpen((v) => !v)}
        // `.pf-tab`, not `.pf-btn`: App.routeBoot.test.tsx filters this toggle out of its `.pf-tab` scan.
        className="pf-tab"
        style={{
          width: '100%',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 10,
          border: 0,
          background: 'transparent',
          padding: '14px 18px',
          cursor: 'pointer',
          fontFamily: 'var(--font-sans)',
          fontSize: 13.5,
          fontWeight: 600,
          color: 'var(--fg-1)',
          textAlign: 'left',
        }}
      >
        {MATRIX_HEADING}
        <span style={{ display: 'inline-flex', flex: 'none', color: 'var(--fg-3)', transform: open ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform var(--dur-base) var(--ease-out)' }}>
          {chevDownGlyph}
        </span>
      </button>

      {open && (
        <div id={bodyId} data-testid="role-matrix" style={{ borderTop: '1px solid var(--line-1)', padding: '6px 18px 18px' }}>
          <div role="table" aria-labelledby={headingId}>
            <div role="row" style={{ display: 'grid', gridTemplateColumns: MATRIX_COLS, alignItems: 'center', gap: 8, padding: '10px 0 8px' }}>
              {/* The corner cell heads neither a row nor a column, so it carries no role. */}
              <span />
              {/* Column order is ACCESS_ROLES order by construction: Admin, Preparer, Reviewer. */}
              {ACCESS_ROLES.map((r) => (
                <span key={r.id} role="columnheader" className="label" style={{ textAlign: 'center' }}>
                  {r.label}
                </span>
              ))}
            </div>
            {CAPABILITY_ROWS.map((row) => (
              <div
                key={row.label}
                role="row"
                style={{ display: 'grid', gridTemplateColumns: MATRIX_COLS, alignItems: 'center', gap: 8, padding: '9px 0', borderTop: '1px solid var(--line-1)' }}
              >
                {/* Rendered exactly as stored, lowercase: §6's own casing (`text-transform` would corrupt `ERP`). */}
                <span role="rowheader" style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>
                  {row.label}
                </span>
                {ACCESS_ROLES.map((r) => {
                  const allowed = row[r.id]
                  return (
                    <span key={r.id} role="cell" style={{ display: 'grid', placeItems: 'center', color: allowed ? 'var(--action)' : 'var(--fg-4)' }}>
                      {/* Teal tick / muted cross, not a pass/fail pair: a Preparer who cannot approve is the role working as designed. */}
                      <span style={{ display: 'inline-flex' }}>{allowed ? tickGlyph11 : matrixCross}</span>
                      <span style={SR_ONLY}>{allowed ? 'Yes' : 'No'}</span>
                    </span>
                  )
                })}
              </div>
            ))}
          </div>

          <p style={{ margin: '14px 0 0', fontSize: 11.5, lineHeight: 1.6, color: 'var(--fg-3)' }}>{CAPABILITY_FOOTNOTE}</p>
        </div>
      )}
    </div>
  )
}

/**
 * §6's firm-only `Client users` placeholder, kept so the open question stays visible. Gated by the
 * caller, which renders nothing in in-house mode. A static `div`: nothing to click, so nothing to
 * disable; the visible `NOT BUILT` marker is the explanation.
 */
export function ClientUsersCard() {
  return (
    <div
      data-testid="client-users-card"
      style={{ marginTop: 14, display: 'flex', alignItems: 'flex-start', gap: 12, background: 'var(--bg-1)', border: '1px dashed var(--line-2)', borderRadius: 'var(--radius-md)', padding: '14px 16px' }}
    >
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 13.5, fontWeight: 600, color: 'var(--fg-2)' }}>Client users</div>
        <div style={{ marginTop: 3, fontSize: 12, lineHeight: 1.55, color: 'var(--fg-3)' }}>{CLIENT_USERS_COPY}</div>
      </div>
      <span className="mono" style={{ flex: 'none', fontSize: 9, fontWeight: 700, letterSpacing: '0.09em', color: 'var(--fg-3)', border: '1px dashed var(--line-3)', borderRadius: 4, padding: '3px 9px' }}>
        NOT BUILT
      </span>
    </div>
  )
}
