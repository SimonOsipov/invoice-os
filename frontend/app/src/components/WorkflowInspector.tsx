// Workflows — the step inspector. One panel per node kind, plus the no-selection state.
//
// Every control funnels through `onPatch(id, patch)`, which the builder turns into
// `updateNode` on its LOCAL working tree — no control here reaches the network; Save draft
// and Publish are the only writes.

import {
  AMOUNT_PRESETS,
  CHANNEL_OPTIONS,
  FIELD_OPTIONS,
  OP_OPTIONS,
  ruleText,
  slaOptions,
  toOptions,
  WfAmountInput,
  WfSelect,
  WfToggle,
  type WfOption,
} from './WorkflowParts'
import type { Resolved } from '../lib/roles'
import type { CondField, CondOp, NodePatch, RoleKey, Sla, WfNode } from '../lib/workflows'

const TITLES: Record<WfNode['type'], string> = {
  approval: 'Approval step',
  condition: 'Condition',
  notify: 'Notification',
  autoapprove: 'Auto-approve',
}

// `delegateTo` has no "unset" value a <select> can emit — `WfSelect` is `value: string` — so
// the default is a SENTINEL option valued `''`, the idiom the invite modal's `NO_WF_ROLE`
// uses for the same reason. `''` and absent both mean "anyone", so nothing maps it back out:
// toggling delegation off and on leaves the key present as `''`, still the default.
const ANY_APPROVER = ''

// Deliberately NOT the same wording as the option above it: the option names the fallback,
// the note states the eligibility rule — do not harmonise.
const DELEGATE_NOTE = 'Only Admins and Reviewers can be a delegate. Delegation is not available yet.'

// The delegation window: `delegate`/`delegateTo` are dropped on write (lib/policies.ts:145) and
// forced false on read (:131), so both controls ship SHUT with all four layers and this sentence
// is layer 3. A third register again — the option names the fallback, DELEGATE_NOTE states the
// eligibility rule, this states the disable and its cause.
const DELEGATION_BLOCKED = 'Delegation is switched off — the server has nowhere to store it yet.'

// ONE reason node, TWO `aria-describedby` pointers: the toggle and the picker share it. A
// deliberate deviation from INVED-02, where every disabled control gets its own id
// (InvoiceDetail.tsx:150-159) — there the causes differ per control, here one cause shuts both,
// and a second copy of the sentence would put two matches under one `getAllByText`.
const DELEGATION_BLOCKED_ID = 'delegation-blocked-reason-text'

/** The read-only hint under a select — the typography MemberParts' Reviewer hint already uses. */
function hintStyle(amber = false) {
  return { marginTop: 6, fontSize: 11.5, lineHeight: 1.45, color: amber ? 'var(--status-amber-text)' : 'var(--fg-3)' } as const
}

export function WorkflowInspector({ node, onPatch, onRemove, resolve, delegates, notifyOptions, roleOptions, onManageRoles }: {
  node: WfNode | null
  onPatch: (id: string, patch: NodePatch) => void
  onRemove: (id: string) => void
  /**
   * The role resolved to whoever actually holds it, in BOTH modes. This component never
   * imports `lib/members.ts`, and never learns which mode it is in.
   */
  resolve: (position: RoleKey) => Resolved
  /** Active members holding an approver role — Admin or Reviewer — in both modes. */
  delegates: string[]
  /**
   * ALWAYS passed, because the notify fork has to happen somewhere and it cannot happen here.
   * Firm mode is handed the untouched `TARGET_OPTIONS` object itself, so its five values are
   * identical by identity rather than merely by value.
   */
  notifyOptions: WfOption[]
  /** The mode's roles, already carrying the selected step's key even when it names none. */
  roleOptions: WfOption[]
  onManageRoles: () => void
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
  const res = node.type === 'approval' ? resolve(node.role) : null

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

      {/* The card's CONTENT column, not its root: the root has no padding of its own, so a
          topology sweep anchored there would read this div's 15px as slack. */}
      <div data-testid="step-inspector-body" style={{ padding: 15 }}>
        {node.type === 'approval' && res && (
          <>
            <WfSelect label="Who must approve" value={node.role} options={roleOptions} onChange={(v) => patch({ role: v as RoleKey })} />
            <div style={{ ...hintStyle(res.warn), display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 10, marginBottom: 14 }}>
              <span>{res.text}</span>
              <button
                type="button"
                onClick={onManageRoles}
                className="pf-btn"
                style={{ flex: 'none', padding: 0, border: 0, background: 'transparent', color: 'var(--action)', fontSize: 11.5, fontWeight: 600, fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
              >
                Manage roles
              </button>
            </div>
            <WfSelect label="Deadline" value={node.sla} options={slaOptions(node.sla)} onChange={(v) => patch({ sla: v as Sla })} marginBottom={14} />
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, padding: '4px 0' }}>
              <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Allow delegation</span>
              <WfToggle
                on={node.delegate}
                onToggle={() => patch({ delegate: !node.delegate })}
                label="Allow delegation"
                disabled
                title={DELEGATION_BLOCKED}
                ariaDescribedBy={DELEGATION_BLOCKED_ID}
              />
            </div>
            {/* Above the picker block, never inside it: `delegateNote()` reads that block's LAST
                child positionally, and DELEGATE_NOTE has to stay the node it finds. */}
            <div id={DELEGATION_BLOCKED_ID} data-testid="delegation-blocked-reason" style={hintStyle()}>
              {DELEGATION_BLOCKED}
            </div>
            {/* No `{node.delegate && …}` guard: the toggle is shut, so behind one the picker would
                be REMOVED rather than labelled. The wrapping div stays — see the comment above. */}
            <div style={{ marginTop: 12 }}>
              <WfSelect
                label="Delegate to"
                value={node.delegateTo ?? ANY_APPROVER}
                options={[{ value: ANY_APPROVER, label: 'Anyone with the Admin or Reviewer role' }, ...toOptions(delegates)]}
                onChange={(v) => patch({ delegateTo: v })}
                disabled
                title={DELEGATION_BLOCKED}
                ariaDescribedBy={DELEGATION_BLOCKED_ID}
              />
              <div style={hintStyle()}>{DELEGATE_NOTE}</div>
            </div>
          </>
        )}

        {node.type === 'condition' && (
          <>
            <WfSelect
              label="If this field"
              value={node.field}
              options={FIELD_OPTIONS}
              // One option, so this never fires — `WfSelect` requires the prop.
              onChange={(v) => patch({ field: v as CondField })}
              marginBottom={14}
            />

            {node.field === 'amount' && (
              <>
                <WfSelect label="Is" value={node.op} options={OP_OPTIONS} onChange={(v) => patch({ op: v as CondOp })} marginBottom={12} />
                <WfAmountInput value={node.value} onChange={(v) => patch({ value: v })} ariaLabel="Threshold amount in naira" marginBottom={10} />
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
            <WfSelect label="Notify" value={node.target} options={notifyOptions} onChange={(v) => patch({ target: v })} marginBottom={14} />
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
