// Top header bar — breadcrumb, search box (static), env switch, "New invoice" CTA.
// Ported from Platform.dc.html ~L121-137, except the env control: that is the
// two-segment SANDBOX|LIVE switch from the ops console (ops-console TopBar.tsx),
// deliberately adopted here in place of the prototype's single toggling pill so
// both consoles state the environment the same way.

import { useState } from 'react'

import { crossGlyph, plusGlyph, searchGlyph } from '../glyphs'
import { clampFilterText } from '../lib/invoices'
import type { PlatformCtx, View } from '../types'

const CRUMB_MAP: Record<View, string> = {
  dashboard: 'Overview',
  invoices: 'Invoices',
  validation: 'Validation',
  workflows: 'Approval workflow',
  rules: 'Validation rules',
  create: 'New invoice',
  detail: 'Invoice detail',
  clients: 'Client portfolio',
  customers: 'Customers',
  reports: 'Reports',
  settings: 'Settings',
  approvals: 'Approvals',
  audit: 'Audit log',
}

// Segment colours mirror ops-console TopBar.tsx: the active segment is filled with
// its status colour and its dot flips to the on-dark text colour; the inactive one
// stays transparent and keeps a tinted dot.
function segStyle(active: boolean, kind: 'sandbox' | 'live') {
  return {
    bg: active ? (kind === 'live' ? 'var(--status-green-text)' : 'var(--status-amber-text)') : 'transparent',
    color: active ? 'var(--text-on-dark)' : 'var(--fg-3)',
    dot: active ? 'var(--text-on-dark)' : kind === 'live' ? 'var(--status-green-text)' : 'var(--status-amber-text)',
  }
}

const SEG_BASE = {
  border: 0,
  cursor: 'pointer',
  height: 28,
  padding: '0 12px',
  borderRadius: 'var(--radius-input)',
  fontFamily: 'var(--font-mono)',
  fontSize: 10,
  fontWeight: 700,
  letterSpacing: '0.06em',
  display: 'inline-flex',
  alignItems: 'center',
  gap: 6,
} as const

export function Header({ ctx }: { ctx: PlatformCtx }) {
  const { active, view, sandbox } = ctx
  const crumb = CRUMB_MAP[view] || 'Overview'
  const sbx = segStyle(sandbox, 'sandbox')
  const [query, setQuery] = useState('')

  return (
    <header style={{ flex: 'none', height: 56, borderBottom: '1px solid var(--line-1)', background: 'oklch(98.5% .008 85 / .82)', backdropFilter: 'blur(12px)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '0 24px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
          {active.initials}
        </span>
        <span style={{ color: 'var(--line-3)' }}>/</span>
        <span style={{ fontSize: 14, fontWeight: 600 }}>{crumb}</span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            ctx.setInvoiceQuery(clampFilterText(query))
            ctx.nav('invoices')
          }}
          className="pf-header-search"
          style={{ display: 'flex', alignItems: 'center', gap: 8, height: 34, padding: '0 12px', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', background: 'var(--bg-2)', width: 240 }}
        >
          <span style={{ color: 'var(--fg-3)', display: 'inline-flex' }}>{searchGlyph}</span>
          <input
            type="text"
            data-testid="invoice-search-input"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            maxLength={200}
            placeholder="Search invoices, TINs…"
            style={{ flex: 1, minWidth: 0, border: 'none', outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)' }}
          />
          {query !== '' && (
            <button
              type="button"
              data-testid="invoice-search-clear"
              onClick={() => {
                setQuery('')
                ctx.setInvoiceQuery('')
              }}
              aria-label="Clear search"
              style={{ display: 'inline-flex', alignItems: 'center', border: 0, padding: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer' }}
            >
              {crossGlyph}
            </button>
          )}
        </form>
        {/* Sandbox / Live switch — segment heights (28 + 2px padding + 1px border = 34)
            keep the control flush with the search box and "New invoice" beside it. */}
        <div data-testid="env-pill" style={{ display: 'flex', alignItems: 'center', background: 'var(--bg-2)', border: `1px solid ${sandbox ? 'var(--status-amber-border)' : 'var(--status-green-border)'}`, borderRadius: 'var(--radius-md)', padding: 2 }}>
          <button
            type="button"
            onClick={() => ctx.setSandbox(true)}
            aria-pressed={sandbox}
            className="pf-btn"
            style={{ ...SEG_BASE, background: sbx.bg, color: sbx.color }}
          >
            <span style={{ width: 6, height: 6, borderRadius: 99, background: sbx.dot }} />
            SANDBOX
          </button>
          {/* Disabled, not hidden — InvoiceDetail.tsx:406-444's idiom, with the banner
              sentence below the header as the visible reason. The dot mutes too, or LIVE
              reads "unselected"; no `--bg-3` fill, since in a segment a fill means selected. */}
          <button
            type="button"
            data-testid="env-pill-live"
            onClick={() => ctx.setSandbox(false)}
            disabled
            aria-pressed={!sandbox}
            title="Live filing switches on at NRS accreditation."
            className="pf-btn"
            style={{ ...SEG_BASE, background: 'transparent', color: 'var(--fg-4)', cursor: 'not-allowed' }}
          >
            <span style={{ width: 6, height: 6, borderRadius: 99, background: 'var(--fg-4)' }} />
            LIVE
          </button>
        </div>
        <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn" style={{ height: 34, padding: '0 14px' }}>
          <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
        </button>
      </div>
    </header>
  )
}
