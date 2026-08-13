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
  ruleText,
  slaOptions,
  toOptions,
  WfAmountInput,
  WfSelect,
  WfToggle,
  type WfOption,
} from './WorkflowParts'
import type { Resolved } from '../lib/roles'
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

// `delegateTo` has no "unset" value a <select> can emit — `WfSelect` is `value: string` — so
// the default is a SENTINEL option valued `''`, the idiom the invite modal's `NO_WF_ROLE`
// uses for the same reason. `''` and absent both mean "anyone", so nothing maps it back out:
// toggling delegation off and on leaves the key present as `''`, still the default.
const ANY_REVIEWER = ''

// Deliberately NOT the same wording as the option above it: the option names the fallback,
// the note states the eligibility rule. §11.3 writes them differently — do not harmonise.
const DELEGATE_NOTE = 'Only members with the Reviewer access role can be a delegate.'

// The delegation window: `delegate`/`delegateTo` have no server column (lib/policies.ts:73-75),
// so the choice is lost on every save. Stated rather than hidden — APPR-10 owns the storage and
// the disabling; until then the control stays interactive and says so.
const DELEGATION_NOT_STORED = 'Delegation is not stored yet — this choice is not saved.'

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
  /** Active members holding the Reviewer access role, in both modes. */
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

      <div style={{ padding: 15 }}>
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
              <WfToggle on={node.delegate} onToggle={() => patch({ delegate: !node.delegate })} label="Allow delegation" />
            </div>
            {/* Outside the guard below, so the warning shows in BOTH toggle states — DELEGATE_NOTE
                already occupies the in-guard slot and speaks only to the picker. */}
            <div style={hintStyle()}>{DELEGATION_NOT_STORED}</div>
            {node.delegate && (
              <div style={{ marginTop: 12 }}>
                <WfSelect
                  label="Delegate to"
                  value={node.delegateTo ?? ANY_REVIEWER}
                  options={[{ value: ANY_REVIEWER, label: 'Anyone with the Reviewer role' }, ...toOptions(delegates)]}
                  onChange={(v) => patch({ delegateTo: v })}
                />
                <div style={hintStyle()}>{DELEGATE_NOTE}</div>
              </div>
            )}
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
