// Presentational empty-state block (M3-06-03): optional title/message inside a muted,
// dashed card — mirrors the app's existing empty-list convention (see
// InvoicesList.tsx's "No invoices yet" card). Styled via @invoice-os/design-tokens
// (--bg-2/-3, --line-3, --fg-1/-3, --font-sans). Presentational only: no data
// fetching, no wiring to a live surface (that's M3-08/09).
import type * as React from 'react'

export function EmptyState(props: { title?: string; message?: string }): React.JSX.Element {
  const { title, message } = props
  return (
    <div
      style={{
        background: 'var(--bg-2)',
        border: '1px dashed var(--line-3)',
        borderRadius: 'var(--radius-xl)',
        padding: 56,
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        textAlign: 'center',
        fontFamily: 'var(--font-sans)',
      }}
    >
      <span
        style={{
          width: 44,
          height: 44,
          borderRadius: 'var(--radius-xl)',
          background: 'var(--bg-3)',
          color: 'var(--fg-3)',
          display: 'grid',
          placeItems: 'center',
          marginBottom: 14,
        }}
      >
        <svg width={20} height={20} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
          <rect x="4" y="4" width="16" height="16" rx="2" />
          <path d="M4 9h16M9 4v16" />
        </svg>
      </span>
      {title ? <div style={{ fontSize: 16, fontWeight: 600, marginBottom: 4, color: 'var(--fg-1)' }}>{title}</div> : null}
      {message ? <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0, maxWidth: 320 }}>{message}</p> : null}
    </div>
  )
}
