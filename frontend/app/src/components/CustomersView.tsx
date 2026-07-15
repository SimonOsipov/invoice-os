// Customers & vendors — buyer master data aggregated from the active client's invoices:
// KPIs + a table with TIN-validity status, or an empty state. Ported from
// Platform.dc.html ~L733-779 + the customers slice of renderVals() (~L1462-1468).

import { fmt, fmtShort } from '../lib/format'
import { aggregateCustomers, initials } from '../lib/customers'
import { docGlyph, plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

export function CustomersView({ ctx }: { ctx: PlatformCtx }) {
  const { active } = ctx
  const custList = aggregateCustomers(active.invoices)
  const customers = custList.map((o) => ({
    name: o.name,
    initials: initials(o.name),
    tin: o.valid ? o.tin : o.tin || 'TIN MISSING',
    tinColor: o.valid ? 'var(--fg-3)' : 'var(--status-red-text)',
    count: String(o.count),
    total: fmt(o.totalNum),
    st: o.valid
      ? { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'VALID' }
      : { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'NEEDS TIN' },
  }))
  const custValid = customers.filter((c) => c.st.label === 'VALID').length
  const custKpis = [
    { label: 'Customers', value: String(customers.length) },
    { label: 'Valid TINs', value: String(custValid) },
    { label: 'Flagged', value: String(customers.length - custValid) },
    { label: 'Total billed', value: fmtShort(custList.reduce((s, o) => s + o.totalNum, 0)) },
  ]

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Customers &amp; vendors</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{active.name} · buyer master data, tax IDs &amp; billing history</p>
      </div>

      {customers.length > 0 && (
        <div>
          <div className="pf-grid-4" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 16, marginBottom: 20 }}>
            {custKpis.map((k) => (
              <div key={k.label} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: '16px 20px' }}>
                <div className="label" style={{ marginBottom: 12 }}>
                  {k.label}
                </div>
                <span className="money" style={{ fontSize: 24, fontWeight: 700 }}>
                  {k.value}
                </span>
              </div>
            ))}
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
            <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: 'minmax(120px, 1.6fr) 140px 70px 140px 104px', gap: 14, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
              <span className="label">Customer</span>
              <span className="label">Tax ID</span>
              <span className="label" style={{ textAlign: 'right' }}>Invoices</span>
              <span className="label" style={{ textAlign: 'right' }}>Total billed</span>
              <span className="label">Tax status</span>
            </div>
            {customers.map((c) => (
              <div key={c.name} className="pf-list-row" style={{ display: 'grid', gridTemplateColumns: 'minmax(120px, 1.6fr) 140px 70px 140px 104px', gap: 14, padding: '13px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
                <span style={{ display: 'flex', alignItems: 'center', gap: 11, minWidth: 0 }}>
                  <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 6, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{c.initials}</span>
                  <span style={{ fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{c.name}</span>
                </span>
                <span className="mono" style={{ fontSize: 12, color: c.tinColor }}>{c.tin}</span>
                <span className="money mono" style={{ fontSize: 13, textAlign: 'right' }}>{c.count}</span>
                <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{c.total}</span>
                <span>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: c.st.bg, border: `1px solid ${c.st.border}`, borderRadius: 999, padding: '3px 9px' }}>
                    <span style={{ width: 6, height: 6, borderRadius: 99, background: c.st.text }} />
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: c.st.text }}>{c.st.label}</span>
                  </span>
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {customers.length === 0 && (
        <div style={{ background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 10, padding: 56, display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
          <span style={{ width: 44, height: 44, borderRadius: 10, background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{docGlyph}</span>
          <div style={{ fontSize: 16, fontWeight: 600, marginBottom: 4 }}>No customers yet</div>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: '0 0 20px', maxWidth: 320 }}>Customers appear automatically as you create invoices for {active.short}.</p>
          <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
            <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
          </button>
        </div>
      )}
    </div>
  )
}
