// Reports & analytics — tax KPIs, top-customers bar list, validation summary, and export
// buttons; or an empty state. Ported from Platform.dc.html ~L780-846 + the reports slice
// of renderVals() (~L1469-1477).

import { EXPORTS_LIST } from '../data'
import { amount, fmt, fmtShort } from '../lib/format'
import { aggregateCustomers } from '../lib/customers'
import { validate } from '../lib/validation'
import { crossGlyph, docGlyph, downloadGlyph, plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

const WHT_RE = /servic|consult|support|warehous|leasing/i

export function ReportsView({ ctx }: { ctx: PlatformCtx }) {
  const { active } = ctx
  const av = active.invoices
  const custList = aggregateCustomers(av)

  const taxable = av.reduce((s, i) => s + amount(i.items), 0)
  const outVat = taxable * 0.075
  const whtTotal = av.reduce((s, i) => {
    if (!i.wht) return s
    const sv = i.items.filter((it) => WHT_RE.test(it.desc)).reduce((x, it) => x + it.qty * it.price, 0)
    return s + sv * 0.05
  }, 0)
  const reportKpis = [
    { label: 'Taxable value', value: fmtShort(taxable), color: 'var(--fg-1)' },
    { label: 'Output VAT · 7.5%', value: fmtShort(outVat), color: 'var(--accent)' },
    { label: 'WHT withheld · 5%', value: fmtShort(whtTotal), color: 'var(--fg-1)' },
    { label: 'Invoices in period', value: String(av.length), color: 'var(--fg-1)' },
  ]
  const tcMax = Math.max(1, ...custList.map((o) => o.totalNum))
  const topCustomers = custList.slice(0, 5).map((o) => ({ name: o.name, total: fmt(o.totalNum), bar: Math.round((o.totalNum / tcMax) * 100) + '%' }))
  const repErrs = av.map((i) => validate(i))
  const repPassed = av.filter((_i, k) => repErrs[k].errors.length === 0).length
  const repFail = av.length - repPassed
  const repPassPct = av.length ? Math.round((repPassed / av.length) * 100) : 0
  const reportFailures = active.dash?.failures ?? []

  if (av.length === 0) {
    return (
      <div style={{ padding: '30px 36px 56px', maxWidth: 1280 }}>
        <div style={{ marginBottom: 22 }}>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Reports &amp; analytics</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{active.name} · tax summary, period to date · June 2026</p>
        </div>
        <div style={{ background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 10, padding: 56, display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
          <span style={{ width: 44, height: 44, borderRadius: 10, background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{docGlyph}</span>
          <div style={{ fontSize: 16, fontWeight: 600, marginBottom: 4 }}>No data to report yet</div>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: '0 0 20px', maxWidth: 340 }}>Reports populate once {active.short} has validated invoices in the period.</p>
          <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
            <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
          </button>
        </div>
      </div>
    )
  }

  return (
    <div style={{ padding: '30px 36px 56px', maxWidth: 1280 }}>
      <div style={{ marginBottom: 22 }}>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Reports &amp; analytics</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{active.name} · tax summary, period to date · June 2026</p>
      </div>
      <div className="pf-grid-4" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 16, marginBottom: 20 }}>
        {reportKpis.map((k) => (
          <div key={k.label} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: '18px 20px' }}>
            <div className="label" style={{ marginBottom: 12 }}>
              {k.label}
            </div>
            <span className="money" style={{ fontSize: 25, fontWeight: 700, color: k.color }}>
              {k.value}
            </span>
          </div>
        ))}
      </div>
      <div className="pf-grid-2" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1.3fr) minmax(0, 1fr)', gap: 20, marginBottom: 20 }}>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
          <div style={{ padding: '15px 20px', borderBottom: '1px solid var(--line-1)' }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Top customers by value</span>
          </div>
          <div style={{ padding: '6px 20px 12px' }}>
            {topCustomers.map((t) => (
              <div key={t.name} style={{ padding: '11px 0', borderBottom: '1px solid var(--line-1)' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', gap: 16, marginBottom: 7 }}>
                  <span style={{ fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.name}</span>
                  <span className="money" style={{ fontSize: 13, fontWeight: 600, flex: 'none' }}>{t.total}</span>
                </div>
                <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 3, overflow: 'hidden' }}>
                  <div style={{ width: t.bar, height: '100%', background: 'var(--accent)', borderRadius: 3 }} />
                </div>
              </div>
            ))}
          </div>
        </div>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
          <div style={{ padding: '15px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Validation summary</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--status-green-text)' }}>
              {repPassPct}% PASS
            </span>
          </div>
          <div style={{ padding: '18px 20px' }}>
            <div style={{ display: 'flex', gap: 10, marginBottom: 16 }}>
              <div style={{ flex: 1, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 6, padding: '12px 14px' }}>
                <div className="money" style={{ fontSize: 22, fontWeight: 700, color: 'var(--status-green-text)' }}>{repPassed}</div>
                <div className="label" style={{ marginTop: 2 }}>
                  Passed
                </div>
              </div>
              <div style={{ flex: 1, background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', borderRadius: 6, padding: '12px 14px' }}>
                <div className="money" style={{ fontSize: 22, fontWeight: 700, color: 'var(--status-red-text)' }}>{repFail}</div>
                <div className="label" style={{ marginTop: 2 }}>
                  Failing
                </div>
              </div>
            </div>
            {reportFailures.length > 0 && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                {reportFailures.map((f) => (
                  <div key={f.rule} style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
                    <span style={{ color: 'var(--status-red-text)', flex: 'none' }}>{crossGlyph}</span>
                    <span style={{ flex: 1, fontSize: 12.5, color: 'var(--fg-2)' }}>{f.label}</span>
                    <span className="money mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--status-red-text)' }}>{f.count}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: '18px 20px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14 }}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>Export &amp; filings</span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            FIRS-READY FORMATS
          </span>
        </div>
        <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          {EXPORTS_LIST.map((e) => (
            <button key={e.name} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 38 }}>
              <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span> {e.name}{' '}
              <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 3, padding: '1px 5px', marginLeft: 2 }}>
                {e.fmt}
              </span>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
