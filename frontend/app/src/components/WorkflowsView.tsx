// Workflows — approval policies. Two mutually exclusive states behind one view:
// the policy LIST, and the BUILDER for whichever policy `ctx.editingPolicyId` names.
//
// Ported from the Claude Design prototype (Platform.dc.html ~L998-1280 markup,
// ~L2285-2412 logic). Every write goes through a ctx verb that calls the gateway; all
// builder-transient state (selection, drag, arm, drop hint, scenario inputs, save flash)
// is local to WorkflowBuilder — nothing else in the app reads it.

import { WorkflowBuilder } from './WorkflowBuilder'
import { PolicyStatusPill, wfBranchGlyph, wfCrossGlyph, wfPlusGlyph } from './WorkflowParts'
import { policyStanding } from '../lib/policies'
import { policySummary, type Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'

const INTRO =
  'Each policy decides who signs off before an invoice is stamped and transmitted. Steps run top to bottom; conditions split the flow. Publishing a policy applies it to every matching invoice.'

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
  // held per WORKSPACE, not per client, so firm mode says "across the firm" rather
  // than naming the switched-to company.
  const subtitle =
    mode === 'firm'
      ? 'Who must sign off before an invoice is transmitted — one set of policies across the firm.'
      : `Who must sign off before ${active.short} transmits an invoice.`

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
        <button
          type="button"
          onClick={() => void ctx.createPolicy()}
          className="v2-btn pf-btn"
          style={{ height: 36, padding: '0 16px', fontSize: 13, background: 'var(--action)', color: 'var(--text-on-dark)', gap: 7 }}
        >
          <span style={{ display: 'inline-flex' }}>{wfPlusGlyph}</span> New policy
        </button>
      </div>

      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 20, marginBottom: 14 }}>
        <p style={{ fontSize: 13.5, color: 'var(--fg-2)', maxWidth: 620, lineHeight: 1.55, margin: 0 }}>{INTRO}</p>
        <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>
          {policies.length} POLICIES
        </span>
      </div>

      {policies.length === 0 ? (
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '26px 20px', fontSize: 13, lineHeight: 1.6, color: 'var(--fg-3)' }}>
          No approval policies yet — every invoice transmits as soon as it validates. Create one to require sign-off first.
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {policies.map((p) => (
            <PolicyRow key={p.id} policy={p} onEdit={() => ctx.openPolicy(p.id)} onDelete={() => void ctx.deletePolicy(p.id)} />
          ))}
        </div>
      )}
    </>
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
