// The audit export's outcome toast (success or an aborted download). Same fixed geometry
// and own-expiry-timer pattern as demo/PersonaToast.tsx, with no persona/DEMO_MODE coupling.

import { useEffect } from 'react'

import { Icon } from '../icons'

const EXPORT_TOAST_MS = 5200
const DEFAULT_TEST_ID = 'audit-export-toast'
export const dismissGlyph = <Icon paths={['M18 6 6 18', 'm6 6 12 12']} size={12} strokeWidth={2} />

export function AuditExportToast({
  kind,
  text,
  onDismiss,
  // AUDIT-07's assertions address this toast by its default; only the evidence-bundle
  // download passes a different one. EB-06-9's second render is the oracle.
  testId = DEFAULT_TEST_ID,
}: {
  kind: 'success' | 'error'
  text: string
  onDismiss: () => void
  testId?: string
}) {
  useEffect(() => {
    const timer = setTimeout(onDismiss, EXPORT_TOAST_MS)
    return () => clearTimeout(timer)
  }, [onDismiss])

  const accent = kind === 'error' ? 'var(--status-red-text)' : 'var(--status-green-text)'

  return (
    <div
      data-testid={testId}
      role="status"
      style={{
        position: 'fixed',
        left: 268,
        bottom: 18,
        zIndex: 200,
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        background: 'var(--bg-2)',
        border: '1px solid var(--line-2)',
        borderLeft: `3px solid ${accent}`,
        borderRadius: 'var(--radius-md)',
        boxShadow: 'var(--shadow-card)',
        padding: '11px 12px 11px 14px',
        // The bundle toast is longer and must stay clear of the open drawer panel.
        maxWidth: testId === DEFAULT_TEST_ID ? 640 : 440,
        animation: 'popIn 160ms ease-out',
      }}
    >
      <span style={{ flex: 1, minWidth: 0, fontSize: 12.5, lineHeight: 1.45, color: 'var(--fg-1)' }}>{text}</span>
      <button
        type="button"
        data-testid="audit-export-toast-dismiss"
        onClick={onDismiss}
        aria-label="Dismiss"
        className="pf-btn"
        style={{ flex: 'none', width: 22, height: 22, borderRadius: 4, background: 'transparent', border: 0, cursor: 'pointer', color: 'var(--fg-3)', display: 'grid', placeItems: 'center' }}
      >
        {dismissGlyph}
      </button>
    </div>
  )
}
