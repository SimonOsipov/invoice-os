// The v2 app's loading, error and empty states, drawn as Platform.dc.html draws them,
// with the api-client trio's props.
import type { ReactNode } from 'react'
import type { ApiError } from '@invoice-os/api-client'

import { gridGlyph } from '../glyphs'

export function Loading({ label }: { label?: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '40px 0', color: 'var(--fg-3)', fontSize: 13 }}>
      <div
        style={{
          width: 16,
          height: 16,
          flex: 'none',
          borderRadius: '50%',
          border: '2px solid var(--line-2)',
          borderTopColor: 'var(--action)',
          animation: 'spin 700ms linear infinite',
        }}
      />
      {label}
    </div>
  )
}

export function ErrorState({ error, onRetry }: { error: ApiError; onRetry?: () => void }) {
  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 28, maxWidth: 520 }}>
      <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 6, color: 'var(--fg-1)' }}>Something went wrong</div>
      <p style={{ fontSize: 13, color: 'var(--fg-2)', margin: '0 0 8px', overflowWrap: 'anywhere' }}>{error.message}</p>
      {error.status ? (
        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginBottom: 16 }}>
          HTTP {error.status}
        </div>
      ) : null}
      {onRetry ? (
        <button type="button" className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34 }} onClick={onRetry}>
          Retry
        </button>
      ) : null}
    </div>
  )
}

export function EmptyState({
  title,
  message,
  glyph,
  dense,
  messageMaxWidth,
  children,
}: {
  title?: string
  message?: string
  glyph?: ReactNode
  dense?: boolean
  messageMaxWidth?: number
  children?: ReactNode
}) {
  const tile = dense ? 40 : 44
  return (
    <div
      style={{
        ...(dense ? null : { background: 'var(--bg-2)' }),
        border: '1px dashed var(--line-3)',
        borderRadius: 'var(--radius-md)',
        padding: dense ? 48 : 56,
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        textAlign: 'center',
      }}
    >
      <div
        style={{
          width: tile,
          height: tile,
          borderRadius: 'var(--radius-md)',
          background: 'var(--bg-3)',
          color: 'var(--fg-3)',
          display: 'grid',
          placeItems: 'center',
          marginBottom: dense ? 12 : 14,
        }}
      >
        {glyph ?? gridGlyph}
      </div>
      {title ? <div style={{ fontSize: dense ? 15 : 16, fontWeight: 700, marginBottom: 4, color: 'var(--fg-1)' }}>{title}</div> : null}
      {message ? (
        <p
          style={{
            fontSize: dense ? 13 : 14,
            ...(dense ? { lineHeight: 1.55 } : null),
            color: 'var(--fg-3)',
            maxWidth: messageMaxWidth ?? (dense ? 460 : 360),
            margin: children ? '0 auto 20px' : '0 auto',
          }}
        >
          {message}
        </p>
      ) : null}
      {children}
    </div>
  )
}
