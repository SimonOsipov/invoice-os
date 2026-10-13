import { useState } from 'react'
import { CHECK_ICON, KILL_ICON } from '../data'
import { reasonValid } from '../rulesApi'
import { Modal } from './Modal'

type Props = {
  ruleKey: string
  action: 'disable' | 'enable'
  busy: boolean
  onClose: () => void
  onConfirm: (reason: string) => void
}

// Disabling a live rule stops validating every tenant's invoices against it, so both
// directions take a reason; the server records it under the staff member's identity.
export function KillConfirm({ ruleKey, action, busy, onClose, onConfirm }: Props) {
  const [reason, setReason] = useState('')
  const disable = action === 'disable'
  const blocked = busy || !reasonValid(reason)
  const tile = disable ? { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' } : { background: 'var(--status-green-bg)', color: 'var(--status-green-text)' }
  return (
    <Modal onClose={onClose}>
      <div style={{ padding: '22px 24px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 14 }}>
          <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 'var(--radius-md)', ...tile, display: 'grid', placeItems: 'center' }}>
            {disable ? KILL_ICON : CHECK_ICON}
          </span>
          <h3 style={{ fontSize: 17, margin: 0 }}>{disable ? 'Disable a live rule?' : 'Enable this rule?'}</h3>
        </div>
        <p style={{ fontSize: 13.5, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 14px' }}>
          You are about to flip{' '}
          <span className="mono" style={{ fontWeight: 600, color: 'var(--fg-1)' }}>
            {ruleKey}
          </span>{' '}
          to{' '}
          <span className="mono" style={{ fontWeight: 600 }}>
            enabled = {disable ? 'false' : 'true'}
          </span>
          . The change applies to <span style={{ fontWeight: 600 }}>every tenant</span> and is recorded under your staff identity.
          {disable && ' Invoices will no longer be validated against it until re-enabled.'}
        </p>
        <div className="label" style={{ marginBottom: 6 }}>
          Reason
        </div>
        <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center' }}>
          <input
            style={{ border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, padding: 0 }}
            placeholder="Why is this changing?"
            aria-label="Reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
        <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
          Cancel
        </button>
        <button
          type="button"
          disabled={blocked}
          onClick={() => onConfirm(reason.trim())}
          className={disable ? 'ops-btn' : 'ops-btn v2-btn v2-btn-primary'}
          style={{
            height: 38,
            ...(disable && { border: 0, padding: '0 18px', borderRadius: 'var(--radius-btn)', background: 'var(--status-red-text)', color: 'var(--primary-foreground)', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: 600 }),
            ...(blocked ? { opacity: 0.45, filter: 'none', cursor: 'not-allowed' } : disable ? { cursor: 'pointer' } : {}),
          }}
        >
          {disable ? (busy ? 'Disabling…' : 'Disable rule') : busy ? 'Enabling…' : 'Enable rule'}
        </button>
      </div>
    </Modal>
  )
}
