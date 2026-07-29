// Workflows — "Test a scenario". Runs the policy against one hypothetical invoice and
// lists the steps that would actually fire.
//
// The scenario is scratch input, NOT policy data: it never goes near savePolicy, so
// dialling the amount cannot demote a published policy to draft.
//
// An `autoapprove` emits a step of its own (a ✓ row, "NO SIGN-OFF NEEDED") as well as
// setting the sticky `auto` flag on the summary line, matching the prototype. The flag
// is not redundant with the row: `auto` stays set for the whole result, so the summary
// still says the path was auto-approved even when later steps follow the ✓.

import { NODE_TONE, DOC_OPTIONS, simSub, simTitle, WfAmountInput, WfSelect, WfToggle } from './WorkflowParts'
import { simulate, type Policy, type SimContext, type WfDocType } from '../lib/workflows'

export function WorkflowSimulator({ policy, sim, onSim }: {
  policy: Policy
  sim: SimContext
  onSim: (next: SimContext) => void
}) {
  const result = simulate(policy, sim)
  const approvals = result.steps.filter((s) => s.node.type === 'approval').length

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
        <WfAmountInput value={sim.amount} onChange={(amount) => onSim({ ...sim, amount })} ariaLabel="Scenario invoice amount in naira" marginBottom={8} />

        <div style={{ display: 'flex', alignItems: 'flex-end', gap: 10, marginBottom: 14 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <WfSelect label="Doc type" value={sim.docType} options={DOC_OPTIONS} onChange={(v) => onSim({ ...sim, docType: v as WfDocType })} height={36} />
          </div>
          <div style={{ flex: 'none', display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
            <div className="label">New customer</div>
            <div style={{ marginTop: 9 }}>
              <WfToggle on={sim.newCustomer} onToggle={() => onSim({ ...sim, newCustomer: !sim.newCustomer })} label="Scenario is a new customer" />
            </div>
          </div>
        </div>

        <div style={{ borderTop: '1px solid var(--line-1)', paddingTop: 13 }}>
          <div className="mono" style={{ fontSize: 10, fontWeight: 600, color: summaryColor, letterSpacing: '0.04em', marginBottom: 11 }}>
            {summary}
            {result.auto && ' · AUTO-APPROVED PATH'}
          </div>

          {result.steps.length > 0 && (
            <div style={{ display: 'flex', flexDirection: 'column' }}>
              {result.steps.map((s, i) => {
                const n = s.node
                const isApproval = n.type === 'approval'
                if (isApproval) approvalSeen += 1
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
                      <div style={{ fontSize: 12.5, fontWeight: 600, lineHeight: 1.2 }}>{simTitle(n)}</div>
                      <div className="mono" style={{ fontSize: 9.5, color: 'var(--fg-3)', letterSpacing: '0.03em', marginTop: 2 }}>
                        {simSub(n)}
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
