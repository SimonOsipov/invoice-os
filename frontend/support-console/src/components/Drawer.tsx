import type { ReactNode } from 'react'
import { CLOSE_ICON } from '../icons'

// The right-hand drawer shell: scrim, panel, header and footer. Only the width, the
// header content and the footer buttons vary.

type Props = {
  /** Rendered in the header, left of the close button. */
  header: ReactNode
  /** Sticky action row at the bottom. Omit for a read-only drawer. */
  footer?: ReactNode
  /** Full-width strip directly under the header. */
  banner?: ReactNode
  width?: number
  onClose: () => void
  children: ReactNode
}

export function Drawer({ header, footer, banner, width = 560, onClose, children }: Props) {
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', animation: 'opsFade 160ms ease-out' }} />
      <div
        className="ops-drawer"
        role="dialog"
        aria-modal="true"
        style={{
          position: 'fixed',
          top: 0,
          right: 0,
          bottom: 0,
          zIndex: 81,
          width,
          maxWidth: '94vw',
          background: 'var(--bg-1)',
          borderLeft: '1px solid var(--line-2)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'opsDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: 1, minWidth: 0 }}>{header}</div>
          <button
            type="button"
            onClick={onClose}
            className="ops-btn"
            aria-label="Close"
            style={{ border: 0, background: 'var(--bg-3)', cursor: 'pointer', width: 30, height: 30, borderRadius: 'var(--radius-btn)', color: 'var(--fg-2)', display: 'grid', placeItems: 'center', flex: 'none' }}
          >
            {CLOSE_ICON}
          </button>
        </div>

        {banner}

        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>{children}</div>

        {footer && (
          <div style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'center', gap: 10 }}>{footer}</div>
        )}
      </div>
    </>
  )
}
