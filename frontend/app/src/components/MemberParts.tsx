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

import { moreGlyph, searchGlyph, tickGlyph11 } from '../glyphs'
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

/** A native radio painted as the prototype's ring with a 7px dot when checked. */
const radioPaint = (checked: boolean): CSSProperties => ({
  appearance: 'none',
  WebkitAppearance: 'none',
  flex: 'none',
  width: 15,
  height: 15,
  boxSizing: 'border-box',
  borderRadius: '50%',
  border: `1.5px solid ${checked ? 'var(--action)' : 'var(--line-3)'}`,
  background: checked ? 'radial-gradient(circle, var(--action) 0 3.5px, transparent 4px)' : 'transparent',
})

// ---------------------------------------------------------------------------
// Initials chip
// ---------------------------------------------------------------------------

// The PERSON avatar (a dark circle), not the company one (a rounded rect in --action-tint),
// and it matches Sidebar.tsx's footer avatar: the `isYou` row renders the same human.
const CHIP_TONE: Record<MemberStatus, { background: string; color: string; border: string }> = {
  // The transparent border keeps all three variants the same box.
  active: { background: 'var(--slate-800)', color: 'var(--primary-foreground)', border: '1px solid transparent' },
  // Dashed over a transparent ground, never over a fill.
  invited: { background: 'transparent', color: 'var(--fg-3)', border: '1px dashed var(--line-3)' },
  // Double-encoded with the SUSPENDED pill on purpose.
  suspended: { background: 'var(--status-red-bg)', color: 'var(--status-red-text)', border: '1px solid var(--status-red-border)' },
}

/** `aria-hidden`: the name it abbreviates is always rendered beside it. */
export function InitialsChip({ initials, status, size = 30, fontSize, ring = false }: {
  initials: string
  status: MemberStatus
  size?: number
  fontSize?: number
  /** Overlapped stacks: a 2px card-ground border keeps neighbouring circles distinct. */
  ring?: boolean
}) {
  const tone = CHIP_TONE[status]
  return (
    <span
      aria-hidden="true"
      style={{
        flex: 'none',
        width: size,
        height: size,
        boxSizing: 'border-box',
        borderRadius: '50%',
        display: 'grid',
        placeItems: 'center',
        fontSize: fontSize ?? (size >= 40 ? 13 : size <= 26 ? 9 : 10.5),
        fontWeight: 700,
        ...tone,
        ...(ring && status !== 'invited' ? { border: '2px solid var(--bg-2)' } : null),
      }}
    >
      {initials}
    </span>
  )
}

// ---------------------------------------------------------------------------
// Status pill
// ---------------------------------------------------------------------------

// Active is MUTED, not green: an active member is the baseline, not a pass verdict, and a column of
// saturated pills would out-shout the exceptions it exists to surface.
const STATUS_TONE: Record<MemberStatus, { bg: string; border: string; text: string; label: string }> = {
  active: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'ACTIVE' },
  invited: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'INVITED' },
  suspended: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'SUSPENDED' },
}

/** `compact` is the drawer header's 2px pill; the table's is 3px. */
export function MemberStatusPill({ status, compact = false }: { status: MemberStatus; compact?: boolean }) {
  const tone = STATUS_TONE[status]
  return (
    <span
      className="mono"
      style={{
        flex: 'none',
        fontSize: 8.5,
        fontWeight: 600,
        letterSpacing: '0.06em',
        color: tone.text,
        background: tone.bg,
        border: `1px solid ${tone.border}`,
        borderRadius: 4,
        padding: compact ? '2px 8px' : '3px 8px',
      }}
    >
      {tone.label}
    </span>
  )
}

/** §6's "small YOU chip". */
export function YouChip() {
  return (
    <span
      className="mono"
      style={{ flex: 'none', fontSize: 8.5, fontWeight: 700, letterSpacing: '0.06em', background: 'var(--action-tint)', color: 'var(--action)', border: '1px solid var(--action)', borderRadius: 4, padding: '1px 5px' }}
    >
      YOU
    </span>
  )
}

// ---------------------------------------------------------------------------
// Amber note
// ---------------------------------------------------------------------------

/** The amber banner: the unassigned-roles notice and the drawer's suspended-in-steps note. No icon. */
export function AmberNote({ children, testId, style }: { children: ReactNode; testId?: string; style?: CSSProperties }) {
  return (
    <div
      data-testid={testId}
      style={{
        padding: '12px 14px',
        borderRadius: 'var(--radius-md)',
        background: 'var(--status-amber-bg)',
        border: '1px solid var(--status-amber-border)',
        fontSize: 13,
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
              // stops reacting to the pointer), not its form. `.pf-upcard:hover` sets
              // `border-color` with `!important` and a React style object
              // cannot emit `!important`, so unlike the unguarded
              // `.pf-menu-item:hover` this one cannot be outranked inline and a dead card
              // would still light up.
              className={disabled ? undefined : 'pf-upcard'}
              style={{
                display: 'flex',
                alignItems: 'flex-start',
                gap: 10,
                padding: '11px 13px',
                borderRadius: 'var(--radius-md)',
                border: `1px solid ${sel ? 'var(--action)' : 'var(--line-2)'}`,
                background: sel ? 'var(--action-tint)' : 'var(--bg-1)',
                cursor: 'pointer',
                ...(disabled ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null),
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
                style={{ ...radioPaint(sel), margin: '2px 0 0' }}
              />
              <span style={{ minWidth: 0 }}>
                <span style={{ display: 'block', fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>{r.label}</span>
                <span style={{ display: 'block', fontSize: 11.5, lineHeight: 1.5, marginTop: 2, color: 'var(--fg-3)' }}>
                  {r.description}
                </span>
              </span>
            </label>
          )
        })}
      </div>
      {note && (
        <div id={noteId} style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
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
      <div style={{ display: 'flex', gap: 8 }}>
        {(['all', 'selected'] as const).map((s) => (
          <label
            key={s}
            data-testid={`${idPrefix}-scope-${s}`}
            style={{
              flex: 1,
              display: 'flex',
              alignItems: 'center',
              gap: 9,
              padding: '10px 12px',
              borderRadius: 'var(--radius-md)',
              border: `1px solid ${scope === s ? 'var(--action)' : 'var(--line-2)'}`,
              background: scope === s ? 'var(--action-tint)' : 'var(--bg-1)',
              fontSize: 13,
              fontWeight: 500,
              cursor: 'pointer',
            }}
          >
            <input
              type="radio"
              name={`${idPrefix}-scope`}
              value={s}
              checked={scope === s}
              // Toggling back to `All clients` KEEPS the ticked set — `ids` is carried
              // through untouched, so switching back re-emits exactly what was ticked.
              onChange={() => pick(s, ids)}
              style={{ ...radioPaint(scope === s), margin: 0 }}
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
      width={260}
      background="var(--bg-2)"
      marginBottom={marginBottom}
    />
  )
}

/**
 * §4's workflow-role picker: a wrapped row of toggle pills, one per role, ticked when held,
 * assigning and unassigning immediately. `.pf-btn`, as the prototype draws it, so the cascade
 * forces the 7px radius.
 *
 * NOT `RoleCards`: those are a three-way EXCLUSIVE choice with real radios. The test IDs are
 * `-wfrole-` so they cannot collide with `-role-` in the same drawer.
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
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
      {roles.map((r) => {
        const on = held.includes(r.key)
        return (
          <button
            key={r.key}
            type="button"
            data-testid={`${idPrefix}-wfrole-${r.key}`}
            aria-pressed={on}
            onClick={() => onToggle(r.key)}
            className="pf-btn"
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 6,
              height: 32,
              padding: '0 13px',
              cursor: 'pointer',
              fontFamily: 'var(--font-sans)',
              fontSize: 12.5,
              fontWeight: 500,
              border: `1px solid ${on ? 'var(--action)' : 'var(--line-2)'}`,
              background: on ? 'var(--action-tint)' : 'var(--bg-2)',
              color: on ? 'var(--action)' : 'var(--fg-2)',
            }}
          >
            {on && tickGlyph11}
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
 * `position: relative` wrapper, an absolute panel in --bg-2 with a
 * --line-2 hairline, --radius-md, the same long soft shadow and `popIn 140ms`
 * (platform.css), and `.pf-menu-item` rows.
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
          // never fire together. Same rule, same reason as InvoicesList.tsx:599-603 and
          // RulesView.tsx:253-267.
          e.stopPropagation()
          if (open) onClose()
          else onOpen()
        }}
        className="pf-btn"
        // `.pf-btn` forces `border-radius` with `!important`; the radius is only visible
        // while open or hovered.
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          width: 28,
          height: 28,
          padding: 0,
          border: 0,
          cursor: 'pointer',
          background: 'transparent',
          color: 'var(--fg-3)',
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
            top: 'calc(100% + 4px)',
            right: 0,
            width: 280,
            zIndex: 60,
            background: 'var(--bg-2)',
            border: '1px solid var(--line-2)',
            borderRadius: 'var(--radius-md)',
            boxShadow: 'var(--shadow-card)',
            overflow: 'hidden',
            padding: '4px 0',
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
                padding: '8px 12px',
                fontFamily: 'var(--font-sans)',
                fontSize: 13,
                fontWeight: 400,
                background: 'transparent',
                color: item.danger ? 'var(--status-red-text)' : 'var(--fg-1)',
                cursor: 'pointer',
                // Nothing in the stylesheets paints `:disabled`: the inline recipe keeps the enabled
                // paint, dims it, and outranks the unguarded `.pf-menu-item:hover`. The reason
                // below is the layer a screenshot, a keyboard user and a text assertion can reach.
                ...(item.disabled ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null),
              }}
            >
              {item.label}
            </button>
          ))}
          {reasons.length > 0 && (
            <div style={{ borderTop: '1px solid var(--line-1)', marginTop: 4, padding: '8px 12px 6px', display: 'flex', flexDirection: 'column', gap: 6 }}>
              {reasons.map((reason) => (
                <span key={reason} id={reasonId(reason)} data-testid="member-menu-reason" style={{ fontSize: 11, lineHeight: 1.45, color: 'var(--fg-3)' }}>
                  {reason}
                </span>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/** The Members and Roles search box; `.pf-chipbox` rings the box once instead of the inner input. */
export function SearchBox({ value, onChange, placeholder, label, testId }: {
  value: string
  onChange: (v: string) => void
  placeholder: string
  label: string
  testId?: string
}) {
  return (
    <div className="pf-chipbox" style={{ display: 'flex', alignItems: 'center', gap: 8, width: 300, height: 36, padding: '0 12px', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)' }}>
      <span aria-hidden="true" style={{ flex: 'none', display: 'inline-flex', color: 'var(--fg-3)' }}>
        {searchGlyph}
      </span>
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={label}
        data-testid={testId}
        style={{ flex: 1, minWidth: 0, border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)' }}
      />
    </div>
  )
}
