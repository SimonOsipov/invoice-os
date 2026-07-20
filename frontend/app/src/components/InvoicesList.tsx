// Invoices list — live invoice feed (M4-09-04, task-185). Fetches the signed-in
// tenant's real invoices from the invoice service and renders them with per-row 7-state
// status badges, replacing the mock `active.invoices` feed + mock 5-label `statusStyle`
// for this surface only (Obsidian M4-09 System Design §4). Ported shell from
// Platform.dc.html ~L343-387; the mock generators (genInvoices/buildClients) are left
// intact — deletion is M4-10.
//
// The "Needs attention" toggle re-fetches server-side (`needs_attention=true` via
// `deps:[needsAttention]`) rather than re-deriving the predicate in the browser
// ([server-side-needs-attention]). Row click routes through the existing
// selectImported/importedInvoiceId/detailTarget->'imported' seam with the real invoice
// UUID ([reuse-imported-seam]); rename deferred.

import { useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { plusGlyph } from '../glyphs'
import { fmt, fmtDate } from '../lib/format'
import { invoiceStatusStyle, invoicesViewState, listInvoices, shouldFetchInvoices, type InvoiceRecord } from '../lib/invoices'
import type { PlatformCtx } from '../types'

export function InvoicesList({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [needsAttention, setNeedsAttention] = useState(false)
  // Same `base ? … : …` narrowing as ClientsView.tsx:38-41 — `immediate:
  // shouldFetchInvoices(base)` keeps the no-gateway build at zero network. `deps:
  // [needsAttention]` re-runs the effect (and re-fetches) whenever the toggle flips.
  const list = useAsync<InvoiceRecord[]>(
    () => (base ? listInvoices(ctx.authedFetch, base, { needsAttention }) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchInvoices(base), deps: [needsAttention] },
  )
  const state = invoicesViewState(base, list)

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      {/* No page-level "New invoice" here: the persistent header-bar CTA (Header.tsx) is
          the single create affordance for the populated list. The empty state below keeps
          its own button (standard zero-state pattern). The "Needs attention" toggle sits
          in the header row (not gated by async state) so it stays reachable even when the
          filtered result set is itself empty. */}
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 22 }}>
        <div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Invoices</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{ctx.user.tenantName ?? 'Your workspace'} · create, validate, and transmit.</p>
        </div>
        <button
          onClick={() => setNeedsAttention((v) => !v)}
          data-testid="needs-attention-toggle"
          className="pf-chip"
          style={{
            height: 30,
            padding: '0 12px',
            borderRadius: 6,
            fontFamily: 'var(--font-sans)',
            fontSize: 12.5,
            fontWeight: 500,
            border: `1px solid ${needsAttention ? 'var(--accent)' : 'var(--line-2)'}`,
            background: needsAttention ? 'var(--accent)' : 'var(--bg-2)',
            color: needsAttention ? '#fff' : 'var(--fg-2)',
          }}
        >
          Needs attention
        </button>
      </div>

      {state === 'loading' && <Loading label="Loading invoices…" />}

      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}

      {(state === 'idle' || state === 'empty') && (
        <div data-testid="invoices-empty">
          <EmptyState title="No invoices yet" message="Create or import an invoice to start tracking compliance." />
          <div style={{ display: 'flex', justifyContent: 'center', marginTop: 16 }}>
            <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
              <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
            </button>
          </div>
        </div>
      )}

      {state === 'ready' && (
        <div data-testid="invoices-list" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
          <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: '150px 1fr 140px 120px 130px', gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
            <span className="label">Invoice #</span>
            <span className="label">Buyer</span>
            <span className="label" style={{ textAlign: 'right' }}>Amount</span>
            <span className="label">Date</span>
            <span className="label">Status</span>
          </div>
          {(list.data ?? []).map((r) => {
            const st = invoiceStatusStyle(r.status)
            return (
              <div
                key={r.id}
                onClick={() => ctx.openImportedInvoice(r.id)}
                data-testid="invoice-row"
                className="pf-row pf-list-row"
                style={{ display: 'grid', gridTemplateColumns: '150px 1fr 140px 120px 130px', gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
              >
                <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.invoice_number}</span>
                <span style={{ minWidth: 0 }}>
                  <span style={{ display: 'block', fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.buyer_name}</span>
                  <span className="mono" style={{ fontSize: 11, color: r.buyer_tin ? 'var(--fg-3)' : 'var(--status-red-text)' }}>{r.buyer_tin ?? 'TIN MISSING'}</span>
                </span>
                <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{r.total != null ? fmt(Number(r.total)) : '—'}</span>
                <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{fmtDate(r.issue_date ?? r.created_at)}</span>
                <span>
                  <span
                    data-testid="invoice-status-badge"
                    style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '3px 9px' }}
                  >
                    <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text, letterSpacing: '0.04em' }}>{st.label}</span>
                  </span>
                </span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
