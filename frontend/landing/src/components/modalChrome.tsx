// Shared v2 modal chrome: scrim, card, header. No hooks, no state.

import type { CSSProperties } from 'react'
import { GLYPHS, Icon } from '../icons'
import { Logo } from './ds/Logo'

export const MODAL_SCRIM_STYLE: CSSProperties = {
  position: 'fixed',
  inset: 0,
  zIndex: 200,
  background: 'color-mix(in srgb, var(--surface) 55%, transparent)',
  backdropFilter: 'blur(6px)',
  WebkitBackdropFilter: 'blur(6px)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 24,
  animation: 'ovIn 160ms ease-out',
}

export function modalCardStyle(maxWidth: number): CSSProperties {
  return {
    width: '100%',
    maxWidth,
    maxHeight: 'calc(100dvh - 48px)',
    overflowY: 'auto',
    background: 'var(--card)',
    border: '1px solid var(--border)',
    borderRadius: 'var(--radius-md)',
    boxShadow: 'var(--shadow-elegant)',
    animation: 'cardIn 200ms var(--ease-out)',
  }
}

export const MODAL_CHROME_CSS = `
  @keyframes ovIn { from { opacity: 0; } to { opacity: 1; } }
  @keyframes cardIn { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: none; } }
  .si-close { transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out); }
  .si-close:hover { background: var(--muted); color: var(--ink); }
  .si-close:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }
`

export function ModalHeader({ onClose, padX }: { onClose?: () => void; padX: 18 | 20 }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: `14px ${padX}px`, borderBottom: '1px solid var(--border)' }}>
      <Logo size={28} />
      {onClose && (
        <button
          onClick={onClose}
          className="si-close"
          aria-label="Close"
          style={{ flex: 'none', width: 36, height: 36, borderRadius: 'var(--radius-btn)', border: 0, background: 'transparent', color: 'var(--muted-foreground)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
        >
          <Icon paths={GLYPHS.x} size={18} strokeWidth={2} />
        </button>
      )}
    </div>
  )
}
