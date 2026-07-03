// Clients / partner portal — portfolio KPIs + a per-company table (firm mode only).
// Ported from Platform.dc.html ~L695-732 + the portfolio slice of renderVals()
// (~L1437-1451).

import { fmtShort } from '../lib/format'
import { statusStyle } from '../lib/clients'
import { plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

export function ClientsView({ ctx }: { ctx: PlatformCtx }) {
  const { clients, activeIdx } = ctx

  const scored = clients.filter((c) => c.score != null)
  const avg = Math.round(scored.reduce((s, c) => s + (c.score as number), 0) / scored.length)
  const vatTotal = clients.reduce((s, c) => s + c.vatNum, 0)
  const openFail = clients.reduce((s, c) => s + (typeof c.failing === 'number' ? c.failing : 0), 0)

  const portfolioKpis = [
    { label: 'Companies', value: String(clients.length), color: 'var(--fg-1)' },
    { label: 'Avg. readiness', value: avg + '%', color: 'var(--accent)' },
    { label: 'VAT tracked', value: fmtShort(vatTotal), color: 'var(--fg-1)' },
    { label: 'Open failures', value: String(openFail), color: 'var(--status-red-text)' },
  ]

  return (
    <div style={{ padding: '30px 36px 56px', maxWidth: 1280 }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 22 }}>
        <div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Client portfolio</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>Okafor &amp; Partners · 6 companies · partner program</p>
        </div>
        <button className="v2-btn v2-btn-primary pf-btn">
          <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> Add client
        </button>
      </div>
      <div className="pf-grid-4" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 20, marginBottom: 20 }}>
        {portfolioKpis.map((k) => (
          <div key={k.label} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: '18px 20px' }}>
            <div className="label" style={{ marginBottom: 12 }}>
              {k.label}
            </div>
            <span className="money" style={{ fontSize: 26, fontWeight: 700, color: k.color }}>
              {k.value}
            </span>
          </div>
        ))}
      </div>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
        <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: '1fr 110px 150px 120px 130px', gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
          <span className="label">Company</span>
          <span className="label" style={{ textAlign: 'right' }}>Readiness</span>
          <span className="label">VAT tracked</span>
          <span className="label" style={{ textAlign: 'right' }}>Failing</span>
          <span className="label">Status</span>
        </div>
        {clients.map((c, i) => {
          const sc = c.score == null ? NaN : c.score
          const st = statusStyle(c.head)
          const scoreColor = isNaN(sc) ? 'var(--fg-4)' : sc >= 85 ? 'var(--status-green-text)' : sc >= 70 ? 'var(--status-amber-text)' : 'var(--status-red-text)'
          const failColor = c.failing === 0 || c.failing === '—' ? (c.failing === '—' ? 'var(--fg-4)' : 'var(--status-green-text)') : 'var(--status-red-text)'
          return (
            <div
              key={c.name}
              onClick={() => ctx.switchClient(i)}
              className="pf-row pf-list-row"
              style={{ display: 'grid', gridTemplateColumns: '1fr 110px 150px 120px 130px', gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', background: i === activeIdx ? 'var(--accent-tint)' : 'transparent' }}
            >
              <span style={{ display: 'flex', alignItems: 'center', gap: 12, minWidth: 0 }}>
                <span style={{ flex: 'none', width: 32, height: 32, borderRadius: 6, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 12, fontWeight: 700 }}>{c.initials}</span>
                <span style={{ minWidth: 0 }}>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13.5, fontWeight: 500 }}>
                    <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{c.name}</span>
                    {i === activeIdx && (
                      <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--accent)', background: 'var(--accent-tint)', padding: '1px 5px', borderRadius: 3, flex: 'none' }}>
                        ACTIVE
                      </span>
                    )}
                  </span>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>TIN {c.tin}</span>
                </span>
              </span>
              <span style={{ textAlign: 'right' }}>
                <span className="money mono" style={{ fontSize: 14, fontWeight: 600, color: scoreColor }}>
                  {c.score == null ? '—' : c.score + '%'}
                </span>
              </span>
              <span className="money" style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{c.onboarding ? '₦0' : c.vatLabel}</span>
              <span style={{ textAlign: 'right' }}>
                <span className="money mono" style={{ fontSize: 13, fontWeight: 600, color: failColor }}>
                  {c.onboarding ? '—' : String(c.failing)}
                </span>
              </span>
              <span>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '3px 9px' }}>
                  <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                  <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
                </span>
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
