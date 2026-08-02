// Settings › Members — the roster table.
//
// A CSS grid, not a <table>: the screen idiom in this app is a `gridTemplateColumns`
// literal repeated on a head row and every body row (InvoicesList, ClientsView,
// CustomersView, RulesView). The one real <table>, ViolationsTable, is a shared component
// embedded inside screens rather than a screen's own layout.
//
// ONE column set differs per workspace mode: firm scopes a person to CLIENT COMPANIES,
// in-house places them in a DEPARTMENT. `Workflow roles` renders in BOTH — a role staffs
// people in either workspace now — so the two grids differ by that one column only.
//
// Nothing is derived here. Every value comes from lib/members.ts or lib/roles.ts — vitest
// is `environment: node` in this project, so a derivation written into a component is a
// derivation no test can reach (§15.8).

import { Fragment, useCallback, useState } from 'react'

import {
  accessRoleLabel,
  clientAccessLabel,
  clientAccessNames,
  isProtectedAdmin,
  lastActiveLabel,
  PROTECTED_ADMIN_NOTE,
  type Member,
} from '../lib/members'
import { rosterRoleCell, stepsForMember, stepsWarning, type Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import { AmberNote, InitialsChip, MemberStatusPill, MoreMenu, YouChip, type MenuAction } from './MemberParts'
import type { PlatformCtx } from '../types'

type Mode = PlatformCtx['mode']

// §15.7's grids, plus the two things a grid template does not state and this table needs:
// the gap and the row padding. Both are the house constants — gap 16, head '11px 18px',
// row '14px 18px' (InvoicesList.tsx:361/:392, ClientsView.tsx:140/:157).
const COLS: Record<Mode, string> = {
  firm: 'minmax(190px,1fr) 130px 130px 160px 120px 140px 44px',
  inhouse: 'minmax(190px,1fr) 120px 150px 160px 110px 130px 44px',
}

const HEADS: Record<Mode, string[]> = {
  // The trailing '' is the `⋯` column: at 44px no uppercase 10.5px label fits, and every
  // action column in the app is unlabelled.
  firm: ['Person', 'Access role', 'Client access', 'Workflow roles', 'Status', 'Last active', ''],
  inhouse: ['Person', 'Access role', 'Department', 'Workflow roles', 'Status', 'Last active', ''],
}

// The width the grid actually needs, per mode. RulesView.tsx:19-29's arithmetic redone for
// this table — RulesView's own 790 does not transfer, because its rationale is sitting
// beside a fixed 244px rail and this table has none.
//
//   firm    = 190 person floor + 724 fixed (130+130+160+120+140+44) + 6 gaps x 16 + 36 padding = 1046
//   inhouse = 190 person floor + 714 fixed (120+150+160+110+130+44) + 6 gaps x 16 + 36 padding = 1036
//
// Firm gained a seventh column with `Workflow roles` and is now the WIDER of the two; 160px
// is the in-house Approval-position column's own width, which already carried titles this
// long. "WORKFLOW ROLES" as a 10.5px uppercase `.label` measures ~102px, so no head truncates.
//
// The Settings content box is ~1116px at a 1440px viewport (1440 - 252 sidebar,
// Sidebar.tsx:132 - 72 page padding, SettingsView.tsx:47), which leaves the firm person
// column ~260px — above its 190px floor, but §15.7's stated ">=300px" headroom is not the
// real figure and must not be relied on. Both modes therefore overflow on any viewport below
// ~1420px, an ordinary laptop.
//
// Without a scroll container of its own that overflow would scroll the WHOLE Settings page
// sideways, dragging the h1 and the tab strip with it: App.tsx:800's `.pf-scroll` sets only
// `overflowY: 'auto'`, and CSS raises the other axis from `visible` to `auto`. So the
// container below takes `overflowX` and every direct child restates `minWidth`, exactly as
// RulesView.tsx:49/69/196/208/241/273 does.
const TABLE_MIN_WIDTH: Record<Mode, number> = { firm: 1046, inhouse: 1036 }

// `overflowX: 'auto'` makes that container a scroll container on BOTH axes, for the same
// reason `.pf-scroll` is — so the absolutely-positioned `⋯` menu would be clipped, and
// would spawn a vertical scrollbar, on any row without ~155px of table below it. The
// scroller simply makes room for whichever menu is open. 168 = the tallest menu (3 items
// plus the last-admin note) and its 6px offset.
//
// The obvious alternative — flip the menu upward for the last few rows — was measured and
// rejected: a row is ~58px, so opening downward needs three rows below and upward needs
// two above, and ANY filtered list under about four rows then clips in both directions.
// Searching one person's name produces exactly that list. The app has no portal and no
// fixed-position popover to borrow instead.
const MENU_CLEARANCE = 168

// The INVED-01 regression class. A grid cell only ellipsises if it is allowed to be
// narrower than its content, so `minWidth: 0` is as load-bearing as the other three.
const ELLIPSIS = { minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const

export function MembersTable({ ctx, rows, policies, roles, onOpen, onFlash }: {
  ctx: PlatformCtx
  /** Already filtered by MembersView — this component never filters. */
  rows: Member[]
  /**
   * The CURRENT workspace's approval policies and workflow roles, both off `ctx`. Named as
   * their own props rather than read through `ctx` so that `stepsForMember`'s inputs are
   * visible at the call site: the tempting wrong answers are `seedPolicies()` / `seedRoles()`,
   * neither of which ever reflects an edit made on the Workflows or Roles screen.
   */
  policies: Policy[]
  roles: Role[]
  /** Opens that member's drawer. MembersView owns the open id (types.ts:255-265). */
  onOpen: (id: string) => void
  /** Raises the top-bar confirmation flash; MembersView owns the state and the timer. */
  onFlash: (message: string) => void
}) {
  const { members, mode } = ctx
  const isFirm = mode === 'firm'
  // One id, so only one menu can be open — the app's house pattern for dismissible
  // surfaces — and so the container below knows to make room for it.
  const [openMenuId, setOpenMenuId] = useState<string | null>(null)
  // Stable: it is a `useDismiss` dependency.
  const closeMenu = useCallback(() => setOpenMenuId(null), [])
  const minWidth = TABLE_MIN_WIDTH[mode]
  // Checked against the rendered rows, not just `!= null`: a filter can narrow the list
  // out from under an open menu, and the clearance below must not be held open for a menu
  // that no longer exists.
  const menuOpen = openMenuId != null && rows.some((m) => m.id === openMenuId)

  function menuItems(m: Member, protectedAdmin: boolean): MenuAction[] {
    if (m.status === 'invited') {
      return [
        { label: 'Resend invite', onSelect: () => onFlash(`Invite resent to ${m.email}`) },
        // Deliberately does NOT touch the clipboard. There is no invite link — this tab is
        // mock-only and there is no members endpoint at all — and putting a fabricated URL
        // on someone's clipboard is a worse lie than a mock confirmation on a mock screen.
        // AC#8 asks only for the inline confirmation.
        { label: 'Copy invite link', onSelect: () => onFlash('Invite link copied') },
        { label: 'Revoke invite', danger: true, onSelect: () => ctx.dropMember(m.id) },
      ]
    }
    const items: MenuAction[] = [
      // The same target as the row click below. `MoreMenu` calls `onClose()` after
      // `onSelect()`, so the menu is gone in the commit that opens the drawer and no two
      // Escape listeners are ever live at once.
      { label: 'Edit', onSelect: () => onOpen(m.id) },
      // A shallow spread is safe for the row being replaced: `replaceMember` discards the
      // old object and nothing else holds its `clientAccess` array (ctx.members is already
      // `seedMembers()`'s deep clone), which is the aliasing `copyMember` guards against.
      m.status === 'suspended'
        ? { label: 'Reactivate', onSelect: () => ctx.saveMember({ ...m, status: 'active' }) }
        : { label: 'Suspend', disabled: protectedAdmin, onSelect: () => ctx.saveMember({ ...m, status: 'suspended' }) },
    ]
    // §6: your own menu has no Remove. A SEPARATE rule from the last-admin lock — `isYou`,
    // not `isProtectedAdmin` — and load-bearing rather than cosmetic: `dropMember` is an
    // unguarded pass-through by design (task-296 AC#4) and MembersView's empty-surface
    // chain has no `members.length === 0` branch, so an empty roster would fall through to
    // "No members match this search." with no search running. This rule is the only thing
    // that keeps that state unreachable.
    if (!m.isYou) {
      // UNCONFIRMED, deliberately — and the drawer's own `Remove` is confirmed. §6 lists this
      // menu item as a removal action and §9 requires it to be DISABLABLE, which a
      // drawer-opener would have nothing to disable; §8's confirm sentence sits under the
      // heading "Member drawer" and is scoped there. So the story really does ask for two
      // postures. FLAGGED FOR THE HUMAN at the Phase 3.5 gate rather than averaged here: the
      // faster path to remove someone is the one that never shows them §8's explanation. If
      // the human agrees, the fix is this one `onSelect`.
      items.push({ label: 'Remove', danger: true, disabled: protectedAdmin, onSelect: () => ctx.dropMember(m.id) })
    }
    return items
  }

  return (
    // Two elements where RulesView.tsx:195 uses one, and the split is the whole point: the
    // clearance above has to sit OUTSIDE the card's border. Inside it, opening a menu would
    // visibly grow the card by 168px of empty background; outside it, the menu simply
    // overhangs the card's bottom edge the way a dropdown is supposed to.
    <div style={{ overflowX: 'auto', paddingBottom: menuOpen ? MENU_CLEARANCE : 0 }}>
      <div
        data-testid="members-table"
        // No `overflow: 'hidden'` here, unlike InvoicesList.tsx:360 — it would clip the
        // menu right back. The head row's tint therefore squares off the top corners
        // fractionally, exactly as it does on RulesView.
        style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', minWidth }}
      >
        {/* Deliberately NOT `.pf-list-head`/`.pf-list-row`: those carry a <=480px collapse to
            a single column (platform.css:264-276) that would fight the minWidth this table
            depends on, and Members is a desktop surface. `.pf-row` is taken on the body rows
            for its hover highlight — with a `⋯` sitting hundreds of pixels from the name it
            acts on, the row highlight is what ties the two together — and its
            `cursor: pointer` is now honest: MEMB-01-07 gave the row its own click. */}
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: COLS[mode],
            gap: 16,
            padding: '11px 18px',
            background: 'var(--bg-1)',
            borderBottom: '1px solid var(--line-1)',
            alignItems: 'center',
            minWidth,
          }}
        >
          {HEADS[mode].map((h, i) => (
            <span key={i} className="label" style={ELLIPSIS}>
              {h}
            </span>
          ))}
        </div>

        {rows.map((m) => {
          // The CURRENT row from the CURRENT list. `isProtectedAdmin` does no identity lookup
          // — it returns true for a detached object that is not in the list at all
          // (members.test.ts:1421) — so a stale row read from a closure gives a wrong answer.
          const protectedAdmin = isProtectedAdmin(members, m)
          // Status alone: `stepsForMember` unions every role they hold and already answers
          // `null` for someone no policy names, so a second gate here would be a rule that
          // can drift from that one.
          const steps = m.status === 'suspended' ? stepsForMember(policies, roles, m.id) : null
          const blocked = steps ? steps.total : 0
          const roleCell = rosterRoleCell(roles, m.id)
          return (
            <Fragment key={m.id}>
              <div
                className="pf-row"
                data-testid="member-row"
                // What makes `.pf-row`'s `cursor: pointer` (platform.css:69) honest — until
                // now this was the app's only row that claimed to be clickable and was not.
                // The InvoicesList.tsx:388-389 / RulesView.tsx:251 shape: `onClick` straight
                // on the row div. No guard is needed against the `⋯` column — the trigger,
                // the panel and every item already `stopPropagation` (MemberParts.tsx).
                //
                // Keyboard reachability is NOT added here. A `div` with `onClick` is not
                // focusable, but the menu's `Edit` is a real <button> so a keyboard path to
                // the drawer exists, and InvoicesList.tsx:385-386 records row-level keyboard
                // access as an app-wide follow-up. Inventing a `role="button"`/`tabIndex`
                // shape on this one table would be this screen deciding it alone.
                onClick={() => onOpen(m.id)}
                style={{
                  display: 'grid',
                  gridTemplateColumns: COLS[mode],
                  gap: 16,
                  padding: '14px 18px',
                  // The warning strip below carries the hairline when there is one, so the
                  // row and its warning read as one unit rather than two.
                  borderBottom: blocked > 0 ? undefined : '1px solid var(--line-1)',
                  alignItems: 'center',
                  minWidth,
                }}
              >
                {/* Person — chip + name + email, two lines (InvoicesList.tsx:410-413). */}
                <span style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
                  <InitialsChip initials={m.initials} status={m.status} />
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                      {/* §10.1 softens the INVITED name only. Suspended keeps full-strength
                          text — its distinctness is carried by the red chip and red pill,
                          and softening it too would make the two states converge. */}
                      <span style={{ ...ELLIPSIS, fontSize: 13.5, fontWeight: 500, color: m.status === 'invited' ? 'var(--fg-3)' : 'var(--fg-1)' }}>
                        {m.name}
                      </span>
                      {m.isYou && <YouChip />}
                    </span>
                    <span className="mono" style={{ display: 'block', ...ELLIPSIS, fontSize: 11, color: 'var(--fg-3)' }}>
                      {m.email}
                    </span>
                  </span>
                </span>

                <span style={{ ...ELLIPSIS, fontSize: 13, color: 'var(--fg-2)' }}>{accessRoleLabel(m.role)}</span>

                {isFirm ? (
                  <span
                    // AC#3's tooltip. Newline-joined rather than comma-joined: six client
                    // names on one line is a tooltip nobody reads.
                    title={clientAccessNames(m.clientAccess ?? []).join('\n')}
                    style={{ ...ELLIPSIS, fontSize: 13, color: 'var(--fg-2)' }}
                  >
                    {clientAccessLabel(m.clientAccess ?? [])}
                  </span>
                ) : (
                  <span style={{ ...ELLIPSIS, fontSize: 13, color: 'var(--fg-2)' }}>{m.department ?? '—'}</span>
                )}

                {/* Newline-joined for the same reason the Client access tooltip is; empty on
                    a roleless row, which is the `—` case and wants no tooltip at all. */}
                <span
                  title={roleCell.tooltip || undefined}
                  style={{ ...ELLIPSIS, fontSize: 13, color: roleCell.tooltip ? 'var(--fg-2)' : 'var(--fg-4)' }}
                >
                  {roleCell.text}
                </span>

                <span style={{ minWidth: 0 }}>
                  <MemberStatusPill status={m.status} />
                </span>

                <span className="mono" style={{ ...ELLIPSIS, fontSize: 12, color: 'var(--fg-3)' }}>
                  {lastActiveLabel(m)}
                </span>

                <MoreMenu
                  open={openMenuId === m.id}
                  onOpen={() => setOpenMenuId(m.id)}
                  onClose={closeMenu}
                  label={m.name}
                  items={menuItems(m, protectedAdmin)}
                  note={protectedAdmin ? PROTECTED_ADMIN_NOTE : undefined}
                />
              </div>

              {blocked > 0 && (
                // A full-width strip under the row rather than a third line inside the Person
                // cell: AC#12 wants row 5 / row 11 to leave the column widths alone, and a
                // warning inside a fixed cell would have to ellipsise away exactly when it
                // matters.
                <div style={{ padding: '0 18px 12px', borderBottom: '1px solid var(--line-1)', minWidth }}>
                  <AmberNote testId="member-steps-warning">{stepsWarning(blocked)}</AmberNote>
                </div>
              )}
            </Fragment>
          )
        })}
      </div>
    </div>
  )
}
