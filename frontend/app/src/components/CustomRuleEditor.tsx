// Custom-rule editor in the RuleDrawer frame. The guided form emits typed rules only; CEL is the
// expression tab's job (D6, D7).

import { useState, type CSSProperties, type ReactNode } from 'react'

import { chevDownGlyph, closeGlyph } from '../glyphs'
import {
  checksFor,
  createCustomRule,
  expressionRuleInput,
  formError,
  GUIDED_FIELDS,
  guidedRuleInput,
  guidedShapeOf,
  payloadPath,
  updateCustomRule,
  type CustomRule,
  type ExpressionState,
  type GuidedCheck,
  type GuidedSeverity,
  type GuidedState,
} from '../lib/rulesApi'
import type { PlatformCtx } from '../types'
import { radioPaint } from './MemberParts'

const CHECK_LABEL: Record<GuidedCheck, string> = {
  required: 'is filled in',
  regex: 'matches a pattern',
  enum: 'is one of',
  range: 'is between',
}

type Mode = 'guided' | 'expression'

const LABEL: CSSProperties = { fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }
const ROW: CSSProperties = { marginBottom: 14 }
const AREA: CSSProperties = { height: 'auto', padding: '8px 12px', resize: 'vertical', lineHeight: 1.45 }

const defaultMessage = (fieldKey: string) => `${GUIDED_FIELDS.find((f) => f.key === fieldKey)?.label ?? fieldKey} failed the check`

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div style={ROW}>
      <div style={LABEL}>{label}</div>
      {children}
    </div>
  )
}

function SelectBox({ label, value, onChange, disabled, children }: { label: string; value: string; onChange: (v: string) => void; disabled: boolean; children: ReactNode }) {
  return (
    <span style={{ position: 'relative', display: 'block' }}>
      <select aria-label={label} className="pf-input" value={value} onChange={(e) => onChange(e.target.value)} disabled={disabled}>
        {children}
      </select>
      <span aria-hidden="true" style={{ position: 'absolute', right: 11, top: '50%', transform: 'translateY(-50%)', display: 'inline-flex', color: 'var(--fg-3)', pointerEvents: 'none' }}>
        {chevDownGlyph}
      </span>
    </span>
  )
}

function Radio({ name, label, checked, disabled, onChange, testId }: { name: string; label: string; checked: boolean; disabled: boolean; onChange: () => void; testId?: string }) {
  return (
    <label
      style={{ flex: 1, minWidth: 0, display: 'flex', alignItems: 'center', gap: 9, padding: '10px 12px', borderRadius: 'var(--radius-md)', border: `1px solid ${checked ? 'var(--action)' : 'var(--line-2)'}`, background: checked ? 'var(--action-tint)' : 'var(--bg-2)', fontSize: 13, fontWeight: 500, color: 'var(--fg-1)', cursor: disabled ? 'default' : 'pointer', overflowWrap: 'anywhere' }}
    >
      <input type="radio" name={name} checked={checked} disabled={disabled} onChange={onChange} data-testid={testId} style={{ ...radioPaint(checked), margin: 0 }} />
      {label}
    </label>
  )
}

/** The mode an existing rule opens in; null means read-only. */
function openMode(rule: CustomRule): Mode | null {
  if (guidedShapeOf(rule)) return 'guided'
  const keys = Object.keys(rule.params)
  return rule.type === 'cel' && rule.when === null && keys.length === 1 && typeof rule.params.expr === 'string' ? 'expression' : null
}

type Props = {
  ctx: PlatformCtx
  base: string
  /** The rule to edit; absent for a new one. */
  rule?: CustomRule
  onClose: () => void
  /** The server's rule after a successful save. */
  onSaved: (saved: CustomRule) => void
}

export function CustomRuleEditor({ ctx, base, rule, onClose, onSaved }: Props) {
  const firm = ctx.mode === 'firm'
  const client = firm ? ctx.activeEntity : null
  const title = rule ? 'Edit custom rule' : 'New custom rule'
  const shape = rule ? guidedShapeOf(rule) : null
  const initial: Mode | null = rule ? openMode(rule) : 'guided'

  const [mode, setMode] = useState<Mode>(initial ?? 'guided')
  const [field, setField] = useState(shape?.field ?? GUIDED_FIELDS[0].key)
  const [check, setCheck] = useState<GuidedCheck>(shape?.check ?? 'required')
  const [pattern, setPattern] = useState(shape?.pattern ?? '')
  const [values, setValues] = useState(shape?.values ?? '')
  const [min, setMin] = useState(shape?.min ?? '')
  const [max, setMax] = useState(shape?.max ?? '')
  const [target, setTarget] = useState(rule && initial === 'expression' ? rule.target : '')
  const [expr, setExpr] = useState(rule && initial === 'expression' ? String(rule.params.expr) : '')
  const [message, setMessage] = useState(rule?.message ?? defaultMessage(GUIDED_FIELDS[0].key))
  const [touched, setTouched] = useState(rule != null)
  const [severity, setSeverity] = useState<GuidedSeverity>(rule && (rule.severity === 'warning' || rule.severity === 'error') ? rule.severity : 'warning')
  // New rules start on the active client: the narrower scope. Firm-wide stays one click away.
  const [scopeClient, setScopeClient] = useState(rule ? rule.entity_id !== null : true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const entityId = client && scopeClient ? client.id : null
  const readOnly = rule != null && initial === null
  const targetKnown = target === '' || GUIDED_FIELDS.some((f) => payloadPath(f.key) === target)

  function pickField(key: string) {
    setField(key)
    const type = GUIDED_FIELDS.find((f) => f.key === key)!.type
    if (!checksFor(type).includes(check)) setCheck(checksFor(type)[0])
    if (!touched) setMessage(defaultMessage(key))
  }

  function pickMode(next: Mode) {
    setMode(next)
    setError(null)
    if (!touched) setMessage(next === 'guided' ? defaultMessage(field) : '')
  }

  async function save() {
    if (saving) return
    const guided: GuidedState = { field, check, pattern, values, min, max, message, severity, entityId }
    const expression: ExpressionState = { target, expr, message, severity, entityId }
    const problem = formError(mode === 'guided' ? guided : expression)
    if (problem) {
      setError(problem)
      return
    }
    setSaving(true)
    setError(null)
    const body = { ...(mode === 'guided' ? guidedRuleInput(guided) : expressionRuleInput(expression)), enabled: rule?.enabled ?? true }
    try {
      const saved = rule ? await updateCustomRule(ctx.authedFetch, base, rule.id, body) : await createCustomRule(ctx.authedFetch, base, body)
      onSaved(saved)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  const close = () => {
    if (!saving) onClose()
  }
  const checks = checksFor(GUIDED_FIELDS.find((f) => f.key === field)!.type)

  return (
    <>
      <div onClick={close} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', animation: 'pfFade 160ms ease-out' }} />
      <div
        className="pf-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        style={{ position: 'fixed', top: 0, right: 0, bottom: 0, zIndex: 81, width: 560, maxWidth: '94vw', background: 'var(--bg-1)', borderLeft: '1px solid var(--line-2)', display: 'flex', flexDirection: 'column', animation: 'pfDrawer 200ms ease-out' }}
      >
        <div style={{ flex: 'none', padding: '16px 24px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 14 }}>
          <div className="card-title">{title}</div>
          <button type="button" onClick={close} className="pf-btn" aria-label="Close" style={{ flex: 'none', width: 34, height: 34, borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
            {closeGlyph}
          </button>
        </div>

        <form
          data-hs-do-not-collect="true"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
          style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}
        >
          <div style={{ flex: 1, overflowY: 'auto', padding: '20px 24px 28px' }}>
            {readOnly ? (
              <div data-testid="rule-readonly" style={{ fontSize: 13, lineHeight: 1.5, color: 'var(--fg-2)', overflowWrap: 'anywhere' }}>
                This rule has a shape the editor does not write, so it cannot be edited here. You can still switch it off or remove it.
              </div>
            ) : (
              <>
                <div role="tablist" style={{ display: 'flex', gap: 8, marginBottom: 18 }}>
                  {(['guided', 'expression'] as const).map((m) => (
                    <button
                      key={m}
                      type="button"
                      role="tab"
                      aria-selected={mode === m}
                      disabled={saving}
                      data-testid={`rule-mode-${m}`}
                      onClick={() => pickMode(m)}
                      className={`v2-btn pf-btn ${mode === m ? 'v2-btn-primary' : 'v2-btn-ghost'}`}
                      style={{ height: 32, fontSize: 13 }}
                    >
                      {m === 'guided' ? 'Guided' : 'Expression'}
                    </button>
                  ))}
                </div>

                {mode === 'guided' ? (
                  <>
                    <Field label="Field">
                      <SelectBox label="Field" value={field} onChange={pickField} disabled={saving}>
                        {GUIDED_FIELDS.map((f) => (
                          <option key={f.key} value={f.key}>{f.label}</option>
                        ))}
                      </SelectBox>
                    </Field>
                    <Field label="Check">
                      <SelectBox label="Check" value={check} onChange={(v) => setCheck(v as GuidedCheck)} disabled={saving}>
                        {checks.map((c) => (
                          <option key={c} value={c}>{CHECK_LABEL[c]}</option>
                        ))}
                      </SelectBox>
                    </Field>
                    {check === 'regex' && (
                      <Field label="Pattern">
                        <input aria-label="Pattern" className="pf-input mono" value={pattern} onChange={(e) => setPattern(e.target.value)} disabled={saving} />
                      </Field>
                    )}
                    {check === 'enum' && (
                      <Field label="Allowed values (comma-separated)">
                        <input aria-label="Allowed values" className="pf-input" value={values} onChange={(e) => setValues(e.target.value)} disabled={saving} />
                      </Field>
                    )}
                    {check === 'range' && (
                      <div style={{ display: 'flex', gap: 12 }}>
                        <div style={{ ...ROW, flex: 1, minWidth: 0 }}>
                          <div style={LABEL}>Minimum</div>
                          <input aria-label="Minimum" className="pf-input mono" inputMode="decimal" value={min} onChange={(e) => setMin(e.target.value)} disabled={saving} />
                        </div>
                        <div style={{ ...ROW, flex: 1, minWidth: 0 }}>
                          <div style={LABEL}>Maximum</div>
                          <input aria-label="Maximum" className="pf-input mono" inputMode="decimal" value={max} onChange={(e) => setMax(e.target.value)} disabled={saving} />
                        </div>
                      </div>
                    )}
                  </>
                ) : (
                  <>
                    <Field label="Target field">
                      <SelectBox label="Target field" value={target} onChange={setTarget} disabled={saving}>
                        <option value="">Whole invoice</option>
                        {!targetKnown && <option value={target}>{target}</option>}
                        {GUIDED_FIELDS.map((f) => (
                          <option key={f.key} value={payloadPath(f.key)}>{f.label}</option>
                        ))}
                      </SelectBox>
                    </Field>
                    <Field label="Expression (true when the invoice passes)">
                      <textarea aria-label="Expression" className="pf-input mono" rows={4} value={expr} onChange={(e) => setExpr(e.target.value)} disabled={saving} style={{ ...AREA, fontSize: 13 }} />
                      <div data-testid="rule-expression-help" style={{ marginTop: 6, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)', overflowWrap: 'anywhere' }}>
                        Guard each field you read, segment by segment: <code className="mono">has(invoice.buyer) && has(invoice.buyer.email)</code>.
                      </div>
                    </Field>
                  </>
                )}

                <Field label="Message">
                  <textarea
                    aria-label="Message"
                    className="pf-input"
                    rows={2}
                    value={message}
                    onChange={(e) => {
                      setMessage(e.target.value)
                      setTouched(true)
                    }}
                    disabled={saving}
                    style={AREA}
                  />
                </Field>

                <Field label="Severity">
                  <div role="radiogroup" aria-label="Severity" style={{ display: 'flex', gap: 8 }}>
                    <Radio name="rule-severity" label="Warn" checked={severity === 'warning'} disabled={saving} onChange={() => setSeverity('warning')} />
                    <Radio name="rule-severity" label="Block submission" checked={severity === 'error'} disabled={saving} onChange={() => setSeverity('error')} />
                  </div>
                </Field>

                {firm && (
                  <Field label="Applies to">
                    <div role="radiogroup" aria-label="Scope" style={{ display: 'flex', gap: 8 }}>
                      <Radio name="rule-scope" label="All clients (firm-wide)" checked={entityId === null} disabled={saving} onChange={() => setScopeClient(false)} testId="rule-scope-firm" />
                      {client && <Radio name="rule-scope" label={`${client.name} only`} checked={entityId !== null} disabled={saving} onChange={() => setScopeClient(true)} testId="rule-scope-client" />}
                    </div>
                  </Field>
                )}
              </>
            )}
          </div>

          {error && (
            <div data-testid="rule-save-error" role="alert" style={{ flex: 'none', padding: '10px 24px', borderTop: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)', overflowWrap: 'anywhere' }}>
              {error}
            </div>
          )}

          <div style={{ flex: 'none', display: 'flex', justifyContent: 'flex-end', gap: 9, padding: '14px 24px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
            <button type="button" onClick={close} disabled={saving} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, fontSize: 13 }}>
              {readOnly ? 'Close' : 'Cancel'}
            </button>
            {!readOnly && (
              <button type="submit" disabled={saving} data-testid="rule-save" className="v2-btn v2-btn-primary pf-btn" style={{ height: 36, fontSize: 13 }}>
                {saving ? 'Saving…' : 'Save'}
              </button>
            )}
          </div>
        </form>
      </div>
    </>
  )
}
