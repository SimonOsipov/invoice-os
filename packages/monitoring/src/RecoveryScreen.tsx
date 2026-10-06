import { useId, type ReactNode } from 'react'

// Card chrome from app SignInLoading. A div, not h1: `.asc-app h1` forces the display font.
export function RecoveryScreen({ brand }: { brand: ReactNode }): ReactNode {
  const headingId = useId()
  return (
    <div
      className="asc-app"
      style={{ minHeight: '100vh', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
    >
      <div style={{ width: '100%', maxWidth: 452, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 32px 64px -24px oklch(16% .03 210 / .42)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          {brand}
          <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>ASComply</span>
          <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 4px' }}>
            AFRICA
          </span>
        </div>
        <section aria-labelledby={headingId} style={{ padding: '44px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 14, textAlign: 'center' }}>
          <div id={headingId} role="heading" aria-level={1} style={{ margin: 0, fontSize: 15, fontWeight: 600, color: 'var(--status-red-text)' }}>
            Something went wrong
          </div>
          <p style={{ margin: 0, fontSize: 13, color: 'var(--fg-2)' }}>This page hit an unexpected error. Reload to continue.</p>
          <button type="button" className="v2-btn v2-btn-primary" onClick={() => window.location.reload()}>
            Reload page
          </button>
        </section>
      </div>
    </div>
  )
}
