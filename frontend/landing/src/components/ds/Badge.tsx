import type { ReactNode } from 'react'

export type BadgeTone = 'success' | 'progress' | 'development'

export function Badge({ tone, dot, children }: { tone: BadgeTone; dot?: boolean; children?: ReactNode }) {
  return (
    <span className={`ds-badge ds-badge--${tone}`}>
      {dot && <span className="ds-badge-dot" aria-hidden="true" />}
      {children}
    </span>
  )
}

export function TagPill({ children }: { children?: ReactNode }) {
  return <span className="ds-tag">{children}</span>
}
