import { ALERT_ICON, CHECK_ICON } from '../data'
import type { ToastState } from '../types'

type Props = {
  toast: NonNullable<ToastState>
}

export function Toast({ toast }: Props) {
  const isRed = toast.tone === 'red'
  return (
    <div
      style={{
        position: 'fixed',
        bottom: 24,
        left: '50%',
        transform: 'translateX(-50%)',
        zIndex: 95,
        display: 'flex',
        alignItems: 'center',
        gap: 11,
        background: 'var(--slate-900)',
        color: '#fff',
        borderRadius: 8,
        padding: '12px 18px',
        boxShadow: '0 16px 40px -12px rgba(20,23,26,0.5)',
        animation: 'opsToast 200ms ease-out',
      }}
    >
      <span style={{ flex: 'none', color: isRed ? 'var(--status-red-text)' : 'var(--teal-300)', display: 'inline-flex' }}>{isRed ? ALERT_ICON : CHECK_ICON}</span>
      <span style={{ fontSize: 13.5, fontWeight: 500 }}>{toast.msg}</span>
      {toast.tag && (
        <span className="mono" style={{ fontSize: 10, color: 'var(--slate-400)', letterSpacing: '0.05em', borderLeft: '1px solid var(--slate-700)', paddingLeft: 11, marginLeft: 4 }}>
          {toast.tag}
        </span>
      )}
    </div>
  )
}
