// Presentational error surface (M3-06-03): renders an ApiError's message in a
// status-red card, with an optional retry action wired via onRetry. Styled purely via
// @invoice-os/design-tokens vars (--status-red-bg/-border/-text, --fg-1/-2, --line-2,
// --font-sans, --radius-md) — mirrors the verdict-banner convention in
// CreateResults.tsx and the app's `.v2-btn-ghost` look, but the retry button is fully
// self-contained (inline styles, not a dependency on the app's global v2-btn/pf-btn
// classes) since this package may be consumed by frontends that don't load that CSS.
// Presentational only: no data fetching, no wiring to a live surface (that's M3-08/09).
import type * as React from 'react'

import type { ApiError } from '../client'

export function ErrorState(props: { error: ApiError; onRetry?: () => void }): React.JSX.Element {
  const { error, onRetry } = props
  return (
    <div
      style={{
        background: 'var(--status-red-bg)',
        border: '1px solid var(--status-red-border)',
        borderRadius: 8,
        padding: '18px 20px',
        display: 'flex',
        alignItems: 'center',
        gap: 14,
        fontFamily: 'var(--font-sans)',
      }}
    >
      <span
        style={{
          flex: 'none',
          width: 38,
          height: 38,
          borderRadius: 8,
          background: 'var(--status-red-text)',
          color: '#fff',
          display: 'grid',
          placeItems: 'center',
        }}
      >
        <svg width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
          <path d="M18 6 6 18M6 6l12 12" />
        </svg>
      </span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--status-red-text)' }}>Something went wrong</div>
        <div style={{ fontSize: 13, color: 'var(--fg-2)' }}>{error.message}</div>
        {error.status ? (
          <span style={{ display: 'inline-block', marginTop: 6, fontSize: 10, letterSpacing: '0.06em', color: 'var(--status-red-text)', fontFamily: 'var(--font-mono)' }}>
            HTTP {error.status}
          </span>
        ) : null}
      </div>
      {onRetry ? (
        <button
          onClick={onRetry}
          style={{
            flex: 'none',
            display: 'inline-flex',
            alignItems: 'center',
            gap: 8,
            height: 34,
            padding: '0 14px',
            borderRadius: 'var(--radius-md)',
            fontFamily: 'var(--font-sans)',
            fontSize: 13,
            fontWeight: 500,
            border: '1px solid var(--line-2)',
            background: 'transparent',
            color: 'var(--fg-1)',
            cursor: 'pointer',
          }}
        >
          Retry
        </button>
      ) : null}
    </div>
  )
}
