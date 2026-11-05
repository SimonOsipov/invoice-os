// Workflows — approval policies. Two mutually exclusive states behind one view:
// the policy LIST, and the BUILDER for whichever policy `ctx.editingPolicyId` names.
//
// Ported from the Claude Design prototype (Platform.dc.html ~L998-1280 markup,
// ~L2285-2412 logic). Every write goes through a ctx verb that calls the gateway, but only
// on Save draft or Publish: the edited TREE is local to WorkflowBuilder too, alongside its
// transient state (selection, drag, arm, drop hint, scenario input, save flash). Nothing
// else in the app reads any of it, and an unsaved tree is discarded when the builder closes.

import { Fragment, useState, type ReactNode } from 'react'

import { EmptyState, ErrorState, Loading, toApiError } from '@invoice-os/api-client'
import { WorkflowBuilder } from './WorkflowBuilder'
import { DISABLED_GHOST, PolicyStatusPill, wfBranchGlyph, wfCrossGlyph, wfDownGlyph, wfPlusGlyph, wfUpGlyph } from './WorkflowParts'
import { membersSurface } from '../lib/members'
import { policyStanding } from '../lib/policies'
import { listLock, moveOrder, policySummary, scopeSummary, type Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'

const INTRO =
  'Each policy decides who signs off before an invoice is stamped and transmitted. Steps run top to bottom; conditions split the flow. Publishing a policy opens an approval on every matching invoice.'

// Two nodes, not one: the shipped sentence splits at its em dash.
const EMPTY_TITLE = 'No approval policies yet'
const EMPTY_MESSAGE = 'Every invoice transmits as soon as it validates. Create one to require sign-off first.'

const CATCH_ALL_REASON = 'This policy decides every document no other policy matches. It stays last, and cannot be moved or deleted, while any policy has scopes.'
const CATCH_ALL_REASON_ID = 'policy-catch-all-reason-text'

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
  // A third slot, row-keyed like delete. A refusal also refetches, so the next Move starts from the server's list.
  const [reorderError, setReorderError] = useState<{ id: string; message: string } | null>(null)
  const [reordering, setReordering] = useState(false)
  const lock = listLock(policies)

  const move = async (id: string, dir: 'up' | 'down') => {
    const order = moveOrder(policies, id, dir)
    if (!order) return
    setReorderError(null)
    setReordering(true)
    try {
      await ctx.reorderPolicies(order)
    } catch (err) {
      setReorderError({ id, message: toApiError(err).message })
      ctx.refetchPolicies()
    } finally {
      setReordering(false)
    }
  }

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 20, flexWrap: 'wrap', marginBottom: 22 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            APPROVAL WORKFLOW
          </div>
          <h1 style={{ fontSize: 26, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Approval policies</h1>
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
            style={{ height: 36, padding: '0 16px', fontSize: 13, background: 'var(--action)', color: 'var(--primary-foreground)', gap: 7 }}
          >
            <span style={{ display: 'inline-flex' }}>{wfPlusGlyph}</span> New policy
          </button>
          {createError && <PolicyError testId="policy-create-error" maxWidth={320}>{createError}</PolicyError>}
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 20, marginBottom: 14 }}>
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
          <EmptyState messageMaxWidth={360} title={EMPTY_TITLE} message={EMPTY_MESSAGE} />
        </div>
      )}

      {surface === 'roster' && (
        <div data-testid="policies-list" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {policies.map((p, i) => (
            // A Fragment, not a wrapper div: the column's `gap: 10` spaces the message and
            // `.pf-row` stays the row hook the topology spec counts on.
            <Fragment key={p.id}>
              <PolicyRow
                policy={p}
                index={i}
                lock={lock}
                count={policies.length}
                reordering={reordering}
                onMove={(dir) => void move(p.id, dir)}
                onEdit={() => ctx.openPolicy(p.id)}
                onDelete={() => {
                  setDeleteError(null)
                  ctx.deletePolicy(p.id).catch((err: unknown) => setDeleteError({ id: p.id, message: toApiError(err).message }))
                }}
              />
              {/* OUTSIDE `.pf-row`: the row's whole box carries `onClick={onEdit}`, so a
                  message nested inside it would open the builder when clicked. */}
              {i === policies.length - 1 && lock.anyScoped && (
                <div id={CATCH_ALL_REASON_ID} data-testid="policy-catch-all-reason" style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                  {CATCH_ALL_REASON}
                </div>
              )}
              {reorderError?.id === p.id && <PolicyError testId="policy-reorder-error">{reorderError.message}</PolicyError>}
              {deleteError?.id === p.id && <PolicyError testId="policy-delete-error">{deleteError.message}</PolicyError>}
            </Fragment>
          ))}
        </div>
      )}
    </>
  )
}

// The gateway's own sentence for a write it refused, verbatim. Inline rather than shared:
// MembersTable, MemberDrawer and RoleModal each carry their own copy.
function PolicyError({ testId, maxWidth, children }: { testId: string; maxWidth?: number; children: ReactNode }) {
  return (
    <div
      data-testid={testId}
      style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, lineHeight: 1.5, color: 'var(--status-red-text)', maxWidth }}
    >
      {children}
    </div>
  )
}

type RowProps = {
  policy: Policy
  index: number
  count: number
  lock: ReturnType<typeof listLock>
  reordering: boolean
  onMove: (dir: 'up' | 'down') => void
  onEdit: () => void
  onDelete: () => void
}

function PolicyRow({ policy, index, count, lock, reordering, onMove, onEdit, onDelete }: RowProps) {
  // A bound reads "no reason"; a lock reads the catch-all sentence.
  const moveUpOk = lock.moveUp(index)
  const moveDownOk = lock.moveDown(index)
  const deleteOk = lock.deletable(index)
  const locked = (ok: boolean, boundary: boolean) => !ok && !boundary
  const iconBtn = (label: string, glyph: ReactNode, disabled: boolean, reason: boolean, onClick: () => void) => (
    <button
      type="button"
      aria-label={label}
      title={reason ? CATCH_ALL_REASON : label}
      aria-describedby={reason ? CATCH_ALL_REASON_ID : undefined}
      disabled={disabled}
      onClick={onClick}
      className="v2-btn v2-btn-ghost pf-btn"
      style={{ flex: 'none', width: 34, height: 34, padding: 0, justifyContent: 'center', color: 'var(--fg-4)', ...(disabled ? DISABLED_GHOST : {}) }}
    >
      {glyph}
    </button>
  )
  // Draft scopes are not on the wire for a published row: mark the line instead.
  const draft = policy.activeVersion !== null && policy.version !== policy.activeVersion
  const summary = `${draft ? 'Draft: ' : ''}${scopeSummary(policy.scopes)} · ${policySummary(policy)}`
  return (
    // pf-ROW, not pf-btn: the prototype markup says pf-btn, but in this repo that
    // class forces `border-radius` with `!important`. pf-row is the repo's clickable-row hover.
    <div
      className="pf-row"
      onClick={onEdit}
      style={{ display: 'flex', alignItems: 'center', gap: 15, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '15px 18px', cursor: 'pointer' }}
    >
      {/* A span: the standing is found as the row's only mono child `div`. */}
      <span className="mono" data-testid="policy-position" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>{index + 1}</span>

      <span style={{ flex: 'none', width: 40, height: 40, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center' }}>{wfBranchGlyph}</span>

      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
          <span style={{ fontSize: 14.5, fontWeight: 600 }}>{policy.name}</span>
          <PolicyStatusPill status={policy.status} />
        </div>
        <div className="mono" data-testid="policy-summary" title={summary} style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 4, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {summary}
        </div>
      </div>

      <div className="mono" style={{ flex: 'none', fontSize: 10.5, color: 'var(--fg-3)' }}>{policyStanding(policy)}</div>

      {/* One wrapper stops every button click, enabled or not, from reaching the row's onEdit. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 15, flex: 'none' }} onClick={(e) => e.stopPropagation()}>
        {iconBtn(`Move ${policy.name} up`, wfUpGlyph, reordering || !moveUpOk, locked(moveUpOk, index === 0), () => onMove('up'))}
        {iconBtn(`Move ${policy.name} down`, wfDownGlyph, reordering || !moveDownOk, locked(moveDownOk, index === count - 1), () => onMove('down'))}

        <button type="button" onClick={onEdit} className="v2-btn v2-btn-ghost pf-btn" style={{ flex: 'none', height: 34, padding: '0 15px', fontSize: 13 }}>
          Edit
        </button>

        {/* No confirmation step: the Rules screen's remove action deletes outright too. */}
        {iconBtn(`Delete ${policy.name}`, wfCrossGlyph, !deleteOk, !deleteOk, onDelete)}
      </div>
    </div>
  )
}
