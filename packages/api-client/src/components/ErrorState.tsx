// Error card for an ApiError's message, with an optional Retry. Retry uses the app's
// v2-btn / pf-btn classes: only frontend/app renders this component.
import type * as React from 'react'

import type { ApiError } from '../client'

export function ErrorState(props: { error: ApiError; onRetry?: () => void }): React.JSX.Element {
  const { error, onRetry } = props
  return (
    <div
      style={{
        background: 'var(--bg-2)',
        border: '1px solid var(--line-1)',
        borderRadius: 'var(--radius-md)',
        padding: 28,
        maxWidth: 520,
        fontFamily: 'var(--font-sans)',
      }}
    >
      <div style={{ fontSize: 15, fontWeight: 700, color: 'var(--fg-1)', marginBottom: 6 }}>Something went wrong</div>
      <p style={{ fontSize: 13, color: 'var(--fg-2)', margin: '0 0 8px' }}>{error.message}</p>
      {error.status ? (
        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginBottom: 16 }}>
          HTTP {error.status}
        </div>
      ) : null}
      {onRetry ? (
        <button onClick={onRetry} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34 }}>
          Retry
        </button>
      ) : null}
    </div>
  )
}
