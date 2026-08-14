// Workflows — approval policies. Two mutually exclusive states behind one view:
// the policy LIST, and the BUILDER for whichever policy `ctx.editingPolicyId` names.
//
// Ported from the Claude Design prototype (Platform.dc.html ~L998-1280 markup,
// ~L2285-2412 logic). Every write goes through a ctx verb that calls the gateway, but only
// on Save draft or Publish: the edited TREE is local to WorkflowBuilder too, alongside its
// transient state (selection, drag, arm, drop hint, scenario inputs, save flash). Nothing
// else in the app reads any of it, and an unsaved tree is discarded when the builder closes.

import { Fragment, useState, type ReactNode } from 'react'

import { EmptyState, ErrorState, Loading, toApiError } from '@invoice-os/api-client'
import { WorkflowBuilder } from './WorkflowBuilder'
import { PolicyStatusPill, wfBranchGlyph, wfCrossGlyph, wfPlusGlyph } from './WorkflowParts'
import { membersSurface } from '../lib/members'
import { policyStanding } from '../lib/policies'
import { policySummary, type Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'

// Arming is not flag-gated — publishing really does open a run. What APPROVALS_ENFORCED
// still gates is whether an open run refuses a transmit, hence the second sentence.
const INTRO =
  'Each policy decides who signs off before an invoice is stamped and transmitted. Steps run top to bottom; conditions split the flow. Publishing a policy opens an approval on every matching invoice. Transmission is not held for approval yet.'

// Two nodes, not one: `EmptyState` takes {title, message}, so the shipped sentence splits
// at its em dash. Module scope, the RolesView.tsx:34-37 shape.
const EMPTY_TITLE = 'No approval policies yet'
const EMPTY_MESSAGE = 'Every invoice transmits as soon as it validates. Create one to require sign-off first.'

export function WorkflowsView({ ctx }: { ctx: PlatformCtx }) {
  const editing = ctx.editingPolicyId ? (ctx.policies.find((p) => p.id === ctx.editingPolicyId) ?? null) : null

  return (
    <div style={{ padding: '30px 36px 56px' }} data-screen-label="Workflow builder">
      {editing ? (
        // Keyed by policy id so opening a different policy starts the builder clean —
        // a node selection or armed step carried across would point into a tree that
        // no longer contains it.
        <WorkflowBuilder key={editing.id} ctx={ctx} policy={editing} />
      ) : (
        <PolicyList ctx={ctx} />
      )}
    </div>
  )
}

function PolicyList({ ctx }: { ctx: PlatformCtx }) {
  const { policies, mode, active } = ctx
  // Copy forks on mode, structure never does — the Rules screen's rule. Policies are
  // held per TENANT, not per client, so firm mode says "across the firm" rather
  // than naming the switched-to company.
  const subtitle =
    mode === 'firm'
      ? 'Who must sign off before an invoice is transmitted — one set of policies across the firm.'
      : `Who must sign off before ${active.short} transmits an invoice.`

  // The ladder lives HERE, below WorkflowsView's `editing ? Builder : List` branch. Above
  // it, a 'loading' arm would tear an open builder off screen on every refetch; the builder
  // renders off the surviving mirror instead. Two accepted consequences: a fetch error
  // while the builder is open surfaces only on return to the list, and a publish's OWN
  // failure is the Publish control's to surface, not this ladder's. Pinned by
  // 'the ladder belongs to the LIST and never reaches an open builder'.
  //
  // `membersSurface`, not a second copy: its 'roster' arm name reads oddly on a policy list,
  // but a `policiesSurface` would be the drift the shared mapper exists to prevent. Its
  // 'idle' arm (no gateway configured) renders the empty card, exactly as Members and Roles
  // already do on that build.
  const surface = membersSurface(ctx.policiesState)
  // Two slots, never one. Create is a screen singleton so its reason is a bare string
  // (RoleModal.tsx:75); delete is row-scoped so its reason is keyed by id
  // (MembersView.tsx:78). One shared slot would let a create failure blank a row's reason.
  const [createError, setCreateError] = useState<string | null>(null)
  const [deleteError, setDeleteError] = useState<{ id: string; message: string } | null>(null)

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 20, flexWrap: 'wrap', marginBottom: 22 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            APPROVAL WORKFLOW
          </div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Approval policies</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{subtitle}</p>
        </div>
        {/* The message rides WITH the button rather than under the header row: that row
            wraps, so on a narrow viewport the button moves and its reason must follow. */}
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 8 }}>
          <button
            type="button"
            onClick={() => {
              setCreateError(null)
              ctx.createPolicy().catch((err: unknown) => setCreateError(toApiError(err).message))
            }}
            className="v2-btn pf-btn"
            style={{ height: 36, padding: '0 16px', fontSize: 13, background: 'var(--action)', color: 'var(--text-on-dark)', gap: 7 }}
          >
            <span style={{ display: 'inline-flex' }}>{wfPlusGlyph}</span> New policy
          </button>
          {createError && <PolicyError testId="policy-create-error">{createError}</PolicyError>}
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 20, marginBottom: 14 }}>
        <p style={{ fontSize: 13.5, color: 'var(--fg-2)', maxWidth: 620, lineHeight: 1.55, margin: 0 }}>{INTRO}</p>
        {/* Roster arm only: `0 POLICIES` beside a spinner or beside ErrorState is the same
            "an errored fetch reads as an empty workspace" claim the ladder exists to kill. */}
        {surface === 'roster' && (
          <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>
            {policies.length} POLICIES
          </span>
        )}
      </div>

      {surface === 'loading' && <Loading label="Loading approval policies…" />}

      {surface === 'error' && ctx.policiesError && <ErrorState error={ctx.policiesError} onRetry={ctx.refetchPolicies} />}

      {surface === 'empty' && (
        <div data-testid="policies-empty">
          <EmptyState title={EMPTY_TITLE} message={EMPTY_MESSAGE} />
        </div>
      )}

      {surface === 'roster' && (
        <div data-testid="policies-list" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {policies.map((p) => (
            // A Fragment, not a wrapper div: the column's `gap: 10` spaces the message and
            // `.pf-row` stays the row hook the topology spec counts on.
            <Fragment key={p.id}>
              <PolicyRow
                policy={p}
                onEdit={() => ctx.openPolicy(p.id)}
                onDelete={() => {
                  setDeleteError(null)
                  ctx.deletePolicy(p.id).catch((err: unknown) => setDeleteError({ id: p.id, message: toApiError(err).message }))
                }}
              />
              {/* OUTSIDE `.pf-row`: the row's whole box carries `onClick={onEdit}`, so a
                  message nested inside it would open the builder when clicked. */}
              {deleteError?.id === p.id && <PolicyError testId="policy-delete-error">{deleteError.message}</PolicyError>}
            </Fragment>
          ))}
        </div>
      )}
    </>
  )
}

// The gateway's own sentence for a write it refused, verbatim. Inline rather than shared:
// MembersTable.tsx:274-289, MemberDrawer.tsx:365-379 and RoleModal.tsx:298-305 each carry
// their own copy, so a local one is the convention, not a fourth divergence.
function PolicyError({ testId, children }: { testId: string; children: ReactNode }) {
  return (
    <div
      data-testid={testId}
      style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}
    >
      {children}
    </div>
  )
}

function PolicyRow({ policy, onEdit, onDelete }: { policy: Policy; onEdit: () => void; onDelete: () => void }) {
  return (
    // pf-ROW, not pf-btn: the prototype markup says pf-btn, but in this repo that
    // class forces `border-radius: var(--radius-pill) !important`, which would round
    // a 72px-tall row into a stadium. pf-row is the repo's clickable-row hover.
    <div
      className="pf-row"
      onClick={onEdit}
      style={{ display: 'flex', alignItems: 'center', gap: 15, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 14, padding: '15px 18px', cursor: 'pointer' }}
    >
      <span style={{ flex: 'none', width: 40, height: 40, borderRadius: 9, background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center' }}>{wfBranchGlyph}</span>

      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
          <span style={{ fontSize: 14.5, fontWeight: 600 }}>{policy.name}</span>
          <PolicyStatusPill status={policy.status} />
        </div>
        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 4 }}>
          {policy.scope} · {policySummary(policy)}
        </div>
      </div>

      <div className="mono" style={{ flex: 'none', fontSize: 10, color: 'var(--fg-4)' }}>{policyStanding(policy)}</div>

      <button
        type="button"
        onClick={(e) => {
          // The whole row navigates; editing is what a plain click already does.
          e.stopPropagation()
          onEdit()
        }}
        className="pf-btn"
        style={{ flex: 'none', height: 34, padding: '0 15px', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-1)', fontSize: 13, fontWeight: 500, fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
      >
        Edit
      </button>

      {/* No confirmation step: the prototype deletes outright and the Rules screen's
          own remove action does the same, so adding one here would be this screen
          inventing a pattern the app does not have. Flagged in the port notes. */}
      <button
        type="button"
        aria-label={`Delete ${policy.name}`}
        title={`Delete ${policy.name}`}
        onClick={(e) => {
          e.stopPropagation()
          onDelete()
        }}
        className="pf-btn"
        style={{ flex: 'none', display: 'grid', placeItems: 'center', width: 32, height: 34, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-4)', cursor: 'pointer' }}
      >
        {wfCrossGlyph}
      </button>
    </div>
  )
}
