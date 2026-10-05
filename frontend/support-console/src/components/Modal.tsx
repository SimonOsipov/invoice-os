import type { ReactNode } from 'react'

// The centred confirm/diff modal shell. proto:640 and proto:663 are the same markup at
// two widths — a scrim that closes on click, and a card that stops propagation so a click
// inside it doesn't reach the scrim.
//
// z 90: above the drawers (80/81), below the toast (95), so the confirmation toast still
// reads over a closing modal.

type Props = {
  width?: number
  onClose: () => void
  children: ReactNode
}

export function Modal({ width = 440, onClose, children }: Props) {
  return (
    <div
      onClick={onClose}
      style={{ position: 'fixed', inset: 0, zIndex: 90, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'grid', placeItems: 'center', animation: 'opsFade 140ms ease-out', padding: 16 }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        style={{ width, maxWidth: '92vw', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', overflow: 'hidden', boxShadow: 'var(--shadow-card)', animation: 'opsPop 160ms ease-out' }}
      >
        {children}
      </div>
    </div>
  )
}
