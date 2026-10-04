// Presentational empty-state block. Optional props: `action` (last child), `dense`, `messageMaxWidth`.
import type * as React from 'react'

export function EmptyState(props: {
  title?: string
  message?: string
  action?: React.ReactNode
  dense?: boolean
  messageMaxWidth?: number
}): React.JSX.Element {
  const { title, message, action, dense, messageMaxWidth } = props
  return (
    <div
      style={{
        background: dense ? 'transparent' : 'var(--bg-2)',
        border: '1px dashed var(--line-3)',
        borderRadius: 'var(--radius-md)',
        padding: dense ? 48 : 56,
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        textAlign: 'center',
        fontFamily: 'var(--font-sans)',
      }}
    >
      <span
        style={{
          width: dense ? 40 : 44,
          height: dense ? 40 : 44,
          borderRadius: 'var(--radius-md)',
          background: 'var(--bg-3)',
          color: 'var(--fg-3)',
          display: 'grid',
          placeItems: 'center',
          marginBottom: dense ? 12 : 14,
        }}
      >
        <svg width={20} height={20} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
          <rect x="4" y="4" width="16" height="16" rx="2" />
          <path d="M4 9h16M9 4v16" />
        </svg>
      </span>
      {title ? <div style={{ fontSize: dense ? 15 : 16, fontWeight: 700, marginBottom: 4, color: 'var(--fg-1)' }}>{title}</div> : null}
      {message ? (
        <p
          style={{
            fontSize: dense ? 13 : 14,
            color: 'var(--fg-3)',
            margin: action != null ? '0 0 20px' : 0,
            maxWidth: messageMaxWidth ?? 340,
            lineHeight: dense ? 1.55 : undefined,
          }}
        >
          {message}
        </p>
      ) : null}
      {action}
    </div>
  )
}
