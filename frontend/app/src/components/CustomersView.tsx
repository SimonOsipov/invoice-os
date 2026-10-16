// Customers: the workspace's customer records (GET /v1/customers, paged), with Invoices and
// Total billed counted from the whole invoice set and joined by trimmed TIN.

import { useState } from 'react'

import { ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { FILEABLE_TIN, listCustomers, type CustomerListResponse } from '../lib/buyerDirectory'
import { fmt } from '../lib/format'
import { aggregateCustomers, customerName, initials } from '../lib/customers'
import { fetchAllInvoices, type AllInvoices } from '../lib/invoices'
import { docGlyph, plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'
import { Pager } from './Pager'

const PAGE_SIZE = 50
const GRID_COLUMNS = 'minmax(160px, 1.6fr) 140px 70px 70px 140px 104px'
const CELL: React.CSSProperties = { minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }

const VALID_CHIP = { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'VALID' }
const NEEDS_TIN_CHIP = { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'NEEDS TIN' }

export function CustomersView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [offset, setOffset] = useState(0)

  const records = useAsync<CustomerListResponse>(
    () => (base ? listCustomers(ctx.authedFetch, base, { limit: PAGE_SIZE, offset }) : Promise.reject(new Error('no gateway configured'))),
    { isEmpty: (r) => r.pagination.total === 0, immediate: base != null, deps: [offset] },
  )
  // Workspace-wide (no entity_id) and no isEmpty: records with zero invoices still render.
  const invoices = useAsync<AllInvoices>(
    () => (base ? fetchAllInvoices(ctx.authedFetch, base, {}) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )

  const loading = records.status === 'loading' || invoices.status === 'loading'
  const errored = records.status === 'error' || invoices.status === 'error'
  const error = records.error ?? invoices.error
  const retry = () => {
    if (records.status === 'error') records.run()
    if (invoices.status === 'error') invoices.run()
  }
  const empty = !errored && !loading && (base == null || records.status === 'empty')
  const ready = !errored && !loading && records.data != null && invoices.data != null

  const byTin = new Map(aggregateCustomers(invoices.data?.invoices ?? []).map((o) => [o.tin, o]))
  const customers = (records.data?.customers ?? []).map((r) => {
    const agg = byTin.get(r.tin)
    const name = customerName(r.name, agg?.name)
    const fileable = FILEABLE_TIN.test(r.tin)
    return {
      id: r.id,
      name,
      initials: initials(name) || '—',
      tin: r.tin,
      tinColor: fileable ? 'var(--fg-3)' : 'var(--status-red-text)',
      type: r.buyer_type ?? '—',
      count: String(agg?.count ?? 0),
      total: fmt(agg?.totalNum ?? 0),
      st: fileable ? VALID_CHIP : NEEDS_TIN_CHIP,
    }
  })

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          TRADING PARTNERS
        </div>
        <h1 style={{ fontSize: 26, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Customers</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
          {ctx.user.tenantName ? `${ctx.user.tenantName} · ` : ''}Customer records, tax IDs &amp; billing history
        </p>
      </div>

      {errored && error && <ErrorState error={error} onRetry={retry} />}

      {!errored && loading && <Loading label="Loading customers…" />}

      {ready && invoices.data != null && (
        <div>
          {invoices.data.truncated && (
            <div
              data-testid="customers-truncated-notice"
              style={{ padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', color: 'var(--status-amber-text)', fontSize: 12.5, marginBottom: 16 }}
            >
              Invoices and Total billed count the newest {invoices.data.fetched} of {invoices.data.total} invoices.
            </div>
          )}
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: GRID_COLUMNS, gap: 14, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
              <span className="label">Customer</span>
              <span className="label">TIN</span>
              <span className="label">Type</span>
              <span className="label" style={{ textAlign: 'right' }}>Invoices</span>
              <span className="label" style={{ textAlign: 'right' }}>Total billed</span>
              <span className="label">Tax status</span>
            </div>
            {customers.map((c) => (
              <div key={c.id} className="pf-list-row" style={{ display: 'grid', gridTemplateColumns: GRID_COLUMNS, gap: 14, padding: '13px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
                <span style={{ display: 'flex', alignItems: 'center', gap: 11, minWidth: 0 }}>
                  <span style={{ flex: 'none', width: 30, height: 30, borderRadius: '50%', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{c.initials}</span>
                  <span style={{ ...CELL, fontSize: 13.5, fontWeight: 500 }}>{c.name}</span>
                </span>
                <span className="mono" style={{ ...CELL, fontSize: 12, color: c.tinColor }}>{c.tin}</span>
                <span style={{ ...CELL, fontSize: 13, color: 'var(--fg-2)' }}>{c.type}</span>
                <span className="money mono" style={{ ...CELL, fontSize: 13, textAlign: 'right' }}>{c.count}</span>
                <span className="money" style={{ ...CELL, fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{c.total}</span>
                <span>
                  <span className="mono" style={{ display: 'inline-flex', fontSize: 10, fontWeight: 600, letterSpacing: '0.04em', color: c.st.text, background: c.st.bg, border: `1px solid ${c.st.border}`, borderRadius: 'var(--radius-sm)', padding: '3px 9px' }}>
                    {c.st.label}
                  </span>
                </span>
              </div>
            ))}
            {records.data != null && records.data.pagination.total > PAGE_SIZE && (
              <div style={{ padding: '12px 18px' }}>
                <Pager pagination={records.data.pagination} busy={false} onGo={setOffset} testId="customers-pager" />
              </div>
            )}
          </div>
        </div>
      )}

      {empty && (
        <div style={{ background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', padding: 56, display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
          <span style={{ width: 44, height: 44, borderRadius: 'var(--radius-md)', background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{docGlyph}</span>
          <div style={{ fontSize: 16, fontWeight: 700, marginBottom: 4 }}>No customers yet</div>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: '0 0 20px', maxWidth: 320 }}>Customers are added when you save an invoice with a buyer TIN.</p>
          <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
            <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
          </button>
        </div>
      )}
    </div>
  )
}
