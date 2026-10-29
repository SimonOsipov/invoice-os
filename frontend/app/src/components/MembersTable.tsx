// Settings › Members — the roster table.
//
// A CSS grid, not a <table>: the screen idiom in this app is a `gridTemplateColumns`
// literal repeated on a head row and every body row (InvoicesList, ClientsView,
// CustomersView, RulesView). The one real <table>, ViolationsTable, is a shared component
// embedded inside screens rather than a screen's own layout.
//
// ONE column set, both modes. Firm's client access is a second line in the Access role cell,
// not a column.
//
// Nothing is derived here. Every value comes from lib/members.ts or lib/roles.ts — vitest
// is `environment: node` in this project, so a derivation written into a component is a
// derivation no test can reach (§15.8).

import { Fragment, useCallback, useEffect, useRef, useState } from 'react'

import {
  accessRoleLabel,
  clientAccessLabel,
  clientAccessTitle,
  emailLabel,
  inviteAccountLine,
  inviteStatusLine,
  memberActionsLock,
  REMOVE_CONFIRMED,
  REMOVE_KEEP,
  removeConfirmQuestion,
  REVOKE_CONFIRM,
  REVOKE_CONFIRMED,
  REVOKE_KEEP,
  type Member,
  type PatchableStatus,
  type PendingInvite,
} from '../lib/members'
import { rosterRoleCell, stepsForMember, stepsWarning, type Role } from '../lib/roles'
import { useDismiss } from '../lib/useDismiss'
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
// 216 fits the plain menu, 256 the pending-row menu with its account-state note; roles.spec.ts ›
// "firm Settings: an admin invites from the Members screen, sees the pending row, and resends"
// (L2, L2b) gates both.
// ceiling: re-measure if an item, reason or note is added.
const MENU_CLEARANCE = 216
const MENU_CLEARANCE_NOTED = 256

// The INVED-01 regression class. A grid cell only ellipsises if it is allowed to be
// narrower than its content, so `minWidth: 0` is as load-bearing as the other three.
const ELLIPSIS = { minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const

export function MembersTable({ ctx, rows, policies, roles, onOpen, onStatus, onRemove, statusError, invites = [], onResend, resending, onRevoke, revoking }: {
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
  onStatus: (id: string, status: PatchableStatus) => void
  /** The remove write. Never rejects; MembersView renders a refusal in `statusError`. */
  onRemove?: (id: string) => void
  /** The last failed write's server reason, and the row it happened on. */
  statusError: { id: string; message: string } | null
  /** Pending invitations, matched to an `invited` row by `Member.id`. */
  invites?: readonly PendingInvite[]
  onResend?: (id: string) => void
  /** Invitation ids with a resend in flight. */
  resending?: ReadonlySet<string>
  onRevoke?: (id: string) => void
  /** Invitation ids with a revoke in flight. */
  revoking?: ReadonlySet<string>
}) {
  const { members } = ctx
  // One id, so only one menu can be open — the app's house pattern for dismissible
  // surfaces — and so the container below knows to make room for it.
  const [openMenuId, setOpenMenuId] = useState<string | null>(null)
  // Stable: it is a `useDismiss` dependency.
  const closeMenu = useCallback(() => setOpenMenuId(null), [])
  const minWidth = TABLE_MIN_WIDTH
  // The invite whose revoke confirm strip is open.
  const [revokeId, setRevokeId] = useState<string | null>(null)
  const closeRevoke = useCallback(() => setRevokeId(null), [])
  useDismiss(revokeId != null, closeRevoke)
  const keepRef = useRef<HTMLButtonElement>(null)
  // Focus the safe button on open.
  useEffect(() => {
    if (revokeId != null) keepRef.current?.focus()
  }, [revokeId])
  // A filter can drop the row; the strip must not return when it comes back.
  useEffect(() => {
    if (revokeId != null && !rows.some((m) => m.id === revokeId)) setRevokeId(null)
  }, [rows, revokeId])

  // The member whose Remove confirm strip is open; same lifecycle as the revoke strip.
  const [removeId, setRemoveId] = useState<string | null>(null)
  const closeRemove = useCallback(() => setRemoveId(null), [])
  useDismiss(removeId != null, closeRemove)
  const keepRemoveRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (removeId != null) keepRemoveRef.current?.focus()
  }, [removeId])
  useEffect(() => {
    if (removeId != null && !rows.some((m) => m.id === removeId)) setRemoveId(null)
  }, [rows, removeId])
  // Checked against the rendered rows, not just `!= null`: a filter can narrow the list
  // out from under an open menu, and the clearance below must not be held open for a menu
  // that no longer exists.
  const openRow = openMenuId != null ? rows.find((m) => m.id === openMenuId) : undefined
  const openNoted = openRow?.status === 'invited' && invites.some((i) => i.id === openRow.id && inviteAccountLine(i) != null)
  const clearance = !openRow ? 0 : openNoted ? MENU_CLEARANCE_NOTED : MENU_CLEARANCE

  function menuItems(m: Member, lock: string | null): MenuAction[] {
    if (m.status === 'invited') {
      // Resend and Revoke exclude each other on one row while either is in flight.
      return [
        { label: 'Resend invite', disabled: resending?.has(m.id) || revoking?.has(m.id), onSelect: () => onResend?.(m.id) },
        { label: 'Revoke invite', danger: true, disabled: revoking?.has(m.id) || resending?.has(m.id), onSelect: () => setRevokeId(m.id) },
      ]
    }
    const items: MenuAction[] = [
      // The same target as the row click below. `MoreMenu` calls `onClose()` after
      // `onSelect()`, so the menu is gone in the commit that opens the drawer and no two
      // Escape listeners are ever live at once — which is also why a failed write's reason
      // renders on the ROW below rather than in here.
      { label: 'Edit', onSelect: () => onOpen(m.id) },
      m.status === 'suspended'
        ? { label: 'Reactivate', disabled: lock != null, reason: lock ?? undefined, onSelect: () => onStatus(m.id, 'active') }
        : {
            label: 'Suspend',
            disabled: lock != null,
            reason: lock ?? undefined,
            onSelect: () => onStatus(m.id, 'suspended'),
          },
    ]
    // Your own menu has no Remove: a separate rule from the lock.
    if (!m.isYou) {
      items.push({
        label: 'Remove',
        danger: true,
        disabled: lock != null,
        reason: lock ?? undefined,
        onSelect: () => setRemoveId(m.id),
      })
    }
    return items
  }

  return (
    // Two elements where RulesView uses one, and the split is the whole point: the
    // clearance above has to sit OUTSIDE the card's border. Inside it, opening a menu would
    // visibly grow the card by empty background; outside it, the menu simply
    // overhangs the card's bottom edge the way a dropdown is supposed to.
    <div data-testid="members-table-scroll" style={{ overflowX: 'auto', paddingBottom: clearance }}>
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
          const lock = memberActionsLock(members, m)
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
          const pending = m.status === 'invited'
          // A removed row is history: pill and name only, no role, menu or drawer.
          const removed = m.status === 'removed'
          const clientLine = ctx.mode === 'firm' && !removed ? clientAccessLabel(m.clientAccess) : null
          const invite = pending ? invites.find((i) => i.id === m.id) : undefined
          const accountLine = invite ? inviteAccountLine(invite) : null
          return (
            <Fragment key={m.id}>
              <div
                className="pf-row"
                data-testid={pending ? 'invite-row' : 'member-row'}
                // What makes `.pf-row`'s `cursor: pointer` (platform.css) honest — until
                // now this was the app's only row that claimed to be clickable and was not.
                // The InvoicesList / RulesView shape: `onClick` straight
                // on the row div. No guard is needed against the `⋯` column — the trigger,
                // the panel and every item already `stopPropagation` (MemberParts.tsx).
                //
                // Keyboard reachability is NOT added here. A `div` with `onClick` is not
                // focusable, but the menu's `Edit` is a real <button> so a keyboard path to
                // the drawer exists, and InvoicesList.tsx:385-386 records row-level keyboard
                // access as an app-wide follow-up. Inventing a `role="button"`/`tabIndex`
                // shape on this one table would be this screen deciding it alone.
                onClick={pending || removed ? undefined : () => onOpen(m.id)}
                style={{
                  cursor: pending || removed ? 'default' : undefined,
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
                      <span style={{ ...ELLIPSIS, fontSize: 13.5, fontWeight: 500, color: m.status === 'invited' || removed ? 'var(--fg-3)' : 'var(--fg-1)' }}>
                        {m.name}
                      </span>
                      {m.isYou && <YouChip />}
                    </span>
                    <span className="mono" style={{ display: 'block', ...ELLIPSIS, fontSize: 10.5, marginTop: 2, color: 'var(--fg-3)' }}>
                      {emailLabel(m)}
                    </span>
                  </span>
                </span>

                <span style={{ minWidth: 0 }}>
                  <span style={{ display: 'block', ...ELLIPSIS, fontSize: 12.5, color: 'var(--fg-2)' }}>{removed ? '' : accessRoleLabel(m.role)}</span>
                  {clientLine && (
                    <span
                      data-testid="member-client-access"
                      title={clientAccessTitle(ctx.entities, m.clientAccess)}
                      style={{ display: 'block', ...ELLIPSIS, marginTop: 2, fontSize: 11, color: 'var(--fg-3)' }}>
                      {clientLine}
                    </span>
                  )}
                </span>

                {/* Newline-joined tooltip; empty on a roleless row, which is the `—` case and
                    wants no tooltip at all. */}
                <span
                  title={roleCell.tooltip || undefined}
                  style={{ ...ELLIPSIS, fontSize: 12.5, color: roleCell.tooltip ? 'var(--fg-1)' : 'var(--fg-3)' }}
                >
                  {roleCell.text}
                </span>

                <span style={{ minWidth: 0, display: 'flex', flexDirection: 'column', alignItems: 'flex-start' }}>
                  <MemberStatusPill status={m.status} />
                  {accountLine && (
                    // Wraps instead of ellipsising: the label is longer than the 120px column.
                    <span
                      data-testid="invite-account-state"
                      style={{ marginTop: 3, maxWidth: '100%', minWidth: 0, fontSize: 11, lineHeight: 1.35, color: 'var(--fg-3)', whiteSpace: 'normal', overflowWrap: 'anywhere' }}
                    >
                      {accountLine}
                    </span>
                  )}
                  {invite && (
                    <span style={{ marginTop: 3, maxWidth: '100%', ...ELLIPSIS, fontSize: 11, color: 'var(--fg-3)' }}>
                      {inviteStatusLine(invite, Date.now())}
                    </span>
                  )}
                </span>

                {removed ? (
                  <span />
                ) : (
                  <MoreMenu
                    open={openMenuId === m.id}
                    onOpen={() => setOpenMenuId(m.id)}
                    onClose={closeMenu}
                    label={m.name}
                    items={menuItems(m, lock)}
                    note={accountLine ?? undefined}
                  />
                )}
              </div>

              {pending && revokeId === m.id && (
                <div
                  data-testid="invite-revoke-confirm"
                  style={{
                    padding: '9px 16px',
                    background: 'var(--status-red-bg)',
                    borderTop: '1px solid var(--status-red-border)',
                    minWidth,
                  }}
                >
                  <p style={{ margin: '0 0 8px', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}>{REVOKE_CONFIRM}</p>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button
                      type="button"
                      ref={keepRef}
                      onClick={closeRevoke}
                      className="v2-btn v2-btn-ghost pf-btn"
                      data-testid="invite-revoke-keep"
                      style={{ height: 32, fontSize: 12.5 }}
                    >
                      {REVOKE_KEEP}
                    </button>
                    <button
                      type="button"
                      disabled={revoking?.has(m.id) || resending?.has(m.id)}
                      onClick={() => {
                        setRevokeId(null)
                        onRevoke?.(m.id)
                      }}
                      className="v2-btn pf-btn"
                      data-testid="invite-revoke-confirmed"
                      style={{ height: 32, fontSize: 12.5, background: 'var(--status-red-text)', color: 'var(--primary-foreground)' }}
                    >
                      {REVOKE_CONFIRMED}
                    </button>
                  </div>
                </div>
              )}

              {removeId === m.id && (
                <div
                  data-testid="member-remove-confirm"
                  style={{
                    padding: '9px 16px',
                    background: 'var(--status-red-bg)',
                    borderTop: '1px solid var(--status-red-border)',
                    minWidth,
                  }}
                >
                  <p style={{ margin: '0 0 8px', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}>{removeConfirmQuestion(m.name)}</p>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button
                      type="button"
                      ref={keepRemoveRef}
                      onClick={closeRemove}
                      className="v2-btn v2-btn-ghost pf-btn"
                      data-testid="member-remove-keep"
                      style={{ height: 32, fontSize: 12.5 }}
                    >
                      {REMOVE_KEEP}
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        setRemoveId(null)
                        onRemove?.(m.id)
                      }}
                      className="v2-btn pf-btn"
                      data-testid="member-remove-confirmed"
                      style={{ height: 32, fontSize: 12.5, background: 'var(--status-red-text)', color: 'var(--primary-foreground)' }}
                    >
                      {REMOVE_CONFIRMED}
                    </button>
                  </div>
                </div>
              )}

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
