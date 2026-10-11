// Settings > Buyers: the workspace buyer directory (GET /v1/customers), one row per TIN.
import { useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { listCustomers, type Customer, type CustomerListResponse } from '../lib/buyerDirectory'
import type { PlatformCtx } from '../types'
import { BuyerFormModal } from './BuyerFormModal'
import { Pager } from './Pager'

const PAGE_SIZE = 50
const GRID_COLUMNS = 'minmax(160px, 1.6fr) 140px 70px minmax(120px, 1.2fr) 130px 110px'

const CELL: React.CSSProperties = { minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }

export function BuyersView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [offset, setOffset] = useState(0)
  const [editing, setEditing] = useState<Customer | null>(null)

  const list = useAsync<CustomerListResponse>(
    () => (base ? listCustomers(ctx.authedFetch, base, { limit: PAGE_SIZE, offset }) : Promise.reject(new Error('no gateway configured'))),
    { isEmpty: (r) => r.pagination.total === 0, immediate: base != null, deps: [offset] },
  )
  const state = base == null ? 'idle' : list.status

  return (
    <div>
      {state === 'loading' && <Loading label="Loading buyers…" />}
      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}
      {(state === 'empty' || state === 'idle') && <EmptyState title="No buyers yet" message="Buyers are added when you save an invoice with a buyer TIN." />}

      {state === 'ready' && list.data != null && (
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
          <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: GRID_COLUMNS, gap: 14, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
            <span className="label">Buyer</span>
            <span className="label">TIN</span>
            <span className="label">Type</span>
            <span className="label">Email</span>
            <span className="label">Phone</span>
            <span className="label">State</span>
          </div>
          {list.data.customers.map((c) => (
            <div
              key={c.id}
              onClick={() => setEditing(c)}
              className="pf-row pf-list-row"
              style={{ display: 'grid', gridTemplateColumns: GRID_COLUMNS, gap: 14, padding: '13px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
            >
              <span style={{ ...CELL, fontSize: 13.5, fontWeight: 500 }}>{c.name ?? '—'}</span>
              <span className="mono" style={{ ...CELL, fontSize: 12, color: 'var(--fg-3)' }}>{c.tin}</span>
              <span style={{ ...CELL, fontSize: 13, color: 'var(--fg-2)' }}>{c.buyer_type ?? '—'}</span>
              <span style={{ ...CELL, fontSize: 13, color: 'var(--fg-2)' }}>{c.email ?? '—'}</span>
              <span style={{ ...CELL, fontSize: 13, color: 'var(--fg-2)' }}>{c.telephone ?? '—'}</span>
              <span style={{ ...CELL, fontSize: 13, color: 'var(--fg-2)' }}>{c.state ?? '—'}</span>
            </div>
          ))}
          {list.data.pagination.total > PAGE_SIZE && (
            <div style={{ padding: '12px 18px' }}>
              <Pager pagination={list.data.pagination} busy={false} onGo={setOffset} testId="buyers-pager" />
            </div>
          )}
        </div>
      )}

      {editing && base != null && (
        <BuyerFormModal
          customer={editing}
          ctx={ctx}
          base={base}
          onClose={() => setEditing(null)}
          onSuccess={() => {
            list.run()
            setEditing(null)
          }}
        />
      )}
    </div>
  )
}
