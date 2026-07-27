import { CHECK_ICON, KILL_ICON, RULE_PARAMS } from '../data'
import { Drawer } from './Drawer'
import type { Rule } from '../types'

type Props = {
  rule: Rule
  testRan: boolean
  onRunTest: () => void
  onKill: () => void
  onClose: () => void
}

// proto:1181. The JSON view is generated from the rule itself so it can never drift from
// the row above it — params become an object keyed by a slugified label.
function ruleJSON(rule: Rule, params: { label: string; value: string }[]): string {
  const paramObj = params.reduce<Record<string, string>>((o, p) => {
    o[p.label.toLowerCase().replace(/[^a-z]/g, '_')] = p.value
    return o
  }, {})
  return `{
  "key": "${rule.key}",
  "type": "${rule.type}",
  "field": "${rule.field}",
  "severity": "${rule.severity}",
  "scope": "${rule.scope}",
  "enabled": ${rule.enabled},
  "params": ${JSON.stringify(paramObj)},
  "message": "${rule.message}"
}`
}

export function RuleDrawer({ rule, testRan, onRunTest, onKill, onClose }: Props) {
  const params = RULE_PARAMS[rule.type] ?? [{ label: 'Config', value: '—' }]

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
          {/* Kill-switch is only offered while the rule is live — the prototype showed it
              unconditionally, so a disabled rule presented a button that re-opened the
              "disable?" confirm for something already disabled. */}
          {rule.enabled && (
            <button
              type="button"
              onClick={onKill}
              className="ops-btn"
              style={{ border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', cursor: 'pointer', height: 38, padding: '0 14px', borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)', display: 'inline-flex', alignItems: 'center', gap: 7 }}
            >
              {KILL_ICON} Kill-switch
            </button>
          )}
          <button type="button" className="ops-btn v2-btn v2-btn-primary" style={{ height: 38 }}>
            Save to draft
          </button>
        </>
      }
    >
      <div className="label" style={{ marginBottom: 12 }}>
        Parameters
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 24 }}>
        {params.map((p) => (
          <div key={p.label}>
            <div className="label" style={{ marginBottom: 5, textTransform: 'none', letterSpacing: 0 }}>
              {p.label}
            </div>
            <div className="ops-input" style={{ display: 'flex', alignItems: 'center', height: 36 }}>
              <span className="mono" style={{ fontSize: 12.5, color: 'var(--fg-1)' }}>
                {p.value}
              </span>
            </div>
          </div>
        ))}
        <div>
          <div className="label" style={{ marginBottom: 5, textTransform: 'none', letterSpacing: 0 }}>
            Failure message
          </div>
          <div className="ops-input" style={{ display: 'flex', alignItems: 'center', height: 36, fontSize: 12.5, color: 'var(--fg-1)' }}>
            {rule.message}
          </div>
        </div>
      </div>

      <div className="label" style={{ marginBottom: 10 }}>
        Underlying rule JSON
      </div>
      <pre className="ops-json" style={{ marginBottom: 22 }}>
        {ruleJSON(rule, params)}
      </pre>

      <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
        <div style={{ padding: '12px 14px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
          <span className="label">Test against sample invoice</span>
          <button
            type="button"
            onClick={onRunTest}
            className="ops-btn"
            style={{ border: '1px solid var(--action)', background: 'var(--action)', cursor: 'pointer', height: 28, padding: '0 12px', borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, color: 'var(--text-on-dark)' }}
          >
            Run test
          </button>
        </div>
        <div style={{ padding: 14 }}>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginBottom: 10 }}>
            SAMPLE-INV-2026-09931 · ₦4,120,000 · VAT 7.5%
          </div>
          {testRan ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 9, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 'var(--radius-input)', padding: '10px 12px' }}>
              <span style={{ color: 'var(--status-green-text)' }}>{CHECK_ICON}</span>
              <span style={{ fontSize: 12.5, fontWeight: 600, color: 'var(--status-green-text)' }}>Rule passed · computed VAT ₦309,000 matches expected</span>
            </div>
          ) : (
            <div className="mono" style={{ fontSize: 11.5, color: 'var(--fg-4)' }}>
              No test run yet.
            </div>
          )}
        </div>
      </div>
    </Drawer>
  )
}
