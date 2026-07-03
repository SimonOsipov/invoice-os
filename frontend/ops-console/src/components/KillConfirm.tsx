import { ALERT_ICON, KILL_ICON } from '../data'
import type { Env } from '../types'

type Props = {
  ruleKey: string
  env: Env
  onCancel: () => void
  onConfirm: () => void
}

export function KillConfirm({ ruleKey, env, onCancel, onConfirm }: Props) {
  const envWord = env === 'sandbox' ? 'SANDBOX' : 'LIVE'
  return (
    <div
      onClick={onCancel}
      style={{ position: 'fixed', inset: 0, zIndex: 90, background: 'rgba(20,23,26,0.42)', display: 'grid', placeItems: 'center', animation: 'opsFade 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{ width: 440, maxWidth: '92vw', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 10, overflow: 'hidden', boxShadow: '0 24px 60px -20px rgba(20,23,26,0.4)', animation: 'opsPop 160ms ease-out' }}
      >
        <div style={{ padding: '22px 24px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 14 }}>
            <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 8, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', display: 'grid', placeItems: 'center' }}>{KILL_ICON}</span>
            <h3 style={{ fontSize: 17, fontWeight: 600, letterSpacing: '-0.02em', margin: 0 }}>Disable a live rule?</h3>
          </div>
          <p style={{ fontSize: 13.5, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 14px' }}>
            You are about to flip{' '}
            <span className="mono" style={{ fontWeight: 600, color: 'var(--fg-1)' }}>
              {ruleKey}
            </span>{' '}
            to{' '}
            <span className="mono" style={{ fontWeight: 600 }}>
              enabled = false
            </span>{' '}
            in the <span style={{ fontWeight: 600 }}>{envWord}</span> environment. Invoices will no longer be validated against it until re-enabled.
          </p>
          <div style={{ background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', borderRadius: 6, padding: '10px 12px', display: 'flex', gap: 9 }}>
            <span style={{ color: 'var(--status-amber-text)', flex: 'none' }}>{ALERT_ICON}</span>
            <span style={{ fontSize: 12, color: 'var(--status-amber-text)', lineHeight: 1.5 }}>This action is recorded in the immutable audit log against your operator identity.</span>
          </div>
        </div>
        <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
          <button type="button" onClick={onCancel} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
            Cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            className="ops-btn"
            style={{ border: 0, cursor: 'pointer', height: 38, padding: '0 18px', borderRadius: 4, background: 'var(--status-red-text)', color: '#fff', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: 600 }}
          >
            Disable rule
          </button>
        </div>
      </div>
    </div>
  )
}
