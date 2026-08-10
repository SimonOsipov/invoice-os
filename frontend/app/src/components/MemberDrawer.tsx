// §8's member drawer — a 560px right drawer, the app's fifth overlay.
//
// Structurally `RuleDrawer` (RuleDrawer.tsx:23-46): scrim, `role="dialog"`, `aria-modal`,
// the `pfDrawer` animation and the `pf-drawer` class. That class is taken for a concrete
// reason and not as decoration — its ONLY rule is `width: 100vw !important` under a
// max-width media query (platform.css:259-261), so omitting it silently deletes the mobile
// collapse.
//
// TWO deliberate departures from that shell.
//
// (1) THE PANEL IS `--bg-2`, WITH `--bg-1` BANDS — RuleDrawer has it the other way round.
// Every control this drawer reuses was drawn for a `--bg-2` ground: `RoleCards` paints its
// unselected cards `--bg-1` (MemberParts.tsx, and its docblock says so explicitly),
// `WfSelect` paints its box `--bg-1` (WorkflowParts.tsx:217) and `ClientAccessPicker`'s
// panel is `--bg-1` too. On RuleDrawer's `--bg-1` body all three would be ground-on-ground.
// Fixed at the CALLER, per the architect: `RoleCards` is not edited, and the same
// `--bg-2` card with a `--bg-1` band relationship already ships eight inches away on this
// very tab (MembersTable.tsx's card and its head row).
//
// (2) IT CLOSES ON ESCAPE. `RuleDrawer` registers no keydown listener at all — it predates
// `useDismiss` — and AC#1 requires one. `useDismiss(true, onClose)` with no `outsideRef`:
// the scrim's own `onClick` is the outside click, which is the call shape useDismiss.ts:20-21
// pre-authorises for this drawer by name.
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
 * The two controls with no `disabled` prop of their own — `ClientAccessPicker` holds state,
 * `DepartmentField` wraps the Workflows-owned `WfSelect`. A `<fieldset disabled>` gives all
 * four layers without plumbing a prop through a shared component for a Members-only need:
 * HTML natively disables every descendant (1, unclickable and out of the tab order),
 * `pointerEvents: none` is a stronger (2) than any background swap, the sibling below is
 * (3), and `aria-describedby` on the fieldset is (4). No `<fieldset>` precedent exists in
 * `frontend/` — accepted over mutating `WfSelect`. `minInlineSize: 0` is mandatory: a
 * fieldset defaults to `min-content` and would refuse to shrink inside a 560px drawer.
 */
function UnbackedField({ reason, noteId, children }: { reason: string; noteId: string; children: ReactNode }) {
  return (
    <>
      <fieldset
        disabled
        aria-describedby={noteId}
        style={{ border: 0, padding: 0, margin: 0, minInlineSize: 0, pointerEvents: 'none' }}
      >
        {children}
      </fieldset>
      <div id={noteId} style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
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

  const disabledGhost = { background: 'transparent', borderColor: 'var(--line-1)', color: 'var(--fg-4)', cursor: 'not-allowed' } as const

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
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.32)', animation: 'pfFade 160ms ease-out' }} />
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
          background: 'var(--bg-2)',
          borderLeft: '1px solid var(--line-2)',
          boxShadow: '-24px 0 48px -24px oklch(20% .02 210 / 0.3)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'pfDrawer 200ms ease-out',
        }}
      >
        {/* Header — AC#2. NOT an <h1>: SettingsView.tsx:49-53 owns the page heading and a
            second one inside a tab would give the page two. */}
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <InitialsChip initials={row.initials} status={row.status} size={40} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 9, flexWrap: 'wrap', marginBottom: 3 }}>
              <span style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg-1)', wordBreak: 'break-word' }}>{row.name}</span>
              <MemberStatusPill status={row.status} />
            </div>
            <div className="mono" style={{ fontSize: 11.5, color: 'var(--fg-3)', wordBreak: 'break-all' }}>
              {emailLabel(row)}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            data-testid="member-drawer-close"
            // No inline `borderRadius`: `.pf-btn` is `border-radius: var(--radius-pill)
            // !important` (app-layer.css:194-197), so RuleDrawer.tsx:63's `--radius-input`
            // here would never apply. The pill is wanted on a 30px icon button.
            style={{ flex: 'none', width: 30, height: 30, border: 0, background: 'var(--bg-3)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          <div className="label" style={{ marginBottom: 6 }}>
            Access role
          </div>
          {/* Every card disabled: the membership endpoint writes status only. Shown at the
              person's REAL role rather than hidden, so the drawer still answers "what is she
              allowed to do" — it just cannot change the answer. `idPrefix="drawer"` names the
              radio group. */}
          <RoleCards idPrefix="drawer" value={row.role} disabledIds={ACCESS_ROLE_IDS} note={MEMBER_UNBACKED.role} noteId={roleNoteId} />

          {ctx.mode === 'firm' ? (
            <>
              <div className="label" style={{ margin: '20px 0 6px' }}>
                Client access
              </div>
              {/* `'all'` is the honest value, not a fallback: nothing stores client access per
                  person, so everyone in the workspace does see the same clients. */}
              <UnbackedField reason={MEMBER_UNBACKED.clientAccess} noteId={scopeNoteId}>
                <ClientAccessPicker idPrefix="drawer" value="all" />
              </UnbackedField>
            </>
          ) : (
            <div style={{ marginTop: 20 }}>
              <UnbackedField reason={MEMBER_UNBACKED.department} noteId={scopeNoteId}>
                <DepartmentField department={null} />
              </UnbackedField>
            </div>
          )}

          {/* BOTH modes — a role staffs people in either workspace now. */}
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, margin: '20px 0 6px' }}>
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
              style={{ flex: 'none', padding: 0, border: 0, background: 'none', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 600, color: 'var(--action)' }}
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
          <div data-testid="drawer-wfrole-helper" style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
            {drawerRoleHelper(row.role)}
          </div>

          {/* The Activity block is GONE. Last active, Joined and Invited by were three mock
              fields a membership row does not carry, and three em dashes under a heading is a
              worse answer than no heading. */}

          {steps && (
            <>
              <div className="label" style={{ margin: '20px 0 6px' }}>
                Approval involvement
              </div>
              <div data-testid="member-steps-named" style={{ fontSize: 13, color: 'var(--fg-1)' }}>
                {stepsNamedLine(steps.total)}
              </div>
              {/* Joined on ' · ', the same way MembersView joins the unassigned role titles —
                  one separator for lists of names on this tab. */}
              <div style={{ marginTop: 3, fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                {steps.policies.map((p) => p.policyName).join(' · ')}
              </div>
              {row.status === 'suspended' && (
                <AmberNote testId="member-drawer-steps-warning" style={{ marginTop: 10 }}>
                  {SUSPENDED_STEPS_NOTE}
                </AmberNote>
              )}
            </>
          )}
        </div>

        {/* §8: "Footer is the danger zone." Its copy is the most important text in the story,
            so both explanations are rendered as visible prose beside their buttons rather than
            hidden behind a hover. */}
        <div
          data-testid="member-danger-zone"
          style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-1)', display: 'flex', flexDirection: 'column', gap: 12 }}
        >
          {canSuspend && (
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
              <button
                type="button"
                onClick={() => onStatus(row.id, suspending ? 'suspended' : 'active')}
                disabled={protectedAdmin}
                title={protectedAdmin ? PROTECTED_ADMIN_NOTE : undefined}
                aria-describedby={protectedAdmin ? noteId : undefined}
                className="v2-btn v2-btn-ghost pf-btn"
                data-testid="member-suspend"
                // Layers (1) and (2) of the four-layer disabled treatment: the real attribute,
                // plus an inline swap that outranks `.v2-btn-ghost:hover`'s unguarded
                // `background: var(--muted)` (app-layer.css) so a dead button stops reacting
                // to the pointer. (3) is the visible note below, (4) the title and
                // aria-describedby above — additions to it, never its replacement.
                style={{ flex: 'none', minWidth: 118, justifyContent: 'center', height: 36, fontSize: 13, ...(protectedAdmin ? disabledGhost : null) }}
              >
                {suspending ? 'Suspend' : 'Reactivate'}
              </button>
              {/* SUSPEND ONLY. The sentence describes what suspension DOES, so beside
                  `Reactivate` it asserted the opposite of the button's effect. Nothing takes
                  its place — an invented reactivate sentence is the one outcome worse than an
                  absent one. The button keeps its `minWidth`, so the Remove row stays aligned. */}
              {suspending && <span style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>{SUSPEND_EXPLANATION}</span>}
            </div>
          )}

          {/* The SERVER's own reason for the write it just refused, beside the control that
              asked for it. Verbatim, no prefix: a client sentence here would tell the user
              the write failed and never why. */}
          {statusError?.id === row.id && (
            <div
              data-testid="member-drawer-status-error"
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
          )}

          {canRemove && (
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
              <button
                type="button"
                disabled
                title={MEMBER_UNBACKED.remove}
                aria-describedby={removeNoteId}
                className="pf-btn"
                data-testid="member-remove"
                // RuleDrawer's own footer treatment (RuleDrawer.tsx:133-142) — same surface
                // type, same band, same tokens. Its inline `borderRadius: var(--radius-sm)`
                // is NOT copied: `.pf-btn` forces the pill with `!important`, so that
                // declaration has never applied and carrying it forward would propagate a
                // false claim.
                style={{
                  flex: 'none',
                  minWidth: 118,
                  height: 36,
                  padding: '0 14px',
                  fontFamily: 'var(--font-sans)',
                  fontSize: 13,
                  fontWeight: 600,
                  border: '1px solid var(--line-1)',
                  ...disabledGhost,
                }}
              >
                Remove
              </button>
              {/* Layer (3), and the reason the two-step confirm went with the button: there
                  is nothing to confirm. The first sentence still says what removal WOULD do;
                  the second says why it cannot be asked for yet. */}
              <span id={removeNoteId} style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                {REMOVE_EXPLANATION}
                <span style={{ display: 'block', marginTop: 4 }}>{MEMBER_UNBACKED.remove}</span>
              </span>
            </div>
          )}

          {/* Layer (3) for the SUSPEND lock — the only layer a screenshot, a keyboard user and
              a text assertion can all reach, since a disabled control is out of the tab order
              and `title` never fires on one in Chromium. */}
          {protectedAdmin && (
            <div id={noteId} data-testid="member-danger-note" style={{ fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
              {PROTECTED_ADMIN_NOTE}
            </div>
          )}
        </div>
      </div>
    </>
  )
}
