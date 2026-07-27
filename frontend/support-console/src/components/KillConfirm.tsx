import { ALERT_ICON, KILL_ICON } from '../data'
import { Modal } from './Modal'
import type { Env } from '../types'

type Props = {
  ruleKey: string
  env: Env
  onClose: () => void
  onConfirm: () => void
}

// proto:639. Disabling a live validation rule is the most consequential thing this console
// can do — it stops every tenant's invoices being checked against it — so it is the only
// action gated behind a typed confirmation step.
export function KillConfirm({ ruleKey, env, onClose, onConfirm }: Props) {
  return (
    <Modal onClose={onClose}>
      <div style={{ padding: '22px 24px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 14 }}>
          <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', color: 'var(--status-red-text)', display: 'grid', placeItems: 'center' }}>
            {KILL_ICON}
          </span>
          <h3 style={{ fontSize: 17, fontWeight: 500, letterSpacing: '-0.02em', margin: 0 }}>Disable a live rule?</h3>
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
          in the <span style={{ fontWeight: 600 }}>{env === 'sandbox' ? 'SANDBOX' : 'LIVE'}</span> environment. Invoices will no longer be validated against it until re-enabled.
        </p>
        <div style={{ background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', borderRadius: 'var(--radius-input)', padding: '10px 12px', display: 'flex', gap: 9 }}>
          <span style={{ color: 'var(--status-amber-text)', flex: 'none' }}>{ALERT_ICON}</span>
          <span style={{ fontSize: 12, color: 'var(--status-amber-text)', lineHeight: 1.5 }}>
            This action is recorded in the immutable audit log against your operator identity.
          </span>
        </div>
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
        <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
          Cancel
        </button>
        <button
          type="button"
          onClick={onConfirm}
          className="ops-btn"
          style={{ border: 0, cursor: 'pointer', height: 38, padding: '0 18px', borderRadius: 'var(--radius-sm)', background: 'var(--status-red-text)', color: 'var(--text-on-dark)', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: 600 }}
        >
          Disable rule
        </button>
      </div>
    </Modal>
  )
}
