// Usage & billing — the client-facing spend screen (prototype lines 448–526).
// Mock UI only: no billing backend, no persistence, no payment flow. Real metering
// and invoicing are out of M4.
//
// Every figure on this screen is either a seed literal ported from the prototype or a
// value produced by an already-unit-tested `charts.ts` export. Nothing is re-derived
// here — see the notes on QUOTA/BILL_ITEMS in data.tsx for which is which.

import { BILL_ITEMS, EXPORT_ICON, INVOICE_STATUS, PAST_INVOICES, QUOTA } from '../data'
import { computeQuota, fmt, nairaC, SCALE_PLAN, spendTotals } from '../charts'

type Props = {
  onManagePlan: () => void
  onDownloadInvoice: (id: string) => void
}

// proto:493/495/502 and 513/515 — hoisted so the header row and every body row cannot
// drift apart. `minWidth` is repeated on both (the house pattern from Evidence.tsx):
// setting it only on the wrapper lets the grid tracks collapse below it.
//
// `.ops-usage-table` / `.ops-invoice-table` are naming contracts for M4-20-08's audit
// grep — no CSS rule resolves off them (nor off `.ops-evidence-table`/`.ops-jobs-table`),
// so do NOT add one and do NOT reflow these tables at any breakpoint: wide tables
// scroll, they do not stack. Because nothing resolves off the name, where it is applied
// is free, and the two existing screens disagree — Evidence.tsx:76 puts it on the header
// alone, Submissions.tsx:150/179 on the header and every row. Following Submissions: it
// names every element that actually shares the grid.
//
// USAGE_MIN_WIDTH is driven by the total row, not the 180px first track: `Total ·
// projected month-end` at 13.5px/700 needs ~205px, plus ~175px for the longest detail
// string, plus the 120+130 fixed tracks and 18px×2 of row padding.
const USAGE_GRID = 'minmax(180px,1.4fr) 1fr 120px 130px'
const USAGE_MIN_WIDTH = 680
const INVOICE_GRID = '150px 1fr 150px 110px 110px'
const INVOICE_MIN_WIDTH = 690

export function Billing({ onManagePlan, onDownloadInvoice }: Props) {
  // `.over` is this screen's only consumer of computeQuota. `.pct`, `.widthPct` and
  // `.detail` belong to the sidebar's compact widget (proto:1081, shipped in M4-20-02)
  // and appear nowhere in proto:468-487 — this meter shows full digits and no
  // percentage at all.
  const { over } = computeQuota(QUOTA.used, SCALE_PLAN.includedRequests)
  const spend = spendTotals()

  return (
    <div className="ops-screen-pad">
      <div style={{ marginBottom: 20 }}>
        <div className="label" style={{ marginBottom: 8 }}>
          / 05 — USAGE &amp; BILLING
        </div>
        <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Usage &amp; billing</h1>
      </div>

      {/* plan + usage (proto:457-488) */}
      <div className="ops-billing-grid" style={{ display: 'grid', gridTemplateColumns: '300px minmax(0,1fr)', gap: 16, marginBottom: 24 }}>
        <div style={{ border: '1px solid var(--line-1)', background: 'var(--bg-2)', borderRadius: 'var(--radius-xl)', padding: '18px 20px' }}>
          <div className="label" style={{ marginBottom: 10 }}>
            Current plan
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 9, marginBottom: 14 }}>
            <span style={{ fontSize: 22, fontWeight: 700, letterSpacing: '-0.02em' }}>Scale</span>
            <span
              className="mono"
              style={{ fontSize: 9.5, fontWeight: 700, background: 'var(--accent-tint)', color: 'var(--accent)', border: '1px solid var(--teal-200)', borderRadius: 'var(--radius-md)', padding: '2px 8px' }}
            >
              ACTIVE
            </span>
          </div>
          {/* Three static rows, not a map: they have no natural key, so mapping them
              would force `key={i}`. The figures read from SCALE_PLAN. */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12.5 }}>
              <span style={{ color: 'var(--fg-2)' }}>Unit price</span>
              <span className="mono" style={{ fontWeight: 600 }}>
                ₦{SCALE_PLAN.clearedRate} / cleared invoice
              </span>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12.5 }}>
              <span style={{ color: 'var(--fg-2)' }}>Included quota</span>
              <span className="mono" style={{ fontWeight: 600 }}>
                {fmt(SCALE_PLAN.includedRequests)} / mo
              </span>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12.5 }}>
              <span style={{ color: 'var(--fg-2)' }}>Overage rate</span>
              <span className="mono" style={{ fontWeight: 600 }}>
                ₦{SCALE_PLAN.overageRate} / request
              </span>
            </div>
          </div>
          <button type="button" onClick={onManagePlan} className="ops-btn v2-btn v2-btn-ghost" style={{ width: '100%', justifyContent: 'center', height: 36, marginTop: 16 }}>
            Manage plan
          </button>
        </div>

        {/* quota + overage meter (proto:468-487) */}
        <div style={{ border: '1px solid var(--line-1)', background: 'var(--bg-2)', borderRadius: 'var(--radius-xl)', padding: '18px 20px' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14 }}>
            <div className="label">Usage vs included quota · July</div>
            <span
              className="mono"
              style={{
                fontSize: 10,
                fontWeight: 700,
                color: 'var(--status-amber-text)',
                background: 'var(--status-amber-bg)',
                border: '1px solid var(--status-amber-border)',
                borderRadius: 'var(--radius-md)',
                padding: '2px 8px',
              }}
            >
              OVER QUOTA
            </span>
          </div>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 12 }}>
            <span className="mono" style={{ fontSize: 30, fontWeight: 700, letterSpacing: '-0.02em' }}>
              {fmt(QUOTA.used)}
            </span>
            <span className="mono" style={{ fontSize: 13, color: 'var(--fg-3)' }}>
              / {fmt(SCALE_PLAN.includedRequests)} requests
            </span>
          </div>
          {/* Two flex segments summing to 100%, not a single fill over a track — the
              widths are prototype literals (proto:475-476), see data.tsx's Quota note. */}
          <div style={{ height: 12, background: 'var(--bg-3)', borderRadius: 'var(--radius-lg)', overflow: 'hidden', display: 'flex', marginBottom: 8 }}>
            <div style={{ width: QUOTA.includedWidth, height: '100%', background: 'var(--accent)' }} />
            <div style={{ width: QUOTA.overWidth, height: '100%', background: 'var(--status-amber-text)' }} />
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <span style={{ width: 9, height: 9, borderRadius: 'var(--radius-xs)', background: 'var(--accent)' }} />
              <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                {fmt(SCALE_PLAN.includedRequests)} INCLUDED
              </span>
            </span>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <span style={{ width: 9, height: 9, borderRadius: 'var(--radius-xs)', background: 'var(--status-amber-text)' }} />
              <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                {fmt(over)} OVERAGE
              </span>
            </span>
          </div>
          <div
            className="ops-billing-kpis"
            style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12, marginTop: 20, borderTop: '1px solid var(--line-1)', paddingTop: 16 }}
          >
            <div>
              <div className="mono" style={{ fontSize: 18, fontWeight: 600 }}>
                {fmt(QUOTA.clearedInvoices)}
              </div>
              <div className="label" style={{ marginTop: 3 }}>
                Cleared invoices
              </div>
            </div>
            <div>
              <div className="mono" style={{ fontSize: 18, fontWeight: 600 }}>
                {fmt(QUOTA.evidenceExports)}
              </div>
              <div className="label" style={{ marginTop: 3 }}>
                Evidence exports
              </div>
            </div>
            <div>
              <div className="mono" style={{ fontSize: 18, fontWeight: 600, color: 'var(--accent)' }}>
                {nairaC(spend.mtd)}
              </div>
              <div className="label" style={{ marginTop: 3 }}>
                Spend to date
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* itemized spend (proto:491-508) */}
      <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 12 }}>Itemized spend · July 2026</div>
      <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', background: 'var(--bg-2)', overflowX: 'auto', marginBottom: 28 }}>
        <div
          className="ops-usage-table"
          style={{
            display: 'grid',
            gridTemplateColumns: USAGE_GRID,
            padding: '11px 18px',
            background: 'var(--bg-1)',
            borderBottom: '1px solid var(--line-1)',
            minWidth: USAGE_MIN_WIDTH,
          }}
        >
          <span className="label">Item</span>
          <span className="label">Detail</span>
          <span className="label" style={{ textAlign: 'right' }}>
            Qty
          </span>
          <span className="label" style={{ textAlign: 'right' }}>
            Amount
          </span>
        </div>
        {BILL_ITEMS.map((b) => (
          <div
            key={b.label}
            className="ops-usage-table"
            style={{
              display: 'grid',
              gridTemplateColumns: USAGE_GRID,
              padding: '13px 18px',
              borderBottom: '1px solid var(--line-1)',
              alignItems: 'center',
              minWidth: USAGE_MIN_WIDTH,
            }}
          >
            <span style={{ fontSize: 13, fontWeight: 500 }}>{b.label}</span>
            <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-3)' }}>
              {b.detail}
            </span>
            <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', textAlign: 'right' }}>
              {b.qty}
            </span>
            <span className="mono" style={{ fontSize: 12.5, fontWeight: 600, textAlign: 'right', color: b.color }}>
              {b.amount}
            </span>
          </div>
        ))}
        {/* Total row (proto:502-507). It renders the PROJECTED month-end spend, which is
            NOT the sum of the four rows above — that is the prototype's own data, see
            BILL_ITEMS. The empty third span holds the Qty track and must stay. */}
        <div
          className="ops-usage-table"
          style={{ display: 'grid', gridTemplateColumns: USAGE_GRID, padding: '14px 18px', background: 'var(--bg-1)', alignItems: 'center', minWidth: USAGE_MIN_WIDTH }}
        >
          <span style={{ fontSize: 13.5, fontWeight: 700 }}>Total · projected month-end</span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            {nairaC(spend.mtd)} to date
          </span>
          <span />
          <span className="mono" style={{ fontSize: 15, fontWeight: 700, textAlign: 'right' }}>
            {nairaC(spend.proj)}
          </span>
        </div>
      </div>

      {/* past invoices (proto:511-523) */}
      <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 12 }}>Invoices from FiscalBridge</div>
      <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', background: 'var(--bg-2)', overflowX: 'auto' }}>
        <div
          className="ops-invoice-table"
          style={{
            display: 'grid',
            gridTemplateColumns: INVOICE_GRID,
            padding: '11px 18px',
            background: 'var(--bg-1)',
            borderBottom: '1px solid var(--line-1)',
            minWidth: INVOICE_MIN_WIDTH,
          }}
        >
          <span className="label">Invoice</span>
          <span className="label">Period</span>
          <span className="label" style={{ textAlign: 'right' }}>
            Amount
          </span>
          <span className="label">Status</span>
          <span className="label" />
        </div>
        {PAST_INVOICES.map((p) => {
          const st = INVOICE_STATUS[p.kind]
          return (
            <div
              key={p.id}
              className="ops-invoice-table"
              style={{
                display: 'grid',
                gridTemplateColumns: INVOICE_GRID,
                padding: '13px 18px',
                borderBottom: '1px solid var(--line-1)',
                alignItems: 'center',
                minWidth: INVOICE_MIN_WIDTH,
              }}
            >
              <span className="mono" style={{ fontSize: 12, fontWeight: 600 }}>
                {p.id}
              </span>
              <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{p.period}</span>
              <span className="mono" style={{ fontSize: 12.5, fontWeight: 600, textAlign: 'right' }}>
                {p.amount}
              </span>
              <span>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: st.bg, border: '1px solid ' + st.border, borderRadius: 999, padding: '2px 9px' }}>
                  <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: st.text }}>
                    {st.label}
                  </span>
                </span>
              </span>
              <span style={{ textAlign: 'right' }}>
                <button
                  type="button"
                  onClick={() => onDownloadInvoice(p.id)}
                  className="ops-btn"
                  style={{
                    border: '1px solid var(--line-2)',
                    background: 'var(--bg-2)',
                    cursor: 'pointer',
                    height: 28,
                    padding: '0 10px',
                    borderRadius: 'var(--radius-md)',
                    fontFamily: 'var(--font-sans)',
                    fontSize: 11.5,
                    fontWeight: 600,
                    color: 'var(--fg-1)',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                  }}
                >
                  {EXPORT_ICON} PDF
                </button>
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
