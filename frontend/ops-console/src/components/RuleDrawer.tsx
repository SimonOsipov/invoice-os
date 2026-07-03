import { CHECK_ICON, CLOSE_ICON, KILL_ICON } from '../data'
import { buildRuleDrawer } from '../helpers'
import type { Rule } from '../types'

type Props = {
  rule: Rule
  testRan: boolean
  onRunTest: () => void
  onClose: () => void
  onKill: () => void
}

export function RuleDrawer({ rule, testRan, onRunTest, onClose, onKill }: Props) {
  const d = buildRuleDrawer(rule)
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'rgba(20,23,26,0.32)', animation: 'opsFade 160ms ease-out' }} />
      <div
        className="ops-drawer"
        style={{
          position: 'fixed',
          top: 0,
          right: 0,
          bottom: 0,
          zIndex: 81,
          width: 580,
          maxWidth: '94vw',
          background: 'var(--bg-1)',
          borderLeft: '1px solid var(--line-2)',
          boxShadow: '-24px 0 48px -24px rgba(20,23,26,0.3)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'opsDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: 1 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4 }}>
              <span className="mono" style={{ fontSize: 14, fontWeight: 700 }}>
                {d.key}
              </span>
              <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-2)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 4, padding: '2px 6px' }}>
                {d.type}
              </span>
            </div>
            <div style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{d.field}</div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="ops-btn"
            style={{ border: 0, background: 'var(--bg-3)', cursor: 'pointer', width: 30, height: 30, borderRadius: 6, color: 'var(--fg-2)', display: 'grid', placeItems: 'center' }}
          >
            {CLOSE_ICON}
          </button>
        </div>
        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          {/* param form */}
          <div className="label" style={{ marginBottom: 12 }}>
            Parameters
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 24 }}>
            {d.params.map((p) => (
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
                {d.message}
              </div>
            </div>
          </div>

          {/* underlying JSON */}
          <div className="label" style={{ marginBottom: 10 }}>
            Underlying rule JSON
          </div>
          <pre className="ops-json" style={{ marginBottom: 22 }}>
            {d.json}
          </pre>

          {/* test against sample */}
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 8, background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div style={{ padding: '12px 14px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span className="label">Test against sample invoice</span>
              <button
                type="button"
                onClick={onRunTest}
                className="ops-btn"
                style={{ border: '1px solid var(--accent)', background: 'var(--accent)', cursor: 'pointer', height: 28, padding: '0 12px', borderRadius: 5, fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, color: '#fff' }}
              >
                Run test
              </button>
            </div>
            <div style={{ padding: 14 }}>
              <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginBottom: 10 }}>
                SAMPLE-INV-2026-09931 · ₦4,120,000 · VAT 7.5%
              </div>
              {testRan ? (
                <div style={{ display: 'flex', alignItems: 'center', gap: 9, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 6, padding: '10px 12px' }}>
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
        </div>
        <div style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'center', gap: 10 }}>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 9 }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Live status</span>
            <span className="mono" style={{ fontSize: 10.5, fontWeight: 700, color: d.enabledColor }}>
              {d.enabledLabel}
            </span>
          </div>
          <button
            type="button"
            onClick={onKill}
            className="ops-btn"
            style={{ border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', cursor: 'pointer', height: 38, padding: '0 14px', borderRadius: 5, fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)', display: 'inline-flex', alignItems: 'center', gap: 7 }}
          >
            {KILL_ICON} Kill-switch
          </button>
          <button type="button" className="ops-btn v2-btn v2-btn-primary" style={{ height: 38 }}>
            Save to draft
          </button>
        </div>
      </div>
    </>
  )
}
