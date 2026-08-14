// Settings › Members — the roster table.
//
// A CSS grid, not a <table>: the screen idiom in this app is a `gridTemplateColumns`
// literal repeated on a head row and every body row (InvoicesList, ClientsView,
// CustomersView, RulesView). The one real <table>, ViolationsTable, is a shared component
// embedded inside screens rather than a screen's own layout.
//
// ONE column set, both modes. The two that used to fork — firm's client scoping, in-house's
// department — are gone with the columns themselves: a membership row carries an identity,
// an access role and a status, and nothing else.
//
// Nothing is derived here. Every value comes from lib/members.ts or lib/roles.ts — vitest
// is `environment: node` in this project, so a derivation written into a component is a
// derivation no test can reach (§15.8).

import { Fragment, useCallback, useState } from 'react'

import {
  accessRoleLabel,
  emailLabel,
  isProtectedAdmin,
  MEMBER_UNBACKED,
  PROTECTED_ADMIN_NOTE,
  type Member,
  type MemberStatus,
} from '../lib/members'
import { rosterRoleCell, stepsForMember, stepsWarning, type Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import { AmberNote, InitialsChip, MemberStatusPill, MoreMenu, YouChip, type MenuAction } from './MemberParts'
import type { PlatformCtx } from '../types'

// §15.7's grid, plus the two things a grid template does not state and this table needs:
// the gap and the row padding. Both are the house constants — gap 16, head '11px 18px',
// row '14px 18px' (InvoicesList.tsx:361/:392, ClientsView.tsx:140/:157).
const COLS = 'minmax(190px,1fr) 130px 160px 120px 44px'

// The trailing '' is the `⋯` column: at 44px no uppercase 10.5px label fits, and every
// action column in the app is unlabelled.
const HEADS = ['Person', 'Access role', 'Workflow roles', 'Status', '']

// The width the grid actually needs. RulesView.tsx:19-29's arithmetic redone for this
// table — RulesView's own 790 does not transfer, because its rationale is sitting beside a
// fixed 244px rail and this table has none.
//
//   190 person floor + 454 fixed (130+160+120+44) + 4 gaps x 16 + 36 padding = 744
//
// 160px is the old in-house Approval-position column's width, which already carried titles
// this long. "WORKFLOW ROLES" as a 10.5px uppercase `.label` measures ~102px, so no head
// truncates.
//
// The Settings content box is ~1116px at a 1440px viewport (1440 - 252 sidebar,
// Sidebar.tsx:128 - 72 page padding, SettingsView.tsx:47), so at 744 the table no longer
// overflows it — the three deleted columns took ~300px with them. The scroll container
// below is kept anyway: it is what stops an overflow at a narrow viewport scrolling the
// WHOLE Settings page sideways, dragging the h1 and the tab strip with it (App.tsx:800's
// `.pf-scroll` sets only `overflowY`, and CSS raises the other axis from `visible` to
// `auto`). Every direct child restates `minWidth`, exactly as RulesView.tsx does.
const TABLE_MIN_WIDTH = 744

// `overflowX: 'auto'` makes that container a scroll container on BOTH axes, for the same
// reason `.pf-scroll` is — so the absolutely-positioned `⋯` menu would be clipped, and
// would spawn a vertical scrollbar, on any row without room below it. The scroller simply
// makes room for whichever menu is open.
//
// Sized for the tallest REACHABLE menu — the active non-self row (Edit + Suspend + Remove,
// one reason note) — measured on the deployed build at 189.73px required clearance;
// viewport-independent, since the panel is a fixed 196px wide at 11px/1.45.
//
// The obvious alternative — flip the menu upward for the last few rows — was measured and
// rejected: a row is ~58px, so opening downward needs three rows below and upward needs
// two above, and ANY filtered list under about four rows then clips in both directions.
// Searching one person's name produces exactly that list. The app has no portal and no
// fixed-position popover to borrow instead.
const MENU_CLEARANCE = 192

// The INVED-01 regression class. A grid cell only ellipsises if it is allowed to be
// narrower than its content, so `minWidth: 0` is as load-bearing as the other three.
const ELLIPSIS = { minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const

export function MembersTable({ ctx, rows, policies, roles, onOpen, onStatus, statusError }: {
  ctx: PlatformCtx
  /** Already filtered by MembersView — this component never filters. */
  rows: Member[]
  /**
   * The CURRENT workspace's approval policies and workflow roles, both off `ctx`. Named as
   * their own props rather than read through `ctx` so that `stepsForMember`'s inputs are
   * visible at the call site: the tempting wrong answer is `seedPolicies()`, which never
   * reflects an edit made on the Workflows screen.
   */
  policies: Policy[]
  roles: Role[]
  /** Opens that member's drawer. MembersView owns the open id. */
  onOpen: (id: string) => void
  /** The live status write. Never rejects — MembersView catches into `statusError`. */
  onStatus: (id: string, status: Exclude<MemberStatus, 'invited'>) => void
  /** The last failed write's server reason, and the row it happened on. */
  statusError: { id: string; message: string } | null
}) {
  const { members } = ctx
  // One id, so only one menu can be open — the app's house pattern for dismissible
  // surfaces — and so the container below knows to make room for it.
  const [openMenuId, setOpenMenuId] = useState<string | null>(null)
  // Stable: it is a `useDismiss` dependency.
  const closeMenu = useCallback(() => setOpenMenuId(null), [])
  const minWidth = TABLE_MIN_WIDTH
  // Checked against the rendered rows, not just `!= null`: a filter can narrow the list
  // out from under an open menu, and the clearance below must not be held open for a menu
  // that no longer exists.
  const menuOpen = openMenuId != null && rows.some((m) => m.id === openMenuId)

  function menuItems(m: Member, protectedAdmin: boolean): MenuAction[] {
    if (m.status === 'invited') {
      // All three disabled with the server's own reason: nothing mints a token, nothing
      // sends an email, and nothing deletes a membership. Rendered rather than hidden — a
      // control that vanishes says the product never had it.
      return [
        { label: 'Resend invite', disabled: true, reason: MEMBER_UNBACKED.invite },
        { label: 'Copy invite link', disabled: true, reason: MEMBER_UNBACKED.invite },
        { label: 'Revoke invite', danger: true, disabled: true, reason: MEMBER_UNBACKED.remove },
      ]
    }
    const items: MenuAction[] = [
      // The same target as the row click below. `MoreMenu` calls `onClose()` after
      // `onSelect()`, so the menu is gone in the commit that opens the drawer and no two
      // Escape listeners are ever live at once — which is also why a failed write's reason
      // renders on the ROW below rather than in here.
      { label: 'Edit', onSelect: () => onOpen(m.id) },
      m.status === 'suspended'
        ? { label: 'Reactivate', onSelect: () => onStatus(m.id, 'active') }
        : {
            label: 'Suspend',
            disabled: protectedAdmin,
            reason: protectedAdmin ? PROTECTED_ADMIN_NOTE : undefined,
            onSelect: () => onStatus(m.id, 'suspended'),
          },
    ]
    // §6: your own menu has no Remove. A SEPARATE rule from the last-admin lock — `isYou`,
    // not `isProtectedAdmin`.
    if (!m.isYou) {
      items.push({ label: 'Remove', danger: true, disabled: true, reason: MEMBER_UNBACKED.remove })
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
            gridTemplateColumns: COLS,
            gap: 16,
            padding: '11px 18px',
            background: 'var(--bg-1)',
            borderBottom: '1px solid var(--line-1)',
            alignItems: 'center',
            minWidth,
          }}
        >
          {HEADS.map((h, i) => (
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
          // Unreachable via the shipped app (MembersView only mounts this table once
          // rolesState has landed) — defense-in-depth so an unlanded fetch renders empty,
          // never ABSENT_LABEL's '—', which claims "holds no roles".
          const rolesLanded = ctx.rolesState === 'ready' || ctx.rolesState === 'empty'
          const roleCell = rolesLanded ? rosterRoleCell(roles, m.id) : { text: '', tooltip: '' }
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
                  gridTemplateColumns: COLS,
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
                      {emailLabel(m)}
                    </span>
                  </span>
                </span>

                <span style={{ ...ELLIPSIS, fontSize: 13, color: 'var(--fg-2)' }}>{accessRoleLabel(m.role)}</span>

                {/* Newline-joined tooltip; empty on a roleless row, which is the `—` case and
                    wants no tooltip at all. */}
                <span
                  title={roleCell.tooltip || undefined}
                  style={{ ...ELLIPSIS, fontSize: 13, color: roleCell.tooltip ? 'var(--fg-2)' : 'var(--fg-4)' }}
                >
                  {roleCell.text}
                </span>

                <span style={{ minWidth: 0 }}>
                  <MemberStatusPill status={m.status} />
                </span>

                <MoreMenu
                  open={openMenuId === m.id}
                  onOpen={() => setOpenMenuId(m.id)}
                  onClose={closeMenu}
                  label={m.name}
                  items={menuItems(m, protectedAdmin)}
                />
              </div>

              {/* The failed write's SERVER reason, in the slot the steps warning already
                  occupies. Here and not in the `⋯` menu because `MoreMenu` closes on select,
                  so the control that started the write no longer exists when it settles. */}
              {statusError?.id === m.id && (
                <div style={{ padding: '0 18px 12px', minWidth }}>
                  <div
                    data-testid="member-status-error"
                    style={{
                      padding: '10px 12px',
                      borderRadius: 'var(--radius-md)',
                      background: 'var(--status-red-bg)',
                      border: '1px solid var(--status-red-border)',
                      fontSize: 12.5,
                      lineHeight: 1.5,
                      color: 'var(--status-red-text)',
                    }}
                  >
                    {statusError.message}
                  </div>
                </div>
              )}

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
