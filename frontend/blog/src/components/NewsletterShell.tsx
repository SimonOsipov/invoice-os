import type { CSSProperties, ReactNode } from 'react'
import { Icon } from '../icons'

export const COLUMN = (gap: number): CSSProperties => ({ display: 'flex', flexDirection: 'column', gap })

// The newsletter pages share the prototype's left column; only the card changes.
export function NewsletterShell({ children }: { children: ReactNode }) {
  return (
    <section
      className="blog-px"
      style={{ position: 'relative', padding: '72px 40px 88px', overflow: 'hidden', minHeight: 'calc(100vh - 64px)' }}
    >
      <div className="hero-grid hero-grid-fade" style={{ position: 'absolute', inset: 0, opacity: 0.6 }} />
      <div style={{ position: 'relative', maxWidth: 1120, display: 'flex', flexWrap: 'wrap', gap: 56, alignItems: 'flex-start' }}>
        <div style={{ flex: '1 1 380px', ...COLUMN(22), maxWidth: 520 }}>
          <div className="t-eyebrow">Newsletter</div>
          <h1
            style={{
              margin: 0,
              fontSize: 'clamp(40px, 4.4vw, 60px)',
              lineHeight: 1.07,
              letterSpacing: '-0.05em',
              fontWeight: 700,
              color: 'var(--ink)',
            }}
          >
            New posts,<br />
            <span style={{ color: 'var(--teal)' }}>on your schedule.</span>
          </h1>
          <p className="t-lead" style={{ margin: 0 }}>
            Choose the topics you follow and how often you hear from us. Every email has a one-click unsubscribe.
          </p>
        </div>
        <div
          style={{
            flex: '1 1 420px',
            maxWidth: 540,
            background: 'var(--card)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            padding: '32px 32px 30px',
          }}
        >
          {children}
        </div>
      </div>
    </section>
  )
}

export function StatusTile() {
  return (
    <span
      style={{
        width: 40,
        height: 40,
        borderRadius: 6,
        display: 'grid',
        placeItems: 'center',
        background: 'var(--mint-soft)',
        color: 'var(--teal)',
      }}
    >
      <Icon name="circle-check" size={20} />
    </span>
  )
}
