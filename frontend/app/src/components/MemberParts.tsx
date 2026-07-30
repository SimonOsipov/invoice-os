// Settings › Members — the shared row atoms.
//
// Everything here takes what it renders as a prop. No derivation lives in this file:
// vitest is `environment: node` in this project, so a fact computed inside a component is
// a fact no test can reach — the members derivations all sit in lib/members.ts with specs
// (§15.8).
//
// MEMB-01-06's invite modal and MEMB-01-07's drawer reuse `InitialsChip`,
// `MemberStatusPill` and `useDismiss` from here; `RoleCards` joins them in -06.

import { useId, useRef, type CSSProperties, type ReactNode } from 'react'

import { moreGlyph } from '../glyphs'
import { useDismiss } from '../lib/useDismiss'
import type { MemberStatus } from '../lib/members'

// ---------------------------------------------------------------------------
// Initials chip
// ---------------------------------------------------------------------------

// The PERSON avatar, not the company one. Both exist in this app and the difference is a
// deliberate signal, not drift: a COMPANY is a rounded rect in --action-tint on --action
// at weight 700 (Sidebar.tsx:151/:177/:196, ClientsView.tsx:160, CustomersView.tsx:110,
// SignIn.tsx:73), and a PERSON is a dark circle in --slate-800 on --text-on-dark at
// weight 600 — Sidebar.tsx:255, until now the only person avatar in the product.
//
// A members table is people, so it takes the person chip. The `isYou` row settles it: it
// renders the same human the sidebar footer renders two inches away, and the seed sources
// that row from APP_PERSONAS (members.ts:128-131) precisely so the two can never drift.
// Giving it the company chip here would re-open the one gap that was deliberately closed.
//
// §15.3's plan named --action-tint/--action for the default chip; that is the company
// family. Picked, not blended.
const CHIP_TONE: Record<MemberStatus, { background: string; color: string; border: string }> = {
  // Sidebar.tsx:255, verbatim. The transparent border keeps all three variants the same
  // box, since `box-sizing: border-box` means the dashed variant's border eats 2px.
  active: { background: 'var(--slate-800)', color: 'var(--text-on-dark)', border: '1px solid transparent' },
  // §10.1. The app's dashed idiom is always `1px dashed var(--line-3)` over a transparent
  // ground — empty-state cards (CustomersView.tsx:134), add chips (ValidationView.tsx:148,
  // InvoiceDetail.tsx:934), dropzones — and never over a fill, so the fill goes with it.
  invited: { background: 'transparent', color: 'var(--fg-3)', border: '1px dashed var(--line-3)' },
  // §10.2's red tint. Double-encoded with the SUSPENDED pill on purpose: AC#2 spells out
  // chip AND pill for both non-active states, so the redundancy is the specification.
  suspended: { background: 'var(--status-red-bg)', color: 'var(--status-red-text)', border: '1px solid var(--status-red-border)' },
}

/** `aria-hidden`: the name it abbreviates is always rendered beside it. */
export function InitialsChip({ initials, status, size = 30 }: { initials: string; status: MemberStatus; size?: number }) {
  const tone = CHIP_TONE[status]
  return (
    <span
      aria-hidden="true"
      style={{
        flex: 'none',
        width: size,
        height: size,
        boxSizing: 'border-box',
        borderRadius: 99,
        display: 'grid',
        placeItems: 'center',
        fontSize: size >= 40 ? 14 : 11,
        fontWeight: 600,
        ...tone,
      }}
    >
      {initials}
    </span>
  )
}

// ---------------------------------------------------------------------------
// Status pill
// ---------------------------------------------------------------------------

// One more tone map over the app's single pill idiom — a {bg, border, text} record drawn
// as a 999-radius pill with a small solid dot in `text` (PolicyStatusPill,
// WorkflowParts.tsx:171-186; also InvoicesList.tsx:416-424, ViolationsTable.tsx:55-56,
// RulesView.tsx:31-34). Three distinct tones, no treatment reused (AC#2).
//
// Active keeps a pill rather than an empty cell — `Status` is a column, and every other
// table in this app fills its status column on every row, so a blank cell on thirteen of
// sixteen rows reads as missing data rather than as "the default".
//
// But MUTED, not green. Two reasons. The palette assigns meaning: "Teal = pass, amber =
// attention, destructive = failure" (app-layer.css:71-73), and an active member is not a
// pass verdict, they are the baseline — --status-muted-* is what the app already uses for
// a neutral state marker (DRAFT, SUPERSEDED). And thirteen saturated pills would out-shout
// the three exceptions the column exists to surface. §10's "active = the solid default".
const STATUS_TONE: Record<MemberStatus, { bg: string; border: string; text: string; label: string }> = {
  active: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'ACTIVE' },
  invited: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'INVITED' },
  suspended: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'SUSPENDED' },
}

export function MemberStatusPill({ status }: { status: MemberStatus }) {
  const tone = STATUS_TONE[status]
  return (
    <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 5, background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 999, padding: '2px 8px' }}>
      <span style={{ flex: 'none', width: 5, height: 5, borderRadius: 99, background: tone.text }} />
      <span className="mono" style={{ fontSize: 8.5, fontWeight: 600, color: tone.text, letterSpacing: '0.05em' }}>
        {tone.label}
      </span>
    </span>
  )
}

/** §6's "small YOU chip" — the micro-badge idiom (RulesView.tsx:163, Sidebar.tsx:241). */
export function YouChip() {
  return (
    <span
      className="mono"
      style={{ flex: 'none', fontSize: 9, fontWeight: 700, letterSpacing: '0.06em', background: 'var(--action-tint)', color: 'var(--action)', borderRadius: 99, padding: '1px 6px' }}
    >
      YOU
    </span>
  )
}

// ---------------------------------------------------------------------------
// Amber note
// ---------------------------------------------------------------------------

/**
 * One amber banner shape for both of the story's warnings — §6's unassigned-positions
 * notice above the table and §10.4's suspended-in-steps row warning. Treatment copied
 * from the app's shipped inline warning (InvoiceDetail.tsx:583-589). No icon, because
 * that one has none and a second, icon-bearing amber banner would read as a different
 * severity rather than the same one.
 */
export function AmberNote({ children, testId, style }: { children: ReactNode; testId?: string; style?: CSSProperties }) {
  return (
    <div
      data-testid={testId}
      style={{
        padding: '10px 12px',
        borderRadius: 'var(--radius-md)',
        background: 'var(--status-amber-bg)',
        border: '1px solid var(--status-amber-border)',
        fontSize: 12.5,
        lineHeight: 1.5,
        color: 'var(--status-amber-text)',
        ...style,
      }}
    >
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Row overflow menu
// ---------------------------------------------------------------------------

// The app's popover shadow, and its second use — the first is the Sidebar company
// switcher (Sidebar.tsx:159). Off-system already: the three shipped shadow tokens
// (--shadow-elegant / -soft / -card, tokens/colors.css:62-64) are a hero panel, a floating
// chip and a resting card, none of them this. Named here rather than inlined a second time
// so the duplication is visible, and reused verbatim rather than swapped for
// --shadow-elegant so the app's two popovers cannot drift apart. Promoting it to a real
// token means editing the shared stylesheet and the design-system doc — flagged, not done
// inside a table subtask.
const POPOVER_SHADOW = '0 16px 40px -16px oklch(20% .02 210 / 0.28)'

export type MenuAction = {
  label: string
  /** Absent = rendered now, wired by a later subtask; selecting it only closes the menu. */
  onSelect?: () => void
  disabled?: boolean
  /** Destructive wording — Remove / Revoke invite. */
  danger?: boolean
}

/**
 * The per-row `⋯` menu. The app had no row menu before this, so the anatomy is taken from
 * the only popover it does have, the Sidebar company switcher (Sidebar.tsx:143-190): a
 * `position: relative` wrapper, an absolute panel at `calc(100% + 6px)` in --bg-2 with a
 * --line-2 hairline, --radius-md, the same long soft shadow and `popIn 140ms`
 * (platform.css:30-39), and `.pf-menu-item` rows.
 *
 * Two deliberate departures from it. The panel is right-aligned with its own width rather
 * than stretched `left:0; right:0` to the trigger — a 28px trigger is not a menu width.
 * And it dismisses itself, which the switcher does not (see lib/useDismiss.ts).
 *
 * Controlled, not self-stating: the table owns `openMenuId`, so only one menu can be open
 * at a time and the table can make vertical room for whichever one it is (MENU_CLEARANCE
 * in MembersTable.tsx).
 */
export function MoreMenu({ open, onOpen, onClose, label, items, note }: {
  open: boolean
  onOpen: () => void
  /** Must be stable — it is a `useDismiss` dependency. */
  onClose: () => void
  /** Names the row, for the trigger's accessible name. */
  label: string
  items: MenuAction[]
  /**
   * §9's explanation, rendered as visible text beneath the items. Set it whenever any item
   * is disabled: it is layer (3) of the disabled treatment below, and the only layer a
   * screenshot can see.
   */
  note?: string
}) {
  // On the WRAPPER, not the panel: with the ref on the panel alone, clicking the trigger of
  // an open menu would dismiss it on mousedown and re-open it on click.
  const wrapRef = useRef<HTMLDivElement>(null)
  const noteId = useId()
  useDismiss(open, onClose, wrapRef)

  return (
    <div ref={wrapRef} style={{ position: 'relative', justifySelf: 'end' }}>
      <button
        type="button"
        data-testid="member-menu-trigger"
        aria-label={`Actions for ${label}`}
        aria-expanded={open}
        onClick={(e) => {
          // MEMB-01-07 gives the row itself a click that opens the drawer; the two must
          // never fire together. Same rule, same reason as InvoicesList.tsx:400-407 and
          // RulesView.tsx:253-267.
          e.stopPropagation()
          if (open) onClose()
          else onOpen()
        }}
        className="pf-btn"
        // `.pf-btn` is forced to `border-radius: var(--radius-pill) !important`
        // (app-layer.css:194-197), so this square icon button renders as a circle. Taken
        // deliberately: `.pf-signout` (Sidebar.tsx:275-283) is the same 28px transparent
        // circle already shipped, and the radius is only visible while open or hovered.
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          width: 28,
          height: 28,
          padding: 0,
          border: 0,
          cursor: 'pointer',
          background: open ? 'var(--bg-3)' : 'transparent',
          color: open ? 'var(--action)' : 'var(--fg-3)',
        }}
      >
        {moreGlyph}
      </button>
      {open && (
        <div
          data-testid="member-menu"
          onClick={(e) => e.stopPropagation()}
          style={{
            position: 'absolute',
            top: 'calc(100% + 6px)',
            right: 0,
            width: 196,
            zIndex: 60,
            background: 'var(--bg-2)',
            border: '1px solid var(--line-2)',
            borderRadius: 'var(--radius-md)',
            boxShadow: POPOVER_SHADOW,
            overflow: 'hidden',
            animation: 'popIn 140ms ease-out',
          }}
        >
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              disabled={item.disabled}
              title={item.disabled ? note : undefined}
              aria-describedby={item.disabled && note ? noteId : undefined}
              onClick={(e) => {
                e.stopPropagation()
                item.onSelect?.()
                onClose()
              }}
              className="pf-menu-item"
              style={{
                display: 'block',
                width: '100%',
                border: 0,
                textAlign: 'left',
                padding: '9px 12px',
                fontFamily: 'var(--font-sans)',
                fontSize: 13,
                fontWeight: 500,
                background: 'transparent',
                color: item.danger ? 'var(--status-red-text)' : 'var(--fg-1)',
                cursor: 'pointer',
                // A disabled control gets NOTHING for free in this codebase:
                // packages/design-tokens/*.css contains zero `:disabled` rules, and
                // `.pf-menu-item:hover` is unguarded in BOTH stylesheets
                // (platform.css:74-78, app-layer.css:272) — so a disabled row would still
                // light up under the pointer and read as clickable. Four layers, the
                // InvoiceDetail.tsx:406-444 ([revalidate-visibility]) treatment:
                // (1) the real `disabled` attribute above — genuinely unclickable;
                // (2) this inline swap, which outranks that unguarded :hover so the row
                //     stops reacting to the pointer. `transparent` rather than
                //     InvoiceDetail's --bg-3: in a menu, a grey fill IS the hover state,
                //     so filling a dead row would say the opposite of what it means;
                // (3) the visible `note` below — the only layer a screenshot, a keyboard
                //     user and a text assertion can all reach, since a disabled control is
                //     out of the tab order and `title` never fires on one in Chromium;
                // (4) title + aria-describedby, as ADDITIONS to (3), never the sole carrier.
                ...(item.disabled ? { background: 'transparent', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
              }}
            >
              {item.label}
            </button>
          ))}
          {note && (
            <div id={noteId} style={{ padding: '8px 12px 10px', borderTop: '1px solid var(--line-1)', fontSize: 11, lineHeight: 1.45, color: 'var(--fg-3)' }}>
              {note}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
