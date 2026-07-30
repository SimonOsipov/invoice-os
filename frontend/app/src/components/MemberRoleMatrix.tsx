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

import { chevDownGlyph, crossGlyph, tickGlyph11 } from '../glyphs'
import { ACCESS_ROLES, CAPABILITY_FOOTNOTE, CAPABILITY_ROWS, CLIENT_USERS_COPY } from '../lib/members'

// §6 names this in backticks as the affordance, not as prose, so AC#1's "capability rows
// and the footnote are §6 verbatim" clause does not reach it and it stays a component
// constant rather than joining the two strings in lib/members.ts.
const MATRIX_HEADING = 'What can each role do?'

// Both blocks are reference material read once, not scanned; capping them keeps them from
// stretching to the roster's 870/1036px and reading as another table. 620 is a judgement call
// of its own, NOT the intro paragraph's width — that paragraph is `maxWidth: 560`
// (MembersView.tsx:84), and this needs the extra room for four columns of capability rows.
const BLOCK_WIDTH = 620

// Every glyph in this app is `aria-hidden` (icons.tsx:25), so a bare tick is a cell that
// announces nothing at all. The word is carried as real, clipped TEXT rather than as an
// `aria-label` on the cell: a cell's accessible name is honoured inconsistently across
// screen readers, its content always is. Declared locally — a shared `.sr-only` utility
// would mean editing the design-system stylesheet, which is not a copy subtask's business.
const SR_ONLY: CSSProperties = { position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap' }

const HEAD_CELL: CSSProperties = { padding: '10px 16px', borderBottom: '1px solid var(--line-1)' }
const BODY_CELL: CSSProperties = { padding: '9px 16px', borderBottom: '1px solid var(--line-1)' }

/**
 * The collapsed "What can each role do?" expander — three role columns × the eight
 * capability rows, plus §6's footnote inside the revealed body.
 *
 * State is local (`[panel-state-is-local]`), and the disclosure semantics are the ones
 * MemberParts' `⋯` trigger already uses: a real `<button type="button">` carrying
 * `aria-expanded`. `useDismiss` is deliberately NOT used — an expander is not a popover,
 * and closing it on an outside click would throw away a reading position the user chose.
 */
export function MemberRoleMatrix() {
  const [open, setOpen] = useState(false)
  const headingId = useId()
  const bodyId = useId()

  return (
    // MembersView returns a bare fragment with no wrapping column and no gap — every
    // sibling carries its own margin — so this block carries its own top margin too.
    <div style={{ marginTop: 22, maxWidth: BLOCK_WIDTH }}>
      <button
        type="button"
        id={headingId}
        data-testid="role-matrix-toggle"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={() => setOpen((v) => !v)}
        // `.pf-tab`, not `.pf-btn`: the button classes are forced to
        // `border-radius: var(--radius-pill) !important` (app-layer.css:194-197), which an
        // inline radius cannot outrank — MEMB-01-04's 28px `⋯` trigger took `.pf-btn` and
        // shipped as a circle, proving it empirically. SettingsView.tsx:59-68 is the same
        // choice for the same reason and is the app's only other `.pf-tab`. `.pf-tab`
        // itself carries a colour transition and nothing else, so border, background and
        // padding are all reset here.
        className="pf-tab"
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 8,
          border: 0,
          background: 'transparent',
          padding: 0,
          cursor: 'pointer',
          fontFamily: 'var(--font-sans)',
          fontSize: 13.5,
          fontWeight: 600,
          color: 'var(--fg-1)',
        }}
      >
        {MATRIX_HEADING}
        {/* Sidebar.tsx:156's rotation, verbatim — including the explicit `rotate(0deg)`,
            which is what makes the chevron animate on the way back up as well. No new
            glyph: `chevDownGlyph` already exists and `[one-new-glyph]` was spent on
            `moreGlyph` in MEMB-01-04. */}
        <span style={{ display: 'inline-flex', flex: 'none', color: 'var(--fg-3)', transform: open ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 160ms' }}>
          {chevDownGlyph}
        </span>
      </button>

      {open && (
        <div
          id={bodyId}
          data-testid="role-matrix"
          style={{ marginTop: 10, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}
        >
          {/* A real <table>, and the one block on this screen that is not a CSS grid.
              MembersTable.tsx:3-6 states the house rule — grid, not <table> — and scopes
              it to "a screen's own layout", carving out ViolationsTable as "a shared
              component embedded inside screens". This is that second kind of thing: a
              small embedded reference matrix, not the screen's layout. And unlike a
              roster it is genuinely two-dimensional — a cell means nothing without BOTH
              its row (the capability) and its column (the role), which is exactly what
              `scope="row"`/`scope="col"` encode and what a grid of <span>s throws away.
              Picked, not blended: the roster above stays a grid. */}
          <table aria-labelledby={headingId} style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ background: 'var(--bg-1)' }}>
                {/* The corner cell heads neither a row nor a column, so it takes no
                    `scope` and no label — naming it would be inventing copy §6 has not
                    got. */}
                <th style={{ ...HEAD_CELL, textAlign: 'left' }} />
                {/* Column order is ACCESS_ROLES order by construction — Admin · Preparer ·
                    Reviewer — rather than a second list here that could drift from it. */}
                {ACCESS_ROLES.map((r) => (
                  <th key={r.id} scope="col" className="label" style={{ ...HEAD_CELL, textAlign: 'center', width: 104 }}>
                    {r.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {CAPABILITY_ROWS.map((row) => (
                <tr key={row.label}>
                  {/* Rendered EXACTLY as stored, lowercase — §6's own casing. AC#1 says
                      the rows are §6 verbatim; re-casing them would also be a derivation
                      living inside a component, which §15.8 bars, and a CSS
                      `text-transform` would corrupt `ERP` and `FIRS/MBS`. */}
                  <th scope="row" style={{ ...BODY_CELL, textAlign: 'left', fontWeight: 400, fontSize: 13, color: 'var(--fg-2)' }}>
                    {row.label}
                  </th>
                  {ACCESS_ROLES.map((r) => {
                    const allowed = row[r.id]
                    return (
                      <td key={r.id} style={{ ...BODY_CELL, textAlign: 'center' }}>
                        {/* Teal tick / muted cross, NOT the green/red pair CreateResults
                            uses. app-layer.css:71-73 assigns the palette meaning "teal =
                            pass, destructive = failure", and a Preparer who cannot approve
                            is not a failure — it is the role working as designed. */}
                        <span style={{ display: 'inline-flex', color: allowed ? 'var(--action)' : 'var(--fg-4)' }}>
                          {allowed ? tickGlyph11 : crossGlyph}
                        </span>
                        <span style={SR_ONLY}>{allowed ? 'Yes' : 'No'}</span>
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </table>

          {/* Inside the revealed body, under the grid — the MoreMenu note idiom
              (MemberParts.tsx:308-312). No `borderTop`: the last row of the table already
              draws that hairline, and a second one on top of it reads as a 2px rule. */}
          <div style={{ padding: '11px 16px 13px', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>{CAPABILITY_FOOTNOTE}</div>
        </div>
      )}
    </div>
  )
}

/**
 * §6's firm-only `Client users` placeholder — "it exists to keep the open question
 * visible". Gated by the caller, which renders NOTHING in in-house mode.
 *
 * A static `<div>` with no interactive element: no `<button>`, no `onClick`, no
 * `disabled`, no `aria-disabled`. AC#3 names all three, but they do not compose — a card
 * is not a form control, so `disabled` on it styles nothing and is read by nothing, and
 * the only way to honour it literally is a real `<button disabled>`, which yields a
 * permanently dead control that reads as broken and drops its own explanation out of the
 * tab order. Inertness here comes from there being nothing to click.
 *
 * That also means the four-layer disabled treatment (MoreMenu, from InvoiceDetail's
 * `[revalidate-visibility]`) does not apply: it exists to explain why a control the user
 * expects to click is refusing, and this card refuses nothing. What survives is its layer
 * (3) alone — the visible `NOT BUILT` marker, which is the required explanation rather
 * than decoration, and the only layer a screenshot can see.
 *
 * Muted throughout, over the dashed placeholder ground the app already uses for
 * not-yet-there content (CustomersView.tsx:134): it must read as a note about the roadmap,
 * not as a feature that failed to load.
 */
export function ClientUsersCard() {
  return (
    <div
      data-testid="client-users-card"
      style={{ marginTop: 16, maxWidth: BLOCK_WIDTH, background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', padding: '14px 16px' }}
    >
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, marginBottom: 5 }}>
        <span style={{ fontSize: 13.5, fontWeight: 600, color: 'var(--fg-3)' }}>Client users</span>
        {/* CreateUpload.tsx:164-166's marker, the app's idiom for "this is not the real
            thing yet" — mono, 11px, muted, on the title row. */}
        <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>
          NOT BUILT
        </span>
      </div>
      <p style={{ margin: 0, fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>{CLIENT_USERS_COPY}</p>
    </div>
  )
}
