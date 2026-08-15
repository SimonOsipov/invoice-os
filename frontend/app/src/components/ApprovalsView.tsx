// Approvals screen (APPR-12-03) -- read-only queue over GET /invoices?awaiting_approval=true.
// InvoicesList's structural sibling: own useAsync, own entity gate, envelope kept whole,
// shared Pager fed the response's echoed pagination. No selection (that's [APPR-12-04])
// and no poll: this queue is always status='validated' (store.go:691), never
// queued|submitted, so shouldPollList would be provably always false here -- reusing it
// would be dead code. No whole-row click either (G6) -- detail navigation out of the
// queue is APPR-13's scope, deliberately omitted here.

import { useMemo, useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { APPROVALS_COPY, approvalRowView, listAwaitingApproval } from '../lib/approvals'
import { fmt, fmtDate } from '../lib/format'
import {
  gateByActiveEntity,
  invoiceListIsEmpty,
  invoicesViewState,
  REGISTER_PAGE_SIZE,
  shouldFetchInvoices,
  type InvoiceListResponse,
} from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { Pager } from './Pager'

// Owed topology assertion: A07-5/A07-6 (APPR-12-07, assertFillsColumn over WIDE_WIDTHS)
// -- kept as ONE shared const across head and rows (InvoicesList.tsx:69's shape), not
// ClientsView.tsx's two-literal shape.
const APPROVALS_GRID_COLUMNS = '140px 1fr 130px 90px 180px 110px'

// Tags the envelope with the entity it was fetched for -- same [dashboard-scope-per-client]
// staleness idiom as InvoicesList's FetchedInvoiceList.
type FetchedApprovalsList = InvoiceListResponse & { fetchedEntityId: string | undefined }

export function ApprovalsView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [offset, setOffset] = useState(0)
  // In-house has no business_entities row ([entity-picker] trap 1) -- entity_id omitted
  // entirely, same convention as InvoicesList.tsx:83.
  const activeEntityId = ctx.mode === 'inhouse' ? undefined : (ctx.active.entityId ?? undefined)

  const list = useAsync<FetchedApprovalsList>(
    () =>
      base
        ? listAwaitingApproval(ctx.authedFetch, base, { entityId: activeEntityId, limit: REGISTER_PAGE_SIZE, offset }).then((r) => ({
            ...r,
            fetchedEntityId: activeEntityId,
          }))
        : Promise.reject(new Error('no gateway configured')),
    {
      isEmpty: invoiceListIsEmpty,
      immediate: shouldFetchInvoices(base),
      deps: [ctx.mode, ctx.active.entityId, offset],
    },
  )
  const state = invoicesViewState(base, list)
  const loading = state === 'loading'
  // !fresh: the envelope predates the active entity (a company switch's pre-refetch
  // frame) -- bounded and self-healing, same reasoning as InvoicesList.tsx:388-394.
  const fresh = list.data != null && list.data.fetchedEntityId === activeEntityId

  // G1: gateByActiveEntity is the render-time invariant on top of the server-side
  // entity_id filter -- useAsync's deps effect is a PASSIVE effect, so a company switch
  // commits and paints one frame holding the PREVIOUS entity's rows before the refetch's
  // 'start' dispatch (gateByActiveEntity's own doc comment, lib/invoices.ts). Unfiltered
  // rows here carry can_approve:true and become checkbox-selectable in [APPR-12-04], so
  // this is a leak-prevention gate, not cosmetic (A03-8).
  const rows = useMemo(
    () => gateByActiveEntity(list.data?.invoices ?? [], ctx.mode === 'inhouse', ctx.active.entityId),
    [list.data, ctx.mode, ctx.active.entityId],
  )

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          {APPROVALS_COPY.eyebrow}
        </div>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>{APPROVALS_COPY.h1}</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
          {ctx.user.tenantName ?? 'Your workspace'} · {APPROVALS_COPY.subtitle}
        </p>
      </div>

      {state === 'loading' && <Loading label={APPROVALS_COPY.loading} />}

      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}

      {(state === 'idle' || state === 'empty') && (
        <div data-testid="approvals-empty">
          <EmptyState title={APPROVALS_COPY.emptyTitle} message={APPROVALS_COPY.emptyMessage} />
        </div>
      )}

      {state === 'ready' && list.data != null && !fresh && <Loading label={APPROVALS_COPY.loading} />}

      {state === 'ready' && list.data != null && fresh && rows.length === 0 && (
        <div data-testid="approvals-empty-page">
          <EmptyState title={APPROVALS_COPY.emptyPageTitle} message={APPROVALS_COPY.emptyPageMessage} />
          <div style={{ marginTop: 16 }}>
            <Pager pagination={list.data.pagination} busy={loading} onGo={setOffset} testId="approvals-pager" />
          </div>
        </div>
      )}

      {state === 'ready' && list.data != null && fresh && rows.length > 0 && (
        <>
          <div data-testid="approvals-list" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: APPROVALS_GRID_COLUMNS, gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', alignItems: 'center' }}>
              <span className="label">{APPROVALS_COPY.colInvoice}</span>
              <span className="label">{APPROVALS_COPY.colBuyer}</span>
              <span className="label" style={{ textAlign: 'right' }}>{APPROVALS_COPY.colAmount}</span>
              <span className="label">{APPROVALS_COPY.colStep}</span>
              <span className="label">{APPROVALS_COPY.colRole}</span>
              <span className="label">{APPROVALS_COPY.colDue}</span>
            </div>
            {rows.map((r) => {
              const av = approvalRowView(r)
              return (
                <div
                  key={r.id}
                  data-testid="approval-row"
                  className="pf-row pf-list-row"
                  style={{ display: 'grid', gridTemplateColumns: APPROVALS_GRID_COLUMNS, gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
                >
                  <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.invoice_number}</span>
                  <span style={{ fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.buyer_name}</span>
                  <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{r.total != null ? fmt(Number(r.total)) : '—'}</span>
                  <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{av.stepLabel}</span>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                    <span style={{ fontSize: 13, color: 'var(--fg-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{av.roleLabel}</span>
                    {av.pendingHolderWarn && (
                      <span
                        data-testid="approval-unstaffed-warning"
                        title={APPROVALS_COPY.unstaffedSeat}
                        className="mono"
                        style={{ fontSize: 10, fontWeight: 600, color: 'var(--status-amber-text)', letterSpacing: '0.04em' }}
                      >
                        {APPROVALS_COPY.unstaffedSeat}
                      </span>
                    )}
                  </span>
                  <span className="mono" style={{ fontSize: 12, color: av.overdue ? 'var(--status-red-text)' : 'var(--fg-3)' }}>
                    {av.overdue ? <span data-testid="approval-overdue">{APPROVALS_COPY.overdue}</span> : av.dueAt != null ? fmtDate(av.dueAt) : '—'}
                  </span>
                </div>
              )
            })}
          </div>

          <div style={{ marginTop: 16 }}>
            <Pager pagination={list.data.pagination} busy={loading} onGo={setOffset} testId="approvals-pager" />
          </div>
        </>
      )}
    </div>
  )
}
