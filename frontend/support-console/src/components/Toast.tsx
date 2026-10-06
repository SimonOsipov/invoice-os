import { ALERT_ICON, CHECK_ICON } from '../data'
import type { ToastState } from '../types'

type Props = {
  toast: NonNullable<ToastState>
}

export function Toast({ toast }: Props) {
  const isRed = toast.tone === 'red'
  return (
    <div
      className="asc-dark"
      style={{
        position: 'fixed',
        bottom: 24,
        left: '50%',
        transform: 'translateX(-50%)',
        zIndex: 95,
        display: 'flex',
        alignItems: 'center',
        gap: 11,
        background: 'var(--surface)',
        color: 'var(--text-on-dark)',
        borderRadius: 'var(--radius-md)',
        padding: '12px 18px',
        boxShadow: 'var(--shadow-card)',
        animation: 'opsToast 200ms ease-out',
      }}
      role="status"
    >
      <span style={{ flex: 'none', color: isRed ? 'var(--status-red-text)' : 'var(--teal-300)', display: 'inline-flex' }}>
        {isRed ? ALERT_ICON : CHECK_ICON}
      </span>
      <span style={{ fontSize: 13.5, fontWeight: 500 }}>{toast.msg}</span>
      {toast.tag && (
        <span className="mono" style={{ fontSize: 10, color: 'var(--surface-body)', letterSpacing: '0.05em', borderLeft: '1px solid var(--surface-panel-border)', paddingLeft: 11, marginLeft: 4 }}>
          {toast.tag}
        </span>
      )}
    </div>
  )
}
