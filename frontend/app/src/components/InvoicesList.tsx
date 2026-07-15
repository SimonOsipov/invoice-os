// Invoices list — status filter chips + table, or an empty state when the client has
// no invoices yet. Ported from Platform.dc.html ~L343-387.

import { amount, fmt } from '../lib/format'
import { statusStyle } from '../lib/clients'
import { docGlyph, importGlyph, plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

const FILTER_DEFS: [string, string][] = [
  ['all', 'All'],
  ['Pending', 'Pending'],
  ['Approved', 'Approved'],
  ['Transmitted', 'Transmitted'],
  ['Draft', 'Draft'],
  ['Rejected', 'Rejected'],
]

const VALID_TIN = (t: string) => /^\d{8}-\d{4}$/.test(t)

export function InvoicesList({ ctx }: { ctx: PlatformCtx }) {
  const { active, filter } = ctx
  const invList = active.invoices
  const counts: Record<string, number> = { all: invList.length }
  ;['Pending', 'Approved', 'Transmitted', 'Draft', 'Rejected'].forEach((st) => (counts[st] = invList.filter((i) => i.status === st).length))
  const visible = invList.filter((i) => filter === 'all' || i.status === filter)

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 22 }}>
        <div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Invoices</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{active.name} · create, validate, and transmit.</p>
        </div>
        <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
          <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
        </button>
      </div>

      {invList.length > 0 && (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
            {FILTER_DEFS.map(([id, label]) => {
              const a = filter === id
              return (
                <button
                  key={id}
                  onClick={() => ctx.setFilter(id)}
                  className="pf-chip"
                  style={{ height: 30, padding: '0 12px', borderRadius: 6, fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 500, border: `1px solid ${a ? 'var(--accent)' : 'var(--line-2)'}`, background: a ? 'var(--accent)' : 'var(--bg-2)', color: a ? '#fff' : 'var(--fg-2)', display: 'inline-flex', alignItems: 'center', gap: 7 }}
                >
                  {label} <span className="mono" style={{ fontSize: 10, opacity: 0.7 }}>{id === 'all' ? counts.all : counts[id] || 0}</span>
                </button>
              )
            })}
            <div style={{ flex: 1 }} />
            <button className="v2-btn v2-btn-ghost pf-btn" style={{ height: 30, fontSize: 12.5, padding: '0 12px' }}>
              <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Import CSV
            </button>
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
            <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: '150px 1fr 140px 120px 130px', gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
              <span className="label">Invoice #</span>
              <span className="label">Buyer</span>
              <span className="label" style={{ textAlign: 'right' }}>Amount</span>
              <span className="label">Date</span>
              <span className="label">Status</span>
            </div>
            {visible.map((r) => {
              const st = statusStyle(r.status)
              const validTin = VALID_TIN(r.buyerTin)
              return (
                <div
                  key={r.number}
                  onClick={() => ctx.selectInvoice(r.number)}
                  className="pf-row pf-list-row"
                  style={{ display: 'grid', gridTemplateColumns: '150px 1fr 140px 120px 130px', gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
                >
                  <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.number}</span>
                  <span style={{ minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.buyer}</span>
                    <span className="mono" style={{ fontSize: 11, color: validTin ? 'var(--fg-3)' : 'var(--status-red-text)' }}>{validTin ? r.buyerTin : r.buyerTin || 'TIN MISSING'}</span>
                  </span>
                  <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{fmt(amount(r.items) * 1.075)}</span>
                  <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{r.date}</span>
                  <span>
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '3px 9px' }}>
                      <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                      <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text, letterSpacing: '0.04em' }}>{st.label}</span>
                    </span>
                  </span>
                </div>
              )
            })}
          </div>
        </div>
      )}

      {invList.length === 0 && (
        <div style={{ background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 10, padding: 56, display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
          <span style={{ width: 44, height: 44, borderRadius: 10, background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{docGlyph}</span>
          <div style={{ fontSize: 16, fontWeight: 600, marginBottom: 4 }}>No invoices yet</div>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: '0 0 20px', maxWidth: 320 }}>Create or import an invoice for {active.short} to start tracking compliance.</p>
          <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
            <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
          </button>
        </div>
      )}
    </div>
  )
}
