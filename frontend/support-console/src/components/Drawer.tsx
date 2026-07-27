import type { ReactNode } from 'react'
import { CLOSE_ICON } from '../data'

// The right-hand drawer shell. The prototype repeats this scrim + panel + header +
// footer markup verbatim for all three drawers (proto:456, 535, 574); the only things
// that vary are the width, the header content and the footer buttons.

type Props = {
  /** Rendered in the header, left of the close button. */
  header: ReactNode
  /** Sticky action row at the bottom. Omit for a read-only drawer. */
  footer?: ReactNode
  /** Full-width strip directly under the header — the audit drawer's immutability note. */
  banner?: ReactNode
  width?: number
  onClose: () => void
  children: ReactNode
}

export function Drawer({ header, footer, banner, width = 560, onClose, children }: Props) {
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.32)', animation: 'opsFade 160ms ease-out' }} />
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
          boxShadow: '-24px 0 48px -24px oklch(20% .02 210 / 0.3)',
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
            style={{ border: 0, background: 'var(--bg-3)', cursor: 'pointer', width: 30, height: 30, borderRadius: 'var(--radius-input)', color: 'var(--fg-2)', display: 'grid', placeItems: 'center', flex: 'none' }}
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

// The 2x2 hairline-separated metadata grid every drawer opens with (proto:472, 550).
export function MetaGrid({ items }: { items: { label: string; value: ReactNode; mono?: boolean }[] }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 1, background: 'var(--line-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', marginBottom: 22 }}>
      {items.map((it) => (
        <div key={it.label} style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
          <div className="label">{it.label}</div>
          <div className={it.mono === false ? undefined : 'mono'} style={{ fontSize: 12.5, fontWeight: 600, marginTop: 4 }}>
            {it.value}
          </div>
        </div>
      ))}
    </div>
  )
}
