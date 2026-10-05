// §8's member drawer — a 560px right drawer, the app's fifth overlay.
//
// Structurally `RuleDrawer` (RuleDrawer.tsx:23-46): scrim, `role="dialog"`, `aria-modal`,
// the `pfDrawer` animation and the `pf-drawer` class. That class is taken for a concrete
// reason and not as decoration — its ONLY rule is `width: 100vw !important` under a
// max-width media query (platform.css:259-261), so omitting it silently deletes the mobile
// collapse.
//
// One departure from that shell: it closes on Escape. `RuleDrawer` registers no keydown listener,
// so `useDismiss(true, onClose)` with no `outsideRef` does it; the scrim's own `onClick` is the
// outside click, the call shape useDismiss.ts pre-authorises for this drawer by name.
//
// No derivation and no copy is authored here. §8/§9's sentences and the three facts behind
// them live in lib/members.ts with specs, because vitest is `environment: node` and this
// component's oracle is a screenshot — the one gate a fluent paraphrase walks through
// (§15.8).

import { useId, useState, type ReactNode } from 'react'

import { ErrorState, Loading, toApiError } from '@invoice-os/api-client'
import { closeGlyph } from '../glyphs'
import {
  ACCESS_ROLES,
  emailLabel,
  isProtectedAdmin,
  MEMBER_UNBACKED,
  PROTECTED_ADMIN_NOTE,
  REMOVE_EXPLANATION,
  SUSPEND_EXPLANATION,
  type MemberStatus,
} from '../lib/members'
import {
  drawerRoleHelper,
  rolesOfMember,
  stepsForMember,
  stepsNamedLine,
  SUSPENDED_STEPS_NOTE,
} from '../lib/roles'
import { useDismiss } from '../lib/useDismiss'
import { AmberNote, ClientAccessPicker, DepartmentField, InitialsChip, MemberStatusPill, RoleCards, WorkflowRolePills } from './MemberParts'
import type { PlatformCtx } from '../types'

// §4 names this as the affordance, not as prose, so it stays a component constant rather
// than joining the drawer's copy in lib/roles.ts — MATRIX_HEADING's posture.
const MANAGE_ROLES = 'Manage roles'

/** Every card, so the picker shows the real role and offers none of them. */
const ACCESS_ROLE_IDS = ACCESS_ROLES.map((r) => r.id)

/**
 * Two controls under ONE lock — `ClientAccessPicker` holds state and takes no `disabled`, and
 * `DepartmentField`'s `WfSelect` has one that would not reach it. A `<fieldset disabled>` gives all
 * four layers without plumbing a prop through a shared component for a Members-only need:
 * HTML natively disables every descendant (1, unclickable and out of the tab order),
 * `pointerEvents: none` is a stronger (2) than any background swap, the sibling below is
 * (3), and `aria-describedby` on the fieldset is (4). This was the first `<fieldset>` in
 * `frontend/`, accepted over mutating `WfSelect`; WorkflowBuilder.tsx:73 now cites it back for
 * the same trade. `minInlineSize: 0` is mandatory: a fieldset defaults to `min-content` and
 * would refuse to shrink inside a 560px drawer.
 */
function UnbackedField({ reason, noteId, dim = false, children }: { reason: string; noteId: string; dim?: boolean; children: ReactNode }) {
  return (
    <>
      <fieldset
        disabled
        aria-describedby={noteId}
        style={{ border: 0, padding: 0, margin: 0, minInlineSize: 0, pointerEvents: 'none', ...(dim ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null) }}
      >
        {children}
      </fieldset>
      <div id={noteId} style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
        {reason}
      </div>
    </>
  )
}

export function MemberDrawer({ ctx, memberId, onClose, onStatus, statusError }: {
  ctx: PlatformCtx
  /** The ID, never the row — see MembersView.tsx and the `row` lookup below. */
  memberId: string
  /** Must be STABLE — it is a `useDismiss` dependency (useDismiss.ts:36-37). */
  onClose: () => void
  /** The live status write. Never rejects — MembersView catches into `statusError`. */
  onStatus: (id: string, status: Exclude<MemberStatus, 'invited'>) => void
  /** The last failed write's server reason, and the row it happened on. */
  statusError: { id: string; message: string } | null
}) {
  const noteId = useId()
  const roleNoteId = useId()
  const scopeNoteId = useId()
  const removeNoteId = useId()
  useDismiss(true, onClose)

  // The staffing write's own in-flight flag and failure reason -- the MembersView.tsx:79-89
  // statusError SHAPE, reused for a different write than the one that prop already carries.
  const [rolePending, setRolePending] = useState(false)
  const [roleError, setRoleError] = useState<{ id: string; message: string } | null>(null)

  // Resolved from the CURRENT list on every render, never captured: `isProtectedAdmin` does
  // no identity lookup and answers `true` for a detached row (members.test.ts), so a stale
  // row gives a wrong lock, and a status write replaces the row under this drawer.
  const row = ctx.members.find((m) => m.id === memberId) ?? null
  if (!row) return null

  const protectedAdmin = isProtectedAdmin(ctx.members, row)

  // BOTH halves of the gate — holding a role, and actually being named in a step — are
  // `stepsForMember`'s, not this component's. It is the rule that stops the drawers of people
  // whose role no policy names reading "Named in 0 approval steps", and a rule derived here
  // is a rule no spec can hold (§15.8, and this file's header). `null` means render nothing;
  // the gate below is that null, never a re-derived count. Both modes, no fork. `ctx.policies`
  // / `ctx.roles` are the CURRENT workspace's; the seeds would never reflect an edit.
  // `ctx.roles` is `[]` for the round trip on every background roles refetch (App.tsx's
  // `setRoles(rolesAsync.data ?? [])`, unlike `members`' patch-in-place), so reading it
  // unguarded here would render an open drawer's held roles as "none" mid-refetch.
  // MembersTable.tsx's own `rolesLanded` gate, reused: 'empty' is a genuinely landed
  // answer and stays landed, only 'loading'/'idle'/'error' are not.
  const rolesLanded = ctx.rolesState === 'ready' || ctx.rolesState === 'empty'
  const steps = rolesLanded ? stepsForMember(ctx.policies, ctx.roles, row.id) : null
  const heldRoleKeys = rolesLanded ? rolesOfMember(ctx.roles, row.id).map((r) => r.key) : []

  // §6's rule, and a SEPARATE one from the last-admin lock: your own row has no Remove.
  // The `⋯` menu holds the same line (MembersTable.tsx). OMITTED, not disabled — a control
  // that would act on YOU is a different fact from one no endpoint backs.
  const canRemove = !row.isYou
  // `invited` is not a PATCH target — the wire excludes it at the type level — and there is
  // no path back to it, so offering Suspend on an invited row would ship a one-way trap.
  // The table's `⋯` menu forks the same way.
  const canSuspend = row.status !== 'invited'
  const suspending = row.status !== 'suspended'

  const disabledGhost = { background: 'transparent', opacity: 0.45, cursor: 'not-allowed', filter: 'none' } as const

  // Writes straight through, like every other control here — §8 says each change persists
  // immediately and there is no Save button. Holders live on the ROLE, so the write funnel is
  // `staffRole`. The `rolePending` guard is load-bearing, not the fieldset below: jsdom never
  // propagates `<fieldset disabled>` to a descendant button's IDL `disabled` property.
  async function toggleWorkflowRole(key: string) {
    if (rolePending) return
    const role = ctx.roles.find((r) => r.key === key)
    if (!role) return
    const members = role.members.includes(memberId) ? role.members.filter((id) => id !== memberId) : [...role.members, memberId]
    setRoleError(null)
    setRolePending(true)
    try {
      await ctx.staffRole(role.key, members)
    } catch (err: unknown) {
      // Verbatim, no prefix — the drawer's own status-error rule (the footer's below), reused.
      setRoleError({ id: key, message: toApiError(err).message })
    } finally {
      setRolePending(false)
    }
  }

  return (
    <>
      <div
        onClick={onClose}
        style={{
          position: 'fixed',
          inset: 0,
          zIndex: 80,
          background: 'color-mix(in srgb, var(--surface) 55%, transparent)',
          backdropFilter: 'blur(6px)',
          WebkitBackdropFilter: 'blur(6px)',
          animation: 'pfFade 160ms ease-out',
        }}
      />
      <div
        className="pf-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={`Member ${row.name}`}
        data-testid="member-drawer"
        style={{
          position: 'fixed',
          top: 0,
          right: 0,
          bottom: 0,
          zIndex: 81,
          width: 560,
          maxWidth: '94vw',
          background: 'var(--bg-1)',
          borderLeft: '1px solid var(--line-2)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'pfDrawer 200ms ease-out',
        }}
      >
        {/* Header — AC#2. NOT an <h1>: SettingsView.tsx:49-53 owns the page heading and a
            second one inside a tab would give the page two. */}
        <div style={{ flex: 'none', padding: '18px 24px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 13 }}>
          <InitialsChip initials={row.initials} status={row.status} size={40} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 9, minWidth: 0 }}>
              <span style={{ fontSize: 16, fontWeight: 700, color: 'var(--fg-1)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{row.name}</span>
              <MemberStatusPill status={row.status} compact />
            </div>
            <div className="mono" style={{ marginTop: 3, fontSize: 11, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {emailLabel(row)}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            data-testid="member-drawer-close"
            // No inline `borderRadius`: `.pf-btn` forces `border-radius` with `!important`.
            style={{ flex: 'none', width: 30, height: 30, border: 0, background: 'var(--bg-3)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '18px 24px 26px' }}>
          <div className="label" style={{ marginBottom: 8 }}>
            Access role
          </div>
          {/* Every card disabled: the membership endpoint writes status only. Shown at the
              person's REAL role rather than hidden, so the drawer still answers "what is she
              allowed to do" — it just cannot change the answer. `idPrefix="drawer"` names the
              radio group. */}
          <RoleCards idPrefix="drawer" value={row.role} disabledIds={ACCESS_ROLE_IDS} note={MEMBER_UNBACKED.role} noteId={roleNoteId} />

          {ctx.mode === 'firm' ? (
            <>
              <div className="label" style={{ margin: '22px 0 8px' }}>
                Client access
              </div>
              {/* `'all'` is the honest value, not a fallback: nothing stores client access per
                  person, so everyone in the workspace does see the same clients. */}
              <UnbackedField reason={MEMBER_UNBACKED.clientAccess} noteId={scopeNoteId} dim>
                <ClientAccessPicker idPrefix="drawer" value="all" />
              </UnbackedField>
            </>
          ) : (
            <div style={{ marginTop: 22 }}>
              <UnbackedField reason={MEMBER_UNBACKED.department} noteId={scopeNoteId}>
                <DepartmentField department={null} />
              </UnbackedField>
            </div>
          )}

          {/* BOTH modes — a role staffs people in either workspace now. */}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, margin: '22px 0 9px' }}>
            <div className="label" style={{ flex: 1, minWidth: 0 }}>
              Workflow roles
            </div>
            {/* CreateUpload.tsx:261-262's shape — name the destination tab, then leave. Both
                are state updates in one handler, so React commits the pair together. */}
            <button
              type="button"
              onClick={() => {
                ctx.setSettingsTab('roles')
                onClose()
              }}
              className="pf-btn"
              data-testid="drawer-manage-roles"
              style={{ flex: 'none', padding: 0, border: 0, background: 'none', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 500, color: 'var(--action)' }}
            >
              {MANAGE_ROLES}
            </button>
          </div>
          {/* The visual half of the in-flight lock — UnbackedField's `<fieldset disabled>`
              shape (line 71 above), inlined rather than reused: that component always renders
              a reason note, and a staffing write in flight has none to show. */}
          <fieldset disabled={rolePending} style={{ border: 0, padding: 0, margin: 0, minInlineSize: 0 }}>
            {rolesLanded ? (
              <WorkflowRolePills idPrefix="drawer" roles={ctx.roles} held={heldRoleKeys} onToggle={toggleWorkflowRole} />
            ) : ctx.rolesState === 'error' ? (
              ctx.rolesError && <ErrorState error={ctx.rolesError} onRetry={ctx.refetchRoles} />
            ) : (
              <Loading label="Loading roles…" />
            )}
          </fieldset>
          {roleError && (
            <div
              data-testid="member-drawer-role-error"
              style={{
                marginTop: 8,
                padding: '10px 12px',
                borderRadius: 'var(--radius-md)',
                background: 'var(--status-red-bg)',
                border: '1px solid var(--status-red-border)',
                fontSize: 12.5,
                lineHeight: 1.5,
                color: 'var(--status-red-text)',
              }}
            >
              {roleError.message}
            </div>
          )}
          <div data-testid="drawer-wfrole-helper" style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>
            {drawerRoleHelper(row.role)}
          </div>

          {/* The Activity block is GONE. Last active, Joined and Invited by were three mock
              fields a membership row does not carry, and three em dashes under a heading is a
              worse answer than no heading. */}

          {steps && (
            <>
              <div className="label" style={{ margin: '24px 0 10px' }}>
                Approval involvement
              </div>
              <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '13px 14px' }}>
                <div data-testid="member-steps-named" style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>
                  {stepsNamedLine(steps.total)}
                </div>
                {/* Joined on ' · ', the one separator for lists of names on this tab. */}
                <div style={{ marginTop: 4, fontSize: 12.5, color: 'var(--fg-2)' }}>
                  {steps.policies.map((p) => p.policyName).join(' · ')}
                </div>
                {row.status === 'suspended' && (
                  <AmberNote testId="member-drawer-steps-warning" style={{ marginTop: 10, padding: '9px 11px', fontSize: 11.5 }}>
                    {SUSPENDED_STEPS_NOTE}
                  </AmberNote>
                )}
              </div>
            </>
          )}
        </div>

        {/* §8: "Footer is the danger zone." Both explanations are visible prose beside their buttons. */}
        <div data-testid="member-danger-zone" style={{ flex: 'none', padding: '14px 24px 16px', borderTop: '1px solid var(--line-1)' }}>
          {canSuspend && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 12 }}>
              <div style={{ flex: 1, minWidth: 0, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                {/* SUSPEND ONLY: beside `Reactivate` this sentence would assert the opposite of the button's effect. */}
                {suspending && <span>{SUSPEND_EXPLANATION}</span>}
                {/* Layer (3) for the SUSPEND lock: a disabled control is out of the tab order and `title` never fires on one. */}
                {protectedAdmin && (
                  <div id={noteId} data-testid="member-danger-note" style={{ marginTop: 4, color: 'var(--status-amber-text)' }}>
                    {PROTECTED_ADMIN_NOTE}
                  </div>
                )}
              </div>
              <button
                type="button"
                onClick={() => onStatus(row.id, suspending ? 'suspended' : 'active')}
                disabled={protectedAdmin}
                title={protectedAdmin ? PROTECTED_ADMIN_NOTE : undefined}
                aria-describedby={protectedAdmin ? noteId : undefined}
                className="v2-btn v2-btn-ghost pf-btn"
                data-testid="member-suspend"
                // `disabledGhost` outranks `.v2-btn-ghost:hover`'s unguarded background.
                style={{ flex: 'none', height: 36, ...(protectedAdmin ? disabledGhost : null) }}
              >
                {suspending ? 'Suspend' : 'Reactivate'}
              </button>
            </div>
          )}

          {/* The SERVER's own reason for the refused write, verbatim: a client sentence would
              say it failed and never why. Full width of the footer, in the red triplet. */}
          {statusError?.id === row.id && (
            <div
              data-testid="member-drawer-status-error"
              style={{
                margin: '0 -24px 12px',
                padding: '7px 24px',
                background: 'var(--status-red-bg)',
                borderTop: '1px solid var(--status-red-border)',
                fontSize: 11.5,
                color: 'var(--status-red-text)',
              }}
            >
              {statusError.message}
            </div>
          )}

          {canRemove && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 14, paddingTop: 12, borderTop: '1px solid var(--line-1)' }}>
              <span id={removeNoteId} style={{ flex: 1, minWidth: 0, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                {REMOVE_EXPLANATION}
                <span style={{ display: 'block', marginTop: 4 }}>{MEMBER_UNBACKED.remove}</span>
              </span>
              <button
                type="button"
                disabled
                title={MEMBER_UNBACKED.remove}
                aria-describedby={removeNoteId}
                className="v2-btn v2-btn-ghost pf-btn"
                data-testid="member-remove"
                style={{ flex: 'none', height: 36, color: 'var(--status-red-text)', borderColor: 'var(--status-red-border)', ...disabledGhost }}
              >
                Remove
              </button>
            </div>
          )}
        </div>
      </div>
    </>
  )
}
