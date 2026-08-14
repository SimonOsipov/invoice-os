// Workflows — "Test a scenario". Runs the policy against one hypothetical invoice and
// lists the steps that would actually fire. The amount is the WHOLE scenario: `CondField`
// has one domain, so a second input would take a value and change no rendered outcome.
//
// The scenario is scratch input, NOT policy data: it never goes near savePolicy, so
// dialling the amount cannot demote a published policy to draft.
//
// An `autoapprove` emits a step of its own (a ✓ row, "NO SIGN-OFF NEEDED") as well as
// setting the sticky `auto` flag on the summary line, matching the prototype. The flag
// is not redundant with the row: `auto` stays set for the whole result, so the summary
// still says the path was auto-approved even when later steps follow the ✓.

import { NODE_TONE, simSub, simTitle, WfAmountInput } from './WorkflowParts'
import type { Resolved, Role } from '../lib/roles'
import { simulate, type Policy, type RoleKey, type SimContext } from '../lib/workflows'

export function WorkflowSimulator({ policy, roles, sim, onSim, resolve }: {
  policy: Policy
  roles: readonly Role[]
  sim: SimContext
  onSim: (next: SimContext) => void
  /**
   * The canvas's prop, same shape: `roles` alone cannot say who holds a seat today, and only
   * `WorkflowBuilder` may reach for the roster. This component never learns one exists.
   */
  resolve: (position: RoleKey) => Resolved
}) {
  const result = simulate(policy, sim)
  const approvals = result.steps.filter((s) => s.node.type === 'approval').length
  // Gated, never unconditional: a claim about notify delivery on a path holding no notify step
  // would be a new false statement.
  const notified = result.steps.some((s) => s.node.type === 'notify')

  const summary =
    approvals === 0
      ? 'NO APPROVAL NEEDED — TRANSMITS IMMEDIATELY'
      : `${approvals} APPROVAL${approvals === 1 ? '' : 'S'} REQUIRED`
  const summaryColor = approvals === 0 ? 'var(--status-green-text)' : 'var(--action)'

  let approvalSeen = 0

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 16, overflow: 'hidden' }}>
      <div style={{ padding: '13px 15px', borderBottom: '1px solid var(--line-1)', fontSize: 13.5, fontWeight: 600 }}>Test a scenario</div>

      <div style={{ padding: 15 }}>
        <div className="label" style={{ marginBottom: 6 }}>
          Invoice amount
        </div>
        {/* 14, not 8: the removed scenario row carried the clearance above the divider, and
            the last element inherits it. */}
        <WfAmountInput value={sim.amount} onChange={(amount) => onSim({ ...sim, amount })} ariaLabel="Scenario invoice amount in naira" marginBottom={14} />

        <div style={{ borderTop: '1px solid var(--line-1)', paddingTop: 13 }}>
          <div className="mono" style={{ fontSize: 10, fontWeight: 600, color: summaryColor, letterSpacing: '0.04em', marginBottom: 11 }}>
            {summary}
            {result.auto && ' · AUTO-APPROVED PATH'}
          </div>

          {/* Above the step list because it explains the notify rows below it — pinned by T5-12.
              Hint register, never a fourth mono verdict line. */}
          {notified && (
            <div data-testid="sim-notify-not-delivered" style={{ fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)', marginBottom: 12 }}>
              This path reaches a notify step — the target and channel are recorded, but no message goes out yet.
            </div>
          )}

          {result.steps.length > 0 && (
            <div style={{ display: 'flex', flexDirection: 'column' }}>
              {result.steps.map((s, i) => {
                const n = s.node
                const isApproval = n.type === 'approval'
                if (isApproval) approvalSeen += 1
                const res = n.type === 'approval' ? resolve(n.role) : null
                const tone = NODE_TONE[n.type]
                const notLast = i < result.steps.length - 1
                return (
                  <div key={n.id} style={{ display: 'flex', alignItems: 'flex-start', gap: 10, position: 'relative', paddingBottom: 14 }}>
                    {notLast && (
                      <>
                        <span style={{ position: 'absolute', left: 11, top: 26, bottom: 2, width: 1.5, background: 'var(--line-2)', borderRadius: 2 }} />
                        <span style={{ position: 'absolute', left: 8, bottom: 0, width: 0, height: 0, borderLeft: '4px solid transparent', borderRight: '4px solid transparent', borderTop: '5px solid var(--line-2)' }} />
                      </>
                    )}
                    <span
                      className="mono"
                      style={{ flex: 'none', width: 24, height: 24, borderRadius: 99, display: 'grid', placeItems: 'center', background: tone.bg, color: tone.color, fontSize: 11, fontWeight: 700 }}
                    >
                      {isApproval ? approvalSeen : n.type === 'autoapprove' ? '✓' : '·'}
                    </span>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ fontSize: 12.5, fontWeight: 600, lineHeight: 1.2 }}>{simTitle(n, roles)}</div>
                      <div className="mono" style={{ fontSize: 9.5, color: res?.warn ? 'var(--status-amber-text)' : 'var(--fg-3)', letterSpacing: '0.03em', marginTop: 2 }}>
                        {simSub(n, roles, res?.text)}
                      </div>
                    </div>
                    {n.type === 'approval' && (
                      <span className="mono" style={{ flex: 'none', fontSize: 9, color: 'var(--fg-3)' }}>
                        {n.sla === '0' ? 'NO SLA' : `${n.sla}H`}
                      </span>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
