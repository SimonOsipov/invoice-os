// Rotate-key confirmation modal (prototype lines 705-726). Mock only — confirming
// rotates nothing; it closes and toasts.
//
// Structurally this is NOT the drawer shell. JobDrawer/EvidenceDrawer render the scrim
// and the panel as siblings in a fragment; here the scrim is the PARENT and the panel is
// its child (proto:707-708), which is why the panel needs its own stopPropagation — a
// click that reaches the scrim closes the modal, and without it every click on the panel
// would too. The other deltas from the drawers are deliberate: z-index 90 (drawers are
// 80/81, Toast is 95, so this layers between them), scrim alpha 0.42 (drawers 0.32) and a
// 140ms fade (drawers 160ms).
//
// Dismiss paths are exactly two — the scrim and Cancel. There is deliberately no Escape
// handler, no close X, no role="dialog"/aria-modal and no focus trap: the prototype has
// none, and neither do the two drawers that sit on this same shell. Overlay a11y is a
// repo-wide follow-up, not a change to make on one of three overlays.

import { ALERT_ICON, REDRIVE_ICON } from '../data'

type Props = {
  // The env label ('LIVE' | 'SANDBOX') the rotate button carried, not the key id
  // (proto:998) — it is the string interpolated into the heading and the toast.
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
        background: 'rgba(20,23,26,0.42)',
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
          borderRadius: 10,
          overflow: 'hidden',
          boxShadow: '0 24px 60px -20px rgba(20,23,26,0.4)',
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
                borderRadius: 8,
                background: 'var(--status-amber-bg)',
                color: 'var(--status-amber-text)',
                display: 'grid',
                placeItems: 'center',
              }}
            >
              {REDRIVE_ICON}
            </span>
            <h3 style={{ fontSize: 17, fontWeight: 600, letterSpacing: '-0.02em', margin: 0 }}>Rotate {env} key?</h3>
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
              borderRadius: 6,
              padding: '10px 12px',
              display: 'flex',
              gap: 9,
            }}
          >
            <span style={{ color: 'var(--status-amber-text)', flex: 'none' }}>{ALERT_ICON}</span>
            <span style={{ fontSize: 12, color: 'var(--status-amber-text)', lineHeight: 1.5 }}>
              This action is recorded against your account and cannot be undone.
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
