// The audit export's outcome toast (success or an aborted download). Same fixed geometry
// and own-expiry-timer pattern as demo/PersonaToast.tsx, with no persona/DEMO_MODE coupling.

import { useEffect } from 'react'

import { closeGlyph } from '../glyphs'

const EXPORT_TOAST_MS = 5200

export function AuditExportToast({
  kind,
  text,
  onDismiss,
}: {
  kind: 'success' | 'error'
  text: string
  onDismiss: () => void
}) {
  useEffect(() => {
    const timer = setTimeout(onDismiss, EXPORT_TOAST_MS)
    return () => clearTimeout(timer)
  }, [onDismiss])

  const accent = kind === 'error' ? 'var(--status-red-text)' : 'var(--status-green-text)'

  return (
    <div
      data-testid="audit-export-toast"
      role="status"
      style={{
        position: 'fixed',
        left: 268,
        bottom: 18,
        zIndex: 200,
        display: 'flex',
        alignItems: 'center',
        gap: 11,
        background: 'var(--bg-2)',
        border: '1px solid var(--line-2)',
        borderLeft: `3px solid ${accent}`,
        borderRadius: 'var(--radius-md)',
        boxShadow: '0 16px 40px -16px oklch(20% .02 210 / 0.28)',
        padding: '11px 14px',
        maxWidth: 440,
        animation: 'popIn 160ms ease-out',
      }}
    >
      <span style={{ flex: 1, minWidth: 0, fontSize: 13, lineHeight: 1.5, color: 'var(--fg-1)' }}>{text}</span>
      <button
        type="button"
        data-testid="audit-export-toast-dismiss"
        onClick={onDismiss}
        aria-label="Dismiss"
        style={{ flex: 'none', background: 'transparent', border: 0, cursor: 'pointer', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', padding: 4 }}
      >
        {closeGlyph}
      </button>
    </div>
  )
}
