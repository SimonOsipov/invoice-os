// Rotate-key confirm (mock: closes and toasts). The scrim is the PARENT of the panel, so the
// panel stops propagation. Dismiss: scrim and Cancel only; no Escape, dialog role or focus trap.

import { ALERT_ICON, REDRIVE_ICON } from '../data'

type Props = {
  // The env label ('LIVE' | 'SANDBOX'), not the key id; interpolated into the heading and toast.
  env: string
  onClose: () => void
  onConfirm: () => void
}

export function RotateConfirm({ env, onClose, onConfirm }: Props) {
  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 90,
        background: 'color-mix(in srgb, var(--surface) 55%, transparent)',
        backdropFilter: 'blur(6px)',
        WebkitBackdropFilter: 'blur(6px)',
        display: 'grid',
        placeItems: 'center',
        animation: 'opsFade 140ms ease-out',
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          width: 440,
          maxWidth: '92vw',
          background: 'var(--bg-2)',
          border: '1px solid var(--line-2)',
          borderRadius: 'var(--radius-lg)',
          overflow: 'hidden',
          animation: 'opsPop 160ms ease-out',
        }}
      >
        <div style={{ padding: '22px 24px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 14 }}>
            <span
              style={{
                flex: 'none',
                width: 36,
                height: 36,
                borderRadius: 'var(--radius-md)',
                background: 'var(--status-amber-bg)',
                color: 'var(--status-amber-text)',
                display: 'grid',
                placeItems: 'center',
              }}
            >
              {REDRIVE_ICON}
            </span>
            <h3 style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.03em', margin: 0 }}>Rotate {env} key?</h3>
          </div>

          <p style={{ fontSize: 13.5, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 14px' }}>
            The current key is revoked immediately and a new secret is issued. Any service still using the old key will start receiving{' '}
            <span className="mono" style={{ fontWeight: 600 }}>
              401 Unauthorized
            </span>{' '}
            until you update it.
          </p>

          <div
            style={{
              background: 'var(--status-amber-bg)',
              border: '1px solid var(--status-amber-border)',
              borderRadius: 'var(--radius-md)',
              padding: '10px 12px',
              display: 'flex',
              gap: 9,
            }}
          >
            <span style={{ color: 'var(--status-amber-text)', flex: 'none' }}>{ALERT_ICON}</span>
            <span style={{ fontSize: 12, color: 'var(--status-amber-text)', lineHeight: 1.5 }}>
              Rotating cannot be undone. After NRS accreditation, the action is attributed to your operator identity in the audit log.
            </span>
          </div>
        </div>

        <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
          <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
            Cancel
          </button>
          <button type="button" onClick={onConfirm} className="ops-btn v2-btn v2-btn-primary" style={{ height: 38 }}>
            Rotate key
          </button>
        </div>
      </div>
    </div>
  )
}
