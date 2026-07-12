// Top header bar — breadcrumb, search box (static), sandbox toggle, "New invoice" CTA.
// Ported from Platform.dc.html ~L121-137.

import { plusGlyph, searchGlyph } from '../glyphs'
import type { PlatformCtx, View } from '../types'

const CRUMB_MAP: Record<View, string> = {
  dashboard: 'Overview',
  invoices: 'Invoices',
  validation: 'Validation',
  create: 'New invoice',
  detail: 'Invoice detail',
  clients: 'Client portfolio',
  customers: 'Customers',
  reports: 'Reports',
  settings: 'Settings',
}

export function Header({ ctx }: { ctx: PlatformCtx }) {
  const { active, view, sandbox } = ctx
  const crumb = CRUMB_MAP[view] || 'Overview'

  return (
    <header style={{ flex: 'none', height: 56, borderBottom: '1px solid var(--line-1)', background: 'rgba(247,249,250,0.8)', backdropFilter: 'blur(12px)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '0 24px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
          {active.initials}
        </span>
        <span style={{ color: 'var(--line-3)' }}>/</span>
        <span style={{ fontSize: 14, fontWeight: 600 }}>{crumb}</span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <div className="pf-header-search" style={{ display: 'flex', alignItems: 'center', gap: 8, height: 34, padding: '0 12px', border: '1px solid var(--line-2)', borderRadius: 6, background: 'var(--bg-2)', width: 240 }}>
          <span style={{ color: 'var(--fg-3)' }}>{searchGlyph}</span>
          <span style={{ fontSize: 13, color: 'var(--fg-4)' }}>Search invoices, TINs…</span>
        </div>
        <button
          onClick={ctx.toggleSandbox}
          className="pf-btn"
          style={{ display: 'flex', alignItems: 'center', gap: 7, height: 34, padding: '0 11px', borderRadius: 6, border: `1px solid ${sandbox ? 'var(--status-amber-border)' : 'var(--line-2)'}`, background: sandbox ? 'var(--status-amber-bg)' : 'transparent', color: sandbox ? 'var(--status-amber-text)' : 'var(--fg-3)', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 600, letterSpacing: '0.06em' }}
        >
          <span style={{ width: 6, height: 6, borderRadius: 99, background: sandbox ? 'var(--status-amber-text)' : 'var(--status-green-text)' }} />
          {sandbox ? 'SANDBOX' : 'LIVE'}
        </button>
        <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn" style={{ height: 34, padding: '0 14px' }}>
          <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
        </button>
      </div>
    </header>
  )
}
