// Presentational only, mounted by App as a sibling of the keyed Workspace: a successful
// switch remounts Workspace (and everything under it), which would kill a toast held there
// mid-display. Own expiry timer so App only has to swap the props, not manage the clock.
import { useEffect } from 'react'

import { accessRoleLabel, type AccessRole } from '../lib/members'
import { TOAST_DISMISS, TOAST_META, TOAST_TITLE } from './copy'
import { demoCloseGlyph } from './glyphs'
import { TOAST_MS } from './timing'

export function PersonaToast({
  name,
  initials,
  role,
  onDismiss,
}: {
  name: string
  initials: string
  role: AccessRole
  onDismiss: () => void
}) {
  useEffect(() => {
    const timer = setTimeout(onDismiss, TOAST_MS)
    return () => clearTimeout(timer)
  }, [onDismiss])

  return (
    <div
      data-testid="persona-toast"
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
        borderLeft: '3px solid var(--status-amber-text)',
        borderRadius: 'var(--radius-md)',
        boxShadow: '0 16px 40px -16px oklch(20% .02 210 / 0.28)',
        padding: '11px 14px',
        maxWidth: 440,
        animation: 'popIn 160ms ease-out',
      }}
    >
      <span
        style={{
          flex: 'none',
          width: 28,
          height: 28,
          borderRadius: 99,
          background: 'var(--slate-800)',
          color: 'var(--text-on-dark)',
          display: 'grid',
          placeItems: 'center',
          fontSize: 11,
          fontWeight: 600,
        }}
      >
        {initials}
      </span>
      <span style={{ flex: 1, minWidth: 0 }}>
        <span data-testid="persona-toast-title" style={{ display: 'block', fontSize: 13, fontWeight: 600 }}>
          {TOAST_TITLE.replace('{full name}', name)}
        </span>
        <span
          data-testid="persona-toast-meta"
          className="mono"
          style={{ display: 'block', fontSize: 10, letterSpacing: '0.04em', marginTop: 2, color: 'var(--fg-3)' }}
        >
          {TOAST_META.replace('{ROLE}', accessRoleLabel(role).toUpperCase())}
        </span>
      </span>
      <button
        type="button"
        data-testid="persona-toast-dismiss"
        onClick={onDismiss}
        aria-label={TOAST_DISMISS}
        title={TOAST_DISMISS}
        style={{ flex: 'none', background: 'transparent', border: 0, cursor: 'pointer', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', padding: 4 }}
      >
        {demoCloseGlyph}
      </button>
    </div>
  )
}
