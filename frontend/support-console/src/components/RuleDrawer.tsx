import { KILL_ICON } from '../data'
import { Drawer } from './Drawer'
import type { Rule } from '../types'

type Props = {
  rule: Rule
  inForce: boolean
  busy: boolean
  onKill: () => void
  onClose: () => void
}

const ruleJSON = (rule: Rule): string =>
  JSON.stringify(
    { key: rule.key, type: rule.type, field: rule.field, severity: rule.severity, scope: rule.scope, enabled: rule.enabled, params: rule.params, when: rule.when, message: rule.message },
    null,
    2,
  )

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

export function RuleDrawer({ rule, inForce, busy, onKill, onClose }: Props) {
  const params = Object.entries(rule.params)

  return (
    <Drawer
      width={580}
      onClose={onClose}
      header={
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4 }}>
            <span className="mono" style={{ fontSize: 14, fontWeight: 700 }}>
              {rule.key}
            </span>
            <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-2)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-sm)', padding: '2px 6px' }}>
              {rule.type}
            </span>
          </div>
          <div style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{rule.field}</div>
        </>
      }
      footer={
        <>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 9 }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Live status</span>
            <span className="mono" style={{ fontSize: 10.5, fontWeight: 700, color: rule.enabled ? 'var(--status-green-text)' : 'var(--status-red-text)' }}>
              {rule.enabled ? 'ENABLED' : 'DISABLED'}
            </span>
          </div>
          {rule.enabled && inForce && (
            <button
              type="button"
              onClick={onKill}
              disabled={busy}
              className="ops-btn"
              style={{ border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', ...(busy ? { opacity: 0.45, cursor: 'not-allowed' } : { cursor: 'pointer' }), height: 38, padding: '0 14px', borderRadius: 'var(--radius-btn)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)', display: 'inline-flex', alignItems: 'center', gap: 7 }}
            >
              {KILL_ICON} Kill-switch
            </button>
          )}
        </>
      }
    >
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
    </Drawer>
  )
}
