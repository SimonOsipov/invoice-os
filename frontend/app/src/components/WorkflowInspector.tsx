// Workflows — the step inspector. One panel per node kind, plus the no-selection state.
//
// Every control funnels through `onPatch(id, patch)`, which the builder turns into
// `updateNode` + `ctx.savePolicy`. The condition panel is the only one that writes two
// keys at once: switching `field` MUST reset `value`, because one slot holds three
// domains (naira number / doc type / boolean) and a stale value from the previous
// domain would make the rule sentence — and the simulator — read nonsense.

import {
  AMOUNT_PRESETS,
  CHANNEL_OPTIONS,
  CUST_OPTIONS,
  DOC_OPTIONS,
  FIELD_OPTIONS,
  isDocType,
  OP_OPTIONS,
  ROLE_OPTIONS,
  ruleText,
  SLA_OPTIONS,
  TARGET_OPTIONS,
  WfAmountInput,
  WfSelect,
  WfToggle,
} from './WorkflowParts'
import type { CondField, CondOp, NodePatch, RoleKey, Sla, WfDocType, WfNode } from '../lib/workflows'

const TITLES: Record<WfNode['type'], string> = {
  approval: 'Approval step',
  condition: 'Condition',
  notify: 'Notification',
  autoapprove: 'Auto-approve',
}

/** The value a condition takes when its field flips domain. */
const FIELD_DEFAULT: Record<CondField, number | WfDocType | boolean> = {
  amount: 100_000_000,
  docType: 'B2B',
  newCustomer: true,
}

export function WorkflowInspector({ node, onPatch, onRemove }: {
  node: WfNode | null
  onPatch: (id: string, patch: NodePatch) => void
  onRemove: (id: string) => void
}) {
  const card = { background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 16, overflow: 'hidden' } as const

  if (!node) {
    return (
      <div style={card}>
        <div style={{ padding: '13px 15px', borderBottom: '1px solid var(--line-1)', fontSize: 13.5, fontWeight: 600 }}>Step details</div>
        <div style={{ padding: '26px 18px', textAlign: 'center', fontSize: 12.5, color: 'var(--fg-3)', lineHeight: 1.6 }}>
          Select a step in the flow to edit who approves and when.
        </div>
      </div>
    )
  }

  const patch = (p: NodePatch) => onPatch(node.id, p)

  return (
    <div style={card}>
      <div style={{ padding: '13px 15px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
        <span style={{ fontSize: 13.5, fontWeight: 600 }}>{TITLES[node.type]}</span>
        <button
          type="button"
          onClick={() => onRemove(node.id)}
          className="pf-btn"
          style={{ flex: 'none', padding: 0, border: 0, background: 'transparent', color: 'var(--status-red-text)', fontSize: 12, fontWeight: 500, fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
        >
          Remove
        </button>
      </div>

      <div style={{ padding: 15 }}>
        {node.type === 'approval' && (
          <>
            <WfSelect label="Who must approve" value={node.role} options={ROLE_OPTIONS} onChange={(v) => patch({ role: v as RoleKey })} marginBottom={14} />
            <WfSelect label="Deadline" value={node.sla} options={SLA_OPTIONS} onChange={(v) => patch({ sla: v as Sla })} marginBottom={14} />
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, padding: '4px 0' }}>
              <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Allow delegation</span>
              <WfToggle on={node.delegate} onToggle={() => patch({ delegate: !node.delegate })} label="Allow delegation" />
            </div>
          </>
        )}

        {node.type === 'condition' && (
          <>
            <WfSelect
              label="If this field"
              value={node.field}
              options={FIELD_OPTIONS}
              onChange={(v) => patch({ field: v as CondField, value: FIELD_DEFAULT[v as CondField] })}
              marginBottom={14}
            />

            {node.field === 'amount' && (
              <>
                <WfSelect label="Is" value={node.op} options={OP_OPTIONS} onChange={(v) => patch({ op: v as CondOp })} marginBottom={12} />
                <WfAmountInput value={typeof node.value === 'number' ? node.value : 0} onChange={(v) => patch({ value: v })} ariaLabel="Threshold amount in naira" marginBottom={10} />
                <div style={{ display: 'flex', gap: 6, marginBottom: 12 }}>
                  {AMOUNT_PRESETS.map((p) => (
                    <button
                      key={p.label}
                      type="button"
                      onClick={() => patch({ value: p.value })}
                      className="pf-btn"
                      style={{ flex: 1, height: 30, border: '1px solid var(--line-2)', background: 'var(--bg-1)', fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 600, color: 'var(--fg-2)', cursor: 'pointer' }}
                    >
                      {p.label}
                    </button>
                  ))}
                </div>
              </>
            )}

            {node.field === 'docType' && (
              <WfSelect label="Equals" value={isDocType(node.value) ? node.value : 'B2B'} options={DOC_OPTIONS} onChange={(v) => patch({ value: v as WfDocType })} marginBottom={12} />
            )}

            {node.field === 'newCustomer' && (
              <WfSelect label="Is" value={String(!!node.value)} options={CUST_OPTIONS} onChange={(v) => patch({ value: v === 'true' })} marginBottom={12} />
            )}

            <div style={{ background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 12, padding: '10px 12px' }}>
              <div className="mono" style={{ fontSize: 9, color: 'var(--fg-3)', letterSpacing: '0.06em', marginBottom: 3 }}>
                RULE
              </div>
              <div style={{ fontSize: 12.5, fontWeight: 600, color: 'var(--fg-1)' }}>{ruleText(node)}</div>
              <div style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 5, lineHeight: 1.5 }}>
                Steps in the IF TRUE lane run only when this is met; otherwise the flow takes the OTHERWISE lane.
              </div>
            </div>
          </>
        )}

        {node.type === 'notify' && (
          <>
            <WfSelect label="Notify" value={node.target} options={TARGET_OPTIONS} onChange={(v) => patch({ target: v })} marginBottom={14} />
            <WfSelect label="Channel" value={node.channel} options={CHANNEL_OPTIONS} onChange={(v) => patch({ channel: v })} />
          </>
        )}

        {node.type === 'autoapprove' && (
          <p style={{ fontSize: 12.5, color: 'var(--fg-2)', lineHeight: 1.6, margin: 0 }}>
            When the flow reaches this step, the invoice clears with no manual sign-off and moves straight to transmission. Use it inside an OTHERWISE lane to fast-track low-risk invoices.
          </p>
        )}
      </div>
    </div>
  )
}
