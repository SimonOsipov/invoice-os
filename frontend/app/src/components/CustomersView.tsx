// Customers & vendors — buyer master data aggregated from the active client's whole,
// entity-scoped invoice set: a table with TIN-validity status, or an honest empty
// state (BUG-01-08 dropped the client-derived KPI cards -- see [e-and-f-ship-in-one-subtask]).
// Ported from Platform.dc.html ~L733-779 + the customers slice of
// renderVals() (~L1462-1468). persona-handoff-fix step 3 swapped the source off the
// fabricated `active.invoices` overlay (attributing invented buyers to the real
// selected company) onto the SAME live fetch InvoicesList.tsx already established; a
// later regression fix ([entity-id-restored]) moved the actual entity-row filtering
// server-side, via listInvoices' own `entity_id` param — see gateByActiveEntity's own
// doc comment (lib/invoices.ts) for what's left client-side. Own independent fetch
// ([fetch-per-surface], same posture as ClientsView/DashboardActive/InvoicesList), not
// a shared cache with InvoicesList.

import { useMemo } from 'react'

import { ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { fmt } from '../lib/format'
import { aggregateCustomers, initials } from '../lib/customers'
import { allInvoicesIsEmpty, fetchAllInvoices, gateByActiveEntity, invoicesViewState, shouldFetchInvoices, type AllInvoices } from '../lib/invoices'
import { docGlyph, plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

// Tags the envelope with the entity it was fetched for, mirroring InvoicesList.tsx's
// FetchedInvoiceList -- lets `fresh` gate the render so a company switch's pre-refetch
// frame can't show the previous client's total/truncated numbers.
type FetchedAllInvoices = AllInvoices & { fetchedEntityId: string | undefined }

export function CustomersView({ ctx }: { ctx: PlatformCtx }) {
  const { active } = ctx
  const base = gatewayBase()
  // Same in-house/null-entity resolution as InvoicesList.tsx's own `activeEntityId`.
  const activeEntityId = ctx.mode === 'inhouse' ? undefined : (ctx.active.entityId ?? undefined)
  // Same `base ? … : …` narrowing as InvoicesList.tsx:65-68 — `immediate:
  // shouldFetchInvoices(base)` keeps the no-gateway build at zero network. `deps`
  // re-fetches on a company switch ([entity-id-restored]: entity_id is a server-side
  // param now, so switching companies needs a real refetch, not just a recompute).
  const list = useAsync<FetchedAllInvoices>(
    () =>
      base
        ? // fetchAllInvoices pages through the whole set (AGGREGATE_PAGE_SIZE/_MAX_PAGES),
          // not just the server's first page (INVCR-01-08 un-paged call was the bug).
          fetchAllInvoices(ctx.authedFetch, base, { entityId: activeEntityId }).then((r) => ({ ...r, fetchedEntityId: activeEntityId }))
        : Promise.reject(new Error('no gateway configured')),
    { isEmpty: allInvoicesIsEmpty, immediate: shouldFetchInvoices(base), deps: [ctx.mode, ctx.active.entityId] },
  )
  const state = invoicesViewState(base, list)
  // See InvoicesList.tsx's own `fresh` for the trap this closes: on a company switch,
  // list.data still holds the PREVIOUS entity's envelope for one committed render.
  const fresh = list.data != null && list.data.fetchedEntityId === activeEntityId

  // [dashboard-scope-per-client]: the fetch itself is already entity-scoped
  // ([entity-id-restored]); gateByActiveEntity blanks the "not yet resolved" transient
  // window entirely, and still row-filters otherwise (its own doc comment covers why:
  // a company switch's pre-refetch frame would otherwise flash the previous client's
  // buyers) — a buyer's totals here can never disagree with the invoice rows the
  // Invoices page renders for this client.
  const rows = useMemo(
    () => gateByActiveEntity(list.data?.invoices ?? [], ctx.mode === 'inhouse', ctx.active.entityId),
    [list.data, ctx.mode, ctx.active.entityId],
  )
  const custList = aggregateCustomers(rows)
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

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          TRADING PARTNERS
        </div>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Customers &amp; vendors</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{active.name} · buyer master data, tax IDs &amp; billing history</p>
      </div>

      {state === 'loading' && <Loading label="Loading customers…" />}

      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}

      {/* Settling frame after a company switch (see `fresh`'s own comment) — never
          render the previous client's rows or its truncation numbers. */}
      {state === 'ready' && list.data != null && !fresh && <Loading label="Loading customers…" />}

      {state === 'ready' && list.data != null && fresh && customers.length > 0 && (
        <div>
          {list.data.truncated && (
            <div
              data-testid="customers-truncated-notice"
              style={{ padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', color: 'var(--status-amber-text)', fontSize: 12.5, marginBottom: 16 }}
            >
              Showing {list.data.fetched} of {list.data.total} invoices — refine your search to see the rest.
            </div>
          )}
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
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
                  <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-input)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{c.initials}</span>
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

      {/* Covers the no-gateway build ('idle'), a genuinely entity-less-of-invoices tenant
          ('empty', now resolved server-side via `entity_id` — [entity-id-restored]), and
          the ready-but-gated-to-zero window (entityId not yet resolved,
          gateByActiveEntity) — same three-way union InvoicesList.tsx's own empty rung
          uses, plus the `fresh` gate so this never fires on a stale, not-yet-settled
          envelope. */}
      {(state === 'idle' || state === 'empty' || (state === 'ready' && list.data != null && fresh && customers.length === 0)) && (
        <div style={{ background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', padding: 56, display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
          <span style={{ width: 44, height: 44, borderRadius: 'var(--radius-md)', background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{docGlyph}</span>
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
