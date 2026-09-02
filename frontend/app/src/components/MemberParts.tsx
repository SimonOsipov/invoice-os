// Settings › Members — the shared row atoms.
//
// No derivation and no copy is DEFINED in this file: vitest is `environment: node` in this
// project, so a fact computed — or a sentence written — inside a component is a fact no test
// can reach. Everything these atoms render is either a prop or a call into lib/members.ts,
// where the specs are (§15.8). `ClientAccessPicker` is the one that holds state rather than
// taking it all as props, and its docblock says why.
//
// The roster table, the member drawer and the role modal share these. Several of the
// drawer's — `RoleCards`, `ClientAccessPicker`, `DepartmentField` — now mount read-only,
// so each takes its writer callback as OPTIONAL rather than growing a `disabled` prop the
// other call sites would never set.

import { useId, useRef, useState, type CSSProperties, type ReactNode } from 'react'

import { moreGlyph } from '../glyphs'
import { useDismiss } from '../lib/useDismiss'
import {
  ABSENT_LABEL,
  ACCESS_ROLES,
  clientSelectionCount,
  DEPARTMENTS,
  filterClientRoster,
  needsClientPick,
  NO_CLIENT_MATCH,
  NO_CLIENTS_NOTE,
  type AccessRole,
  type Department,
  type MemberStatus,
} from '../lib/members'
import type { Role } from '../lib/roles'
import { WfSelect, type WfOption } from './WorkflowParts'

// ---------------------------------------------------------------------------
// Initials chip
// ---------------------------------------------------------------------------

// The PERSON avatar, not the company one. Both exist in this app and the difference is a
// deliberate signal, not drift: a COMPANY is a rounded rect in --action-tint on --action
// at weight 700 (Sidebar.tsx:147/:173/:192, ClientsView.tsx:160, CustomersView.tsx:110,
// SignIn.tsx:73), and a PERSON is a dark circle in --slate-800 on --text-on-dark at
// weight 600 — Sidebar.tsx:251, until now the only person avatar in the product.
//
// A members table is people, so it takes the person chip. The `isYou` row settles it: it
// renders the same human the sidebar footer renders two inches away, and the seed sources
// that row from APP_PERSONAS (members.ts:128-131) precisely so the two can never drift.
// Giving it the company chip here would re-open the one gap that was deliberately closed.
//
// §15.3's plan named --action-tint/--action for the default chip; that is the company
// family. Picked, not blended.
const CHIP_TONE: Record<MemberStatus, { background: string; color: string; border: string }> = {
  // Sidebar.tsx:251, verbatim. The transparent border keeps all three variants the same
  // box, since `box-sizing: border-box` means the dashed variant's border eats 2px.
  active: { background: 'var(--slate-800)', color: 'var(--text-on-dark)', border: '1px solid transparent' },
  // §10.1. The app's dashed idiom is always `1px dashed var(--line-3)` over a transparent
  // ground — empty-state cards (CustomersView.tsx:134), add chips (InvoiceDetail.tsx:934),
  // dropzones — and never over a fill, so the fill goes with it.
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

/** §6's "small YOU chip" — the micro-badge idiom (RulesView.tsx:163, Sidebar.tsx:237). */
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
 * One amber banner shape for both of the story's warnings — the unassigned-roles notice
 * above the table and §10.4's suspended-in-steps row warning. Treatment copied
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
// Access-role cards
// ---------------------------------------------------------------------------

/**
 * §7's access-role picker — three radio CARDS, each carrying an `ACCESS_ROLES` label and
 * description. That copy is §3 verbatim and already pinned by T1.39, labels and descriptions
 * alike, so nothing is re-pinned here.
 *
 * NATIVE RADIOS, and the app's first. `frontend/app/src` contains no `type="radio"`, no
 * `role="radio"` and no `radiogroup`; the ARIA-on-button idiom it does have (RulesView.tsx:259,
 * WorkflowParts.tsx:295-299) is for TOGGLES, not for a three-way exclusive choice, and would need
 * hand-rolled arrow-key handling. A real radio group gives roving focus, form semantics and
 * `:checked` for free. The card LOOK follows the app's selected-card idiom
 * (CreateUpload.tsx:178-198); its ARIA does not — that one carries no selected state at all,
 * and shipping that gap into the control that sets someone's permissions is not a precedent
 * worth honouring.
 *
 * Unselected cards sit on --bg-1 rather than CreateUpload's --bg-2. Same rule, different
 * ground: a card must be one step off the surface behind it, and this one is mounted on a
 * --bg-2 modal panel where --bg-2 would be invisible. It is the pair `WfSelect` already uses
 * for a control inside a panel (WorkflowParts.tsx:235).
 */
export function RoleCards({ value, onChange, disabledIds, note, noteId: noteIdProp, idPrefix }: {
  value: AccessRole
  /** Absent when every card is disabled — there is nothing left that can emit. */
  onChange?: (role: AccessRole) => void
  /** The roles this caller may not switch to. */
  disabledIds?: readonly AccessRole[]
  /**
   * Why those cards are disabled, rendered as visible text beneath them. Set it whenever any
   * card is disabled — it is the only layer a screenshot, a keyboard user and a text
   * assertion can all reach.
   */
  note?: string
  /** Lets a caller point its own `aria-describedby` at the note. Defaults to a local id. */
  noteId?: string
  /**
   * Names the radio group, so two instances can be mounted at once without one stealing the
   * other's selection. Deliberately a caller-supplied string and not `useId()`: React emits
   * `:r3:`, which needs escaping in a CSS selector and moves with the render tree, and these
   * ids are the handle the browser-only gate uses to find the cards.
   */
  idPrefix: string
}) {
  const localNoteId = useId()
  const noteId = noteIdProp ?? localNoteId

  return (
    <div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {ACCESS_ROLES.map((r) => {
          const sel = value === r.id
          const disabled = disabledIds?.includes(r.id) ?? false
          return (
            <label
              key={r.id}
              data-testid={`${idPrefix}-role-${r.id}`}
              // Layer (2) of MoreMenu's four-layer disabled treatment, by CLASS OMISSION
              // rather than by an inline override — the idiom's PURPOSE (a disabled control
              // stops reacting to the pointer), not its form. `.pf-upcard:hover` is
              // `border-color: var(--action) !important` (platform.css:134-136) and a React
              // style object cannot emit `!important`, so unlike the unguarded
              // `.pf-menu-item:hover` this one cannot be outranked inline and a dead card
              // would still light up. Omitting the class costs nothing else: `.pf-upcard`
              // carries only that hover, a transition and a font-family — every card's
              // padding, radius, border and background is inline, here and at both other
              // call sites. Adding a `[aria-disabled]` rule to the shared stylesheet would
              // fix it globally for a state only -07 reaches, which is the edit
              // MemberRoleMatrix.tsx:32-33 already declines to make. Do not restore it.
              className={disabled ? undefined : 'pf-upcard'}
              style={{
                display: 'flex',
                alignItems: 'flex-start',
                gap: 10,
                padding: '11px 13px',
                borderRadius: 'var(--radius-md)',
                border: `1px solid ${sel ? 'var(--action)' : 'var(--line-2)'}`,
                background: sel ? 'var(--action-tint)' : 'var(--bg-1)',
                cursor: disabled ? 'not-allowed' : 'pointer',
              }}
            >
              <input
                type="radio"
                name={`${idPrefix}-role`}
                value={r.id}
                checked={sel}
                disabled={disabled}
                onChange={() => onChange?.(r.id)}
                title={disabled ? note : undefined}
                aria-describedby={disabled && note ? noteId : undefined}
                style={{ flex: 'none', margin: '2px 0 0' }}
              />
              <span style={{ minWidth: 0 }}>
                <span style={{ display: 'block', fontSize: 13, fontWeight: 600, color: disabled ? 'var(--fg-4)' : 'var(--fg-1)' }}>{r.label}</span>
                <span style={{ display: 'block', fontSize: 11.5, lineHeight: 1.45, marginTop: 2, color: disabled ? 'var(--fg-4)' : 'var(--fg-3)' }}>
                  {r.description}
                </span>
              </span>
            </label>
          )
        })}
      </div>
      {note && (
        <div id={noteId} style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
          {note}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Client access picker (FIRM) — §7's scope radios + searchable multi-select
// ---------------------------------------------------------------------------

/**
 * Extracted from the invite modal when the member drawer became its second call site. Not
 * speculative abstraction: this control carries five decisions a second copy would have to
 * re-make and could silently get wrong —
 *
 *   1. toggling back to `All clients` KEEPS the ticked set (a mis-click must not destroy a
 *      selection assembled one checkbox at a time);
 *   2. filtering never unticks — the search narrows what is SHOWN and `ids` is untouched;
 *   3. the running count's denominator is the ROSTER, never the filtered length;
 *   4. `Selected clients` with nothing ticked is representable but not grantable;
 *   5. the `.pf-row` checkbox rows.
 *
 * (1) and (2) were on that subtask's own QA gate list, so duplicating the JSX would
 * duplicate both of them out of coverage. The derivations themselves already live in
 * lib/members.ts with specs — only the markup was ever at stake here.
 *
 * `scope` and the ticked `ids` are OWN state, seeded once from `value`, and that split is
 * load-bearing: `value` alone cannot be the source of truth, because collapsing "scope is
 * all" and "nothing is ticked" into one representation is what destroys the set on a
 * mis-click. What it emits is the union the caller stores — `'all'`, or a FRESH array, so
 * no caller ever receives this control's own state to alias.
 *
 * THE CONTRACT THAT FOLLOWS FROM THAT: `value` is read ONCE, on mount, and every later
 * change to it is ignored. To point this control at a different subject you must REMOUNT
 * it — `key` on this element, or on whatever wraps it (MembersView keys the whole drawer).
 * Passing a new `value` to a live instance silently keeps the old subject's ticked set.
 */
export function ClientAccessPicker({ value, onChange, idPrefix }: {
  value: 'all' | readonly number[]
  /**
   * Always a new array in the `selected` case — no caller ever receives this control's own
   * state. Absent when the caller mounts this read-only: nothing can emit through a
   * `<fieldset disabled>`.
   */
  onChange?: (next: 'all' | number[]) => void
  /** Names the scope radio group and every `data-testid` here, exactly as `RoleCards` does. */
  idPrefix: string
}) {
  const [scope, setScope] = useState<'all' | 'selected'>(value === 'all' ? 'all' : 'selected')
  const [ids, setIds] = useState<number[]>(() => (value === 'all' ? [] : value.slice()))
  const [query, setQuery] = useState('')

  const shown = filterClientRoster(query)
  const emptyPick = needsClientPick(scope === 'all' ? 'all' : ids)

  // The ONE writer, so the emitted value can never disagree with what the checkboxes show.
  function pick(nextScope: 'all' | 'selected', nextIds: number[]) {
    setScope(nextScope)
    setIds(nextIds)
    onChange?.(nextScope === 'all' ? 'all' : [...nextIds])
  }

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
        {(['all', 'selected'] as const).map((s) => (
          <label key={s} data-testid={`${idPrefix}-scope-${s}`} style={{ display: 'inline-flex', alignItems: 'center', gap: 7, fontSize: 13, cursor: 'pointer' }}>
            <input
              type="radio"
              name={`${idPrefix}-scope`}
              value={s}
              checked={scope === s}
              // Toggling back to `All clients` KEEPS the ticked set — `ids` is carried
              // through untouched, so switching back re-emits exactly what was ticked.
              onChange={() => pick(s, ids)}
              style={{ flex: 'none' }}
            />
            {s === 'all' ? 'All clients' : 'Selected clients'}
          </label>
        ))}
      </div>

      {scope === 'selected' && (
        <div style={{ marginTop: 10, border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-1)', padding: 10 }}>
          <input
            type="text"
            className="pf-input"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search clients"
            aria-label="Search clients"
            data-testid={`${idPrefix}-client-search`}
            style={{ height: 34, fontSize: 13, marginBottom: 8 }}
          />
          {shown.length === 0 ? (
            <div data-testid={`${idPrefix}-client-empty`} style={{ padding: '8px 10px', fontSize: 12.5, color: 'var(--fg-3)' }}>
              {NO_CLIENT_MATCH}
            </div>
          ) : (
            shown.map((c) => (
              <label
                key={c.id}
                className="pf-row"
                data-testid={`${idPrefix}-client-row`}
                style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '6px 10px', borderRadius: 'var(--radius-md)', fontSize: 13, color: 'var(--fg-1)' }}
              >
                <input
                  type="checkbox"
                  checked={ids.includes(c.id)}
                  // `toggleSelection` (invoices.ts:654) is the nearest shipped helper and is
                  // typed `string[]`; CLIENT_ROSTER ids are numbers, so it cannot be reused.
                  // Filtering never unticks: the search narrows `shown`, not `ids`.
                  onChange={() => pick('selected', ids.includes(c.id) ? ids.filter((x) => x !== c.id) : [...ids, c.id])}
                  style={{ flex: 'none' }}
                />
                {c.name}
              </label>
            ))
          )}
          <div data-testid={`${idPrefix}-client-count`} style={{ marginTop: 8, paddingTop: 8, borderTop: '1px solid var(--line-1)', fontSize: 12, color: 'var(--fg-3)' }}>
            {clientSelectionCount(ids.length)}
            {emptyPick && <span style={{ color: 'var(--status-amber-text)' }}> · {NO_CLIENTS_NOTE}</span>}
          </div>
        </div>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// Department (IN-HOUSE) and the workflow-role pill toggles (BOTH MODES)
// ---------------------------------------------------------------------------

const DEPARTMENT_OPTIONS: WfOption[] = DEPARTMENTS.map((d) => ({ value: d, label: d }))

// A membership row carries no department, so `null` is a reachable value and the control
// has to render SOMETHING. One option holding the em dash absence renders everywhere else
// on this tab — never an empty box, which reads as a load failure.
const ABSENT_OPTIONS: WfOption[] = [{ value: '', label: ABSENT_LABEL }]

/**
 * What is left of `PositionFields` once Axis B became a workflow role. It SPLIT rather than
 * growing a mode branch: the pills below render in both modes and this select renders in
 * neither firm surface, so one atom would have had to know which mode it was in.
 *
 * Fully controlled — there is no internal state a mis-click could destroy, so the caller owns
 * the value outright. `onDepartment` is absent when the caller mounts it read-only.
 */
export function DepartmentField({ department, onDepartment, marginBottom }: {
  department: Department | null
  onDepartment?: (next: Department) => void
  marginBottom?: number
}) {
  return (
    <WfSelect
      label="Department"
      value={department ?? ''}
      options={department == null ? ABSENT_OPTIONS : DEPARTMENT_OPTIONS}
      onChange={(v) => onDepartment?.(v as Department)}
      width="100%"
      marginBottom={marginBottom}
    />
  )
}

/**
 * §4's workflow-role picker — a wrapped row of pill toggles, one per role, ticked when held,
 * assigning and unassigning immediately. The ReviewInvoicesTab filter-pill idiom verbatim
 * (ReviewInvoicesTab.tsx:363-380): `.pf-chip`, `aria-pressed`, teal fill when on. No inline
 * `borderRadius` — `.pf-chip` is `border-radius: var(--radius-pill) !important`
 * (app-layer.css:275), so a radius here would be a declaration that never applies.
 *
 * NOT `RoleCards`: those are a three-way EXCLUSIVE choice over a closed union and carry real
 * radios. This is a multi-select over a list the user can edit, which is why the testids are
 * `-wfrole-` and cannot collide with `-role-` in the same drawer.
 */
export function WorkflowRolePills({ roles, held, onToggle, idPrefix }: {
  roles: readonly Role[]
  /** Keys this person holds — `rolesOfMember`'s answer, never re-derived here. */
  held: readonly string[]
  onToggle: (key: string) => void
  /** Names each pill's `data-testid`, exactly as `RoleCards` does. */
  idPrefix: string
}) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 7 }}>
      {roles.map((r) => {
        const on = held.includes(r.key)
        return (
          <button
            key={r.key}
            type="button"
            data-testid={`${idPrefix}-wfrole-${r.key}`}
            aria-pressed={on}
            onClick={() => onToggle(r.key)}
            className="pf-chip"
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              height: 28,
              padding: '0 12px',
              fontFamily: 'var(--font-sans)',
              fontSize: 12.5,
              fontWeight: 500,
              border: `1px solid ${on ? 'var(--action)' : 'var(--line-2)'}`,
              background: on ? 'var(--action)' : 'var(--bg-1)',
              color: on ? 'var(--text-on-dark)' : 'var(--fg-2)',
            }}
          >
            {r.title}
          </button>
        )
      })}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Row overflow menu
// ---------------------------------------------------------------------------

// The app's popover shadow, and its second use — the first is the Sidebar company
// switcher (Sidebar.tsx:155). Off-system already: the three shipped shadow tokens
// (--shadow-elegant / -soft / -card, tokens/colors.css:62-64) are a hero panel, a floating
// chip and a resting card, none of them this. Named here rather than inlined a second time
// so the duplication is visible, and reused verbatim rather than swapped for
// --shadow-elegant so the app's two popovers cannot drift apart. Promoting it to a real
// token means editing the shared stylesheet and the design-system doc — flagged, not done
// inside a table subtask.
const POPOVER_SHADOW = '0 16px 40px -16px oklch(20% .02 210 / 0.28)'

export type MenuAction = {
  label: string
  /** Absent on a disabled item; selecting anything else closes the menu after it runs. */
  onSelect?: () => void
  disabled?: boolean
  /**
   * Why this item is disabled — layer (3) of the treatment below, rendered as a visible
   * note. PER ITEM, because one row can disable Suspend for the last-admin lock and Remove
   * for a missing endpoint at the same time, and a single menu-wide note cannot say both.
   */
  reason?: string
  /** Destructive wording — Remove / Revoke invite. */
  danger?: boolean
}

/**
 * The per-row `⋯` menu. The app had no row menu before this, so the anatomy is taken from
 * the only popover it does have, the Sidebar company switcher (Sidebar.tsx:139-186): a
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
export function MoreMenu({ open, onOpen, onClose, label, items }: {
  open: boolean
  onOpen: () => void
  /** Must be stable — it is a `useDismiss` dependency. */
  onClose: () => void
  /** Names the row, for the trigger's accessible name. */
  label: string
  items: MenuAction[]
}) {
  // On the WRAPPER, not the panel: with the ref on the panel alone, clicking the trigger of
  // an open menu would dismiss it on mousedown and re-open it on click.
  const wrapRef = useRef<HTMLDivElement>(null)
  const noteId = useId()
  useDismiss(open, onClose, wrapRef)

  // One note per DISTINCT reason, in item order, so a row disabled for two different
  // reasons states both and each item points at its own.
  const reasons = [...new Set(items.filter((i) => i.disabled && i.reason).map((i) => i.reason as string))]
  const reasonId = (reason: string) => `${noteId}-${reasons.indexOf(reason)}`

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
        // deliberately: `.pf-signout` (Sidebar.tsx:271-279) is the same 28px transparent
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
              title={item.disabled ? item.reason : undefined}
              aria-describedby={item.disabled && item.reason ? reasonId(item.reason) : undefined}
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
          {reasons.map((reason) => (
            <div
              key={reason}
              id={reasonId(reason)}
              data-testid="member-menu-reason"
              style={{ padding: '8px 12px 10px', borderTop: '1px solid var(--line-1)', fontSize: 11, lineHeight: 1.45, color: 'var(--fg-3)' }}
            >
              {reason}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
