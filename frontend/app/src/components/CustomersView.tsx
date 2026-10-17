// Customers: the workspace's customer records (GET /v1/customers, paged), with Invoices and
// Total billed counted from the whole invoice set and joined by trimmed TIN.

import { useEffect, useMemo, useState } from 'react'

import { ApiError, EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { archiveCustomer, FILEABLE_TIN, listCustomers, restoreCustomer, type Customer, type CustomerListResponse } from '../lib/buyerDirectory'
import { fmt } from '../lib/format'
import { aggregateCustomers, customerName, initials } from '../lib/customers'
import { fetchAllInvoices, type AllInvoices } from '../lib/invoices'
import { archiveActionFor, entityStatusParam, entityStatusStyle, type ArchiveAction, type EntityFilterPos } from '../lib/portfolio'
import { docGlyph, plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'
import { CustomerFormModal } from './CustomerFormModal'
import { Pager } from './Pager'

const PAGE_SIZE = 50
const GRID_COLUMNS = 'minmax(160px, 1.6fr) 120px 70px 70px 140px 104px 140px'
const ARCHIVE_NOTICE = 'Leaves the Active list and the invoice prefill. Invoices that name it still save.'
const CELL: React.CSSProperties = { minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }

const VALID_CHIP = { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'VALID' }
const NEEDS_TIN_CHIP = { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'NEEDS TIN' }
const ARCHIVED_CHIP = entityStatusStyle('archived')

const FILTER_POSITIONS: Array<{ pos: EntityFilterPos; label: string }> = [
  { pos: 'all', label: 'All' },
  { pos: 'active', label: 'Active' },
  { pos: 'archived', label: 'Archived' },
]

function chipStyle(c: { bg: string; border: string; text: string }): React.CSSProperties {
  return { display: 'inline-flex', fontSize: 10, fontWeight: 600, letterSpacing: '0.04em', color: c.text, background: c.bg, border: `1px solid ${c.border}`, borderRadius: 'var(--radius-sm)', padding: '3px 9px' }
}

export function CustomersView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [offset, setOffset] = useState(0)
  const [pos, setPos] = useState<EntityFilterPos>('active')
  const [modal, setModal] = useState<{ mode: 'create' } | { mode: 'edit'; customer: Customer } | null>(null)

  const records = useAsync<CustomerListResponse>(
    () => (base ? listCustomers(ctx.authedFetch, base, { limit: PAGE_SIZE, offset, status: entityStatusParam(pos) }) : Promise.reject(new Error('no gateway configured'))),
    { isEmpty: (r) => r.pagination.total === 0, immediate: base != null, deps: [offset, pos] },
  )
  // Probe for archived records behind an empty Active page, so the empty card does not lie.
  const probe = useAsync<CustomerListResponse>(
    () => (base ? listCustomers(ctx.authedFetch, base, { limit: 1, offset: 0, status: 'archived' }) : Promise.reject(new Error('no gateway configured'))),
    { immediate: false },
  )
  const probing = base != null && pos === 'active' && records.status === 'empty'
  useEffect(() => {
    if (probing) probe.run()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [probing])
  // Workspace-wide (no entity_id) and no isEmpty: records with zero invoices still render.
  const invoices = useAsync<AllInvoices>(
    () => (base ? fetchAllInvoices(ctx.authedFetch, base, {}) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )

  const probePending = probing && (probe.status === 'idle' || probe.status === 'loading')
  const loading = records.status === 'loading' || invoices.status === 'loading' || probePending
  const errored = records.status === 'error' || invoices.status === 'error'
  const error = records.error ?? invoices.error
  const retry = () => {
    if (records.status === 'error') records.run()
    if (invoices.status === 'error') invoices.run()
  }
  const empty = !errored && !loading && (base == null || records.status === 'empty')
  const filteredEmpty = empty && base != null && pos === 'archived'
  const noActive = empty && probing && (probe.data?.pagination?.total ?? 0) > 0
  const changePos = (p: EntityFilterPos) => {
    setPos(p)
    setOffset(0)
  }
  const [armedId, setArmedId] = useState<string | null>(null)
  const [archivingId, setArchivingId] = useState<string | null>(null)
  const [archiveError, setArchiveError] = useState<{ id: string; message: string } | null>(null)

  // A later page emptied by archive/restore (or an outside change) steps to the last page.
  const recordsTotal = records.data?.pagination.total ?? 0
  const recordsEmptyPage = records.data != null && records.data.customers.length === 0
  useEffect(() => {
    if (recordsEmptyPage && recordsTotal > 0 && offset > 0) setOffset(Math.floor((recordsTotal - 1) / PAGE_SIZE) * PAGE_SIZE)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [records.data])

  async function handleArchiveClick(id: string, action: ArchiveAction) {
    if (base == null || archivingId === id) return
    if (!action.confirming) {
      setArmedId(id)
      setArchiveError(null)
      return
    }
    setArchivingId(id)
    try {
      if (action.kind === 'offboard') await archiveCustomer(ctx.authedFetch, base, id)
      else await restoreCustomer(ctx.authedFetch, base, id)
      setArmedId(null)
      setArchiveError(null)
      records.run()
    } catch (err) {
      setArmedId(null)
      setArchiveError({ id, message: err instanceof ApiError ? err.message : 'Something went wrong. Please try again.' })
    } finally {
      setArchivingId(null)
    }
  }
  const ready = !errored && !loading && records.data != null && invoices.data != null

  const byTin = useMemo(() => new Map(aggregateCustomers(invoices.data?.invoices ?? []).map((o) => [o.tin, o])), [invoices.data])
  const customers = (records.data?.customers ?? []).map((r) => {
    const agg = byTin.get(r.tin)
    const name = customerName(r.name, agg?.name)
    const fileable = FILEABLE_TIN.test(r.tin)
    return {
      record: r,
      id: r.id,
      name,
      initials: initials(name) || '—',
      tin: r.tin,
      tinColor: fileable ? 'var(--fg-3)' : 'var(--status-red-text)',
      type: r.buyer_type ?? '—',
      count: String(agg?.count ?? 0),
      total: fmt(agg?.totalNum ?? 0),
      st: fileable ? VALID_CHIP : NEEDS_TIN_CHIP,
      archived: r.status === 'archived',
      action: archiveActionFor(r, null, armedId === r.id),
    }
  })

  // Rejects on a failed lookup or no record, so the create form shows its banner.
  async function openExisting(tin: string) {
    if (base == null) throw new Error('no gateway configured')
    const res = await listCustomers(ctx.authedFetch, base, { limit: 1, offset: 0, tin })
    const held = res.customers[0]
    if (!held) throw new Error('no customer holds this TIN')
    if (held.status === 'archived') changePos('archived')
    setModal({ mode: 'edit', customer: held })
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 22 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            TRADING PARTNERS
          </div>
          <h1 style={{ fontSize: 26, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Customers</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
            {ctx.user.tenantName ? `${ctx.user.tenantName} · ` : ''}Customer records, tax IDs &amp; billing history
          </p>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          {FILTER_POSITIONS.map(({ pos: p, label }) => (
            <button
              key={p}
              onClick={() => changePos(p)}
              className="pf-chip"
              style={{
                height: 30,
                padding: '0 12px',
                fontFamily: 'var(--font-sans)',
                fontSize: 12.5,
                fontWeight: 500,
                border: `1px solid ${pos === p ? 'var(--action)' : 'var(--line-2)'}`,
                background: pos === p ? 'var(--action)' : 'var(--bg-2)',
                color: pos === p ? 'var(--primary-foreground)' : 'var(--fg-2)',
              }}
            >
              {label}
            </button>
          ))}
          <button onClick={() => setModal({ mode: 'create' })} disabled={base == null} className="v2-btn v2-btn-primary pf-btn">
            <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> Add customer
          </button>
        </div>
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
              <span className="label">Action</span>
            </div>
            {customers.map((c) => (
              <div key={c.id} onClick={() => setModal({ mode: 'edit', customer: c.record })} className="pf-row pf-list-row" style={{ display: 'grid', gridTemplateColumns: GRID_COLUMNS, gap: 14, padding: '13px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
                <span style={{ display: 'flex', alignItems: 'center', gap: 11, minWidth: 0 }}>
                  <span style={{ flex: 'none', width: 30, height: 30, borderRadius: '50%', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{c.initials}</span>
                  <span style={{ ...CELL, fontSize: 13.5, fontWeight: 500 }}>{c.name}</span>
                  {c.archived && (
                    <span className="mono" style={{ ...chipStyle(ARCHIVED_CHIP), flex: 'none' }}>
                      {ARCHIVED_CHIP.label}
                    </span>
                  )}
                </span>
                <span className="mono" style={{ ...CELL, fontSize: 12, color: c.tinColor }}>{c.tin}</span>
                <span style={{ ...CELL, fontSize: 13, color: 'var(--fg-2)' }}>{c.type}</span>
                <span className="money mono" style={{ ...CELL, fontSize: 13, textAlign: 'right' }}>{c.count}</span>
                <span className="money" style={{ ...CELL, fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{c.total}</span>
                <span>
                  <span className="mono" style={chipStyle(c.st)}>
                    {c.st.label}
                  </span>
                </span>
                <span style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 4, minWidth: 0 }}>
                  <button
                    type="button"
                    onClick={(ev) => {
                      ev.stopPropagation() // the row's own onClick opens the edit form
                      void handleArchiveClick(c.id, c.action)
                    }}
                    disabled={archivingId === c.id}
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={{ height: 30, padding: '0 12px', fontSize: 12.5, color: c.action.confirming ? 'var(--status-red-text)' : 'var(--fg-1)' }}
                  >
                    {c.action.label}
                  </button>
                  {c.action.confirming && c.action.kind === 'offboard' && <span style={{ fontSize: 11.5, lineHeight: 1.4, color: 'var(--fg-3)' }}>{ARCHIVE_NOTICE}</span>}
                  {archiveError?.id === c.id && <span style={{ fontSize: 11.5, lineHeight: 1.4, color: 'var(--status-red-text)' }}>{archiveError.message}</span>}
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

      {filteredEmpty && <EmptyState title="No customers match this filter" message="Try a different status filter." />}

      {empty && !filteredEmpty && (
        <div style={{ background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', padding: 56, display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
          <span style={{ width: 44, height: 44, borderRadius: 'var(--radius-md)', background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{docGlyph}</span>
          <div style={{ fontSize: 16, fontWeight: 700, marginBottom: 4 }}>{noActive ? 'No active customers' : 'No customers yet'}</div>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: '0 0 20px', maxWidth: 320 }}>{noActive ? 'Archived customers are under the Archived filter.' : 'Customers are added when you save an invoice with a buyer TIN.'}</p>
          <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
            <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
          </button>
        </div>
      )}

      {modal && base != null && (
        <CustomerFormModal
          key={modal.mode}
          mode={modal.mode}
          customer={modal.mode === 'edit' ? modal.customer : undefined}
          ctx={ctx}
          base={base}
          onClose={() => setModal(null)}
          onSuccess={() => {
            // A new record is active: leave Archived (the pos change refetches).
            if (modal.mode === 'create' && pos === 'archived') changePos('active')
            else records.run()
            setModal(null)
          }}
          onOpenExisting={openExisting}
        />
      )}
    </div>
  )
}
