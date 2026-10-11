import { useState, type ReactNode } from 'react'
import { KILL_ICON } from '../data'
import { draftRuleErrors, EMPTY_FORM, formFromRule, RULE_TYPES, SEVERITIES, type RuleForm } from '../rulesApi'
import { Drawer } from './Drawer'
import { Fault } from './Fault'
import type { Rule } from '../types'

export type DraftEdit = {
  /** Keys already in the draft, checked for a new rule only. */
  draftKeys: string[]
  busy: boolean
  onSave: (form: RuleForm) => void
  onRemove: () => void
}

type Props = {
  /** null opens an empty form for a new draft rule. */
  rule: Rule | null
  inForce: boolean
  busy: boolean
  draft?: DraftEdit
  onKill: () => void
  onClose: () => void
}

// The rule as the API holds it: wire keys and severity.
const ruleJSON = (rule: Rule): string => {
  const { target, severity } = formFromRule(rule)
  return JSON.stringify({ key: rule.key, type: rule.type, target, severity, scope: rule.scope, enabled: rule.enabled, params: rule.params, when: rule.when, message: rule.message }, null, 2)
}

const readOnlyRow = (label: string, value: string, mono: boolean) => (
  <div key={label}>
    <div className="label" style={{ marginBottom: 5, textTransform: 'none', letterSpacing: 0 }}>
      {label}
    </div>
    <div className="ops-input" style={{ display: 'flex', alignItems: 'center', height: 'auto', minHeight: 36, padding: '8px 11px', fontSize: 12.5, color: 'var(--fg-1)', overflowWrap: 'anywhere' }}>
      <span className={mono ? 'mono' : undefined}>{value}</span>
    </div>
  </div>
)

const BARE = { border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', flex: 1, minWidth: 0, padding: 0 } as const

function Field({ label, children, tall }: { label: string; children: ReactNode; tall?: boolean }) {
  return (
    <div>
      <div className="label" style={{ marginBottom: 6 }}>
        {label}
      </div>
      <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center', ...(tall && { height: 'auto', padding: '8px 11px' }) }}>
        {children}
      </div>
    </div>
  )
}

const RED_BTN = { border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', height: 38, padding: '0 14px', borderRadius: 'var(--radius-btn)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)', display: 'inline-flex', alignItems: 'center', gap: 7 } as const
const LOCKED = { opacity: 0.45, filter: 'none', cursor: 'not-allowed' } as const

export function RuleDrawer({ rule, inForce, busy, draft, onKill, onClose }: Props) {
  const isNew = rule === null
  const [form, setForm] = useState<RuleForm>(rule ? formFromRule(rule) : EMPTY_FORM)
  const [touched, setTouched] = useState(false)
  const set = <K extends keyof RuleForm>(k: K, v: RuleForm[K]) => {
    setTouched(true)
    setForm((f) => ({ ...f, [k]: v }))
  }
  const errors = draft ? draftRuleErrors(form, isNew ? draft.draftKeys : []) : []
  const saveBlocked = !draft || draft.busy || errors.length > 0
  const params = rule ? Object.entries(rule.params) : []
  const types = RULE_TYPES.includes(form.type) ? RULE_TYPES : [form.type, ...RULE_TYPES]

  return (
    <Drawer
      width={580}
      onClose={onClose}
      header={
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4 }}>
            <span className="mono" style={{ fontSize: 14, fontWeight: 700, overflowWrap: 'anywhere' }}>
              {rule ? rule.key : 'New rule'}
            </span>
            {rule && (
              <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-2)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-sm)', padding: '2px 6px' }}>
                {rule.type}
              </span>
            )}
          </div>
          <div style={{ fontSize: 12.5, color: 'var(--fg-2)', overflowWrap: 'anywhere' }}>{rule ? rule.field : 'Added to the draft when you save.'}</div>
        </>
      }
      footer={
        draft ? (
          <>
            <div style={{ flex: 1 }} />
            {rule && (
              <button type="button" onClick={draft.onRemove} disabled={draft.busy} className="ops-btn" style={{ ...RED_BTN, ...(draft.busy ? LOCKED : { cursor: 'pointer' }) }}>
                {KILL_ICON} Remove from draft
              </button>
            )}
            <button
              type="button"
              onClick={() => draft.onSave(form)}
              disabled={saveBlocked}
              className="ops-btn v2-btn v2-btn-primary"
              style={{ height: 38, ...(saveBlocked ? LOCKED : {}) }}
            >
              Save to draft
            </button>
          </>
        ) : (
          rule && (
            <>
              <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 9 }}>
                <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Live status</span>
                <span className="mono" style={{ fontSize: 10.5, fontWeight: 700, color: rule.enabled ? 'var(--status-green-text)' : 'var(--status-red-text)' }}>
                  {rule.enabled ? 'ENABLED' : 'DISABLED'}
                </span>
              </div>
              {rule.enabled && inForce && (
                <button type="button" onClick={onKill} disabled={busy} className="ops-btn" style={{ ...RED_BTN, ...(busy ? { opacity: 0.45, cursor: 'not-allowed' } : { cursor: 'pointer' }) }}>
                  {KILL_ICON} Kill-switch
                </button>
              )}
            </>
          )
        )
      }
    >
      {draft ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          {isNew && (
            <Field label="Key">
              <input style={BARE} aria-label="Key" value={form.key} onChange={(e) => set('key', e.target.value)} />
            </Field>
          )}
          <Field label="Type">
            <select style={BARE} aria-label="Type" value={form.type} onChange={(e) => set('type', e.target.value)}>
              {types.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Severity">
            <select style={BARE} aria-label="Severity" value={form.severity} onChange={(e) => set('severity', e.target.value as RuleForm['severity'])}>
              {SEVERITIES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Target">
            <input style={BARE} aria-label="Target" value={form.target} onChange={(e) => set('target', e.target.value)} />
          </Field>
          <Field label="Params" tall>
            <textarea className="mono" style={{ ...BARE, fontSize: 12, resize: 'vertical' }} rows={6} aria-label="Params" value={form.params} onChange={(e) => set('params', e.target.value)} />
          </Field>
          <Field label="When">
            <input className="mono" style={BARE} aria-label="When" value={form.when} onChange={(e) => set('when', e.target.value)} />
          </Field>
          <Field label="Message">
            <input style={BARE} aria-label="Message" value={form.message} onChange={(e) => set('message', e.target.value)} />
          </Field>
          <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <input type="checkbox" aria-label="Enabled" checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} />
            <span className="label">Enabled</span>
          </label>
          {touched && errors.map((e) => <Fault key={e} text={e} />)}
        </div>
      ) : (
        rule && (
          <>
            <div className="label" style={{ marginBottom: 12 }}>
              Parameters
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 24 }}>
              {params.map(([k, v]) => readOnlyRow(k, JSON.stringify(v), true))}
              {rule.when && readOnlyRow('When', rule.when, true)}
              {readOnlyRow('Failure message', rule.message, false)}
            </div>

            <div className="label" style={{ marginBottom: 10 }}>
              Underlying rule JSON
            </div>
            <pre className="ops-json">{ruleJSON(rule)}</pre>
          </>
        )
      )}
    </Drawer>
  )
}
