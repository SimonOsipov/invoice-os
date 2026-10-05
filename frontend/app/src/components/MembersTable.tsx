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
import { InitialsChip, MemberStatusPill, MoreMenu, YouChip, type MenuAction } from './MemberParts'
import type { PlatformCtx } from '../types'

// Gap 12, head '11px 16px' and row '12px 16px' sit in the head and row styles below.
const COLS = 'minmax(220px,1.4fr) 110px minmax(150px,1fr) 120px 36px'

// The trailing '' is the `⋯` column: at 36px no uppercase 10.5px label fits, and every
// action column in the app is unlabelled.
const HEADS = ['Person', 'Access role', 'Workflow roles', 'Status', '']

// Floors 220 + 110 + 150 + 120 + 36, 4 gaps x 12, padding 32. The scroll container below stops a
// narrow viewport scrolling the whole Settings page sideways (`.pf-scroll` sets only `overflowY`).
// Every direct child restates `minWidth`, as RulesView does.
const TABLE_MIN_WIDTH = 716

// `overflowX: 'auto'` makes that container a scroll container on BOTH axes, for the same
// reason `.pf-scroll` is — so the absolutely-positioned `⋯` menu would be clipped, and
// would spawn a vertical scrollbar, on any row without room below it. The scroller simply
// makes room for whichever menu is open.
//
// Sized for the tallest reachable menu, an invited row: 3 items and 2 reasons wrapped at the 280px
// panel (~235px), less the ~15px of row below the trigger, plus one spare wrapped line per reason.
// ceiling: derived, not measured on a deployed build; re-measure if a menu reason grows.
const MENU_CLEARANCE = 256

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
        // No `overflow: 'hidden'`: it would clip the `⋯` menu.
        style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', minWidth }}
      >
        {/* Not `.pf-list-head`/`.pf-list-row`: their <=480px single-column collapse would fight
            `minWidth`. `.pf-row` is taken for its hover highlight and pointer cursor. */}
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: COLS,
            gap: 12,
            padding: '11px 16px',
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

        {rows.length === 0 && (
          <div
            data-testid="members-no-match"
            style={{ borderTop: '1px solid var(--line-1)', padding: '26px 16px', textAlign: 'center', fontSize: 12.5, color: 'var(--fg-3)', minWidth }}
          >
            No members match this search.
          </div>
        )}

        {rows.map((m) => {
          // The CURRENT row from the CURRENT list. `isProtectedAdmin` does no identity lookup
          // — it returns true for a detached object that is not in the list at all
          // (members.test.ts QA38) — so a stale row read from a closure gives a wrong answer.
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
                // What makes `.pf-row`'s `cursor: pointer` (platform.css) honest — until
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
                  gap: 12,
                  padding: '12px 16px',
                  borderTop: '1px solid var(--line-1)',
                  alignItems: 'center',
                  minWidth,
                }}
              >
                {/* Person — chip + name + email, two lines (InvoicesList.tsx:410-413). */}
                <span style={{ display: 'flex', alignItems: 'center', gap: 11, minWidth: 0 }}>
                  <InitialsChip initials={m.initials} status={m.status} />
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 7, minWidth: 0 }}>
                      {/* §10.1 softens the INVITED name only. Suspended keeps full-strength
                          text — its distinctness is carried by the red chip and red pill,
                          and softening it too would make the two states converge. */}
                      <span style={{ ...ELLIPSIS, fontSize: 13.5, fontWeight: 500, color: m.status === 'invited' ? 'var(--fg-3)' : 'var(--fg-1)' }}>
                        {m.name}
                      </span>
                      {m.isYou && <YouChip />}
                    </span>
                    <span className="mono" style={{ display: 'block', ...ELLIPSIS, fontSize: 10.5, marginTop: 2, color: 'var(--fg-3)' }}>
                      {emailLabel(m)}
                    </span>
                  </span>
                </span>

                <span style={{ ...ELLIPSIS, fontSize: 12.5, color: 'var(--fg-2)' }}>{accessRoleLabel(m.role)}</span>

                {/* Newline-joined tooltip; empty on a roleless row, which is the `—` case and
                    wants no tooltip at all. */}
                <span
                  title={roleCell.tooltip || undefined}
                  style={{ ...ELLIPSIS, fontSize: 12.5, color: roleCell.tooltip ? 'var(--fg-1)' : 'var(--fg-3)' }}
                >
                  {roleCell.text}
                </span>

                <span style={{ minWidth: 0, display: 'flex' }}>
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
                <div
                  data-testid="member-status-error"
                  style={{
                    padding: '7px 16px',
                    background: 'var(--status-red-bg)',
                    borderTop: '1px solid var(--status-red-border)',
                    fontSize: 11.5,
                    color: 'var(--status-red-text)',
                    minWidth,
                  }}
                >
                  {statusError.message}
                </div>
              )}

              {blocked > 0 && (
                // A full-width strip under the row: a warning inside a fixed cell would have to
                // ellipsise away exactly when it matters.
                <div
                  data-testid="member-steps-warning"
                  style={{
                    padding: '7px 16px',
                    background: 'var(--status-amber-bg)',
                    borderTop: '1px solid var(--status-amber-border)',
                    fontSize: 11.5,
                    color: 'var(--status-amber-text)',
                    minWidth,
                  }}
                >
                  {stepsWarning(blocked)}
                </div>
              )}
            </Fragment>
          )
        })}
      </div>
    </div>
  )
}
