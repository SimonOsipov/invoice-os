// Reports & analytics — tax KPIs, top-customers bar list, validation summary, and export
// buttons; or an honest empty state. Ported from Platform.dc.html ~L780-846 + the
// reports slice of renderVals() (~L1469-1477). persona-handoff-fix step 3 swapped every
// number off the fabricated `active.invoices` overlay onto two independent live fetches
// ([fetch-per-surface], same posture as ClientsView/DashboardActive/InvoicesList/
// CustomersView): `list` (listInvoices, same entity-scoped rows CustomersView.tsx uses —
// entity-scoped server-side since [entity-id-restored]; gateByActiveEntity's own doc
// comment covers why its row-level check stays too) feeds the monetary KPIs + top-customers list,
// and `roll` (getRollup) feeds the Validation summary card via scopedBucket/topFailures —
// the SAME helpers DashboardActive.tsx already uses, not a second implementation of
// either.
//
// Step 3 DELETED the WHT KPI here instead of SAMPLE-marking it, on the reasoning that a
// fabricated withholding-tax figure on a tax-reporting screen was worse than a marked
// panel elsewhere. The user was asked directly and overruled that call: restore it,
// SAMPLE-marked, not silently dropped. That's what the 5th `reportKpis` entry below is —
// its underlying mock signal (a `wht: boolean` flag + an item-description regex) still
// doesn't exist on a real InvoiceRecord (no wht column on invoices at all,
// migrations/20260714103137_invoices.sql; line_items aren't in the list wire shape
// either, [D7]/[D8]), so it is NOT recomputed from that. It applies the same
// illustrative 5% rate to the real `taxable` sum that already feeds the first KPI tile —
// a real base, an invented rate, not a wholly invented number — and it carries the same
// "SAMPLE" mono chip DashboardActive.tsx's TileHead renders for its own fabricated
// panels (readiness score/trend, recent activity), so nobody reads it as a computed
// filing figure. "Total invoiced" (added by step 3 into WHT's old slot) is NOT evicted
// to make room — the KPI row grows to five tiles instead.

import { useMemo } from 'react'

import { ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { EXPORTS_LIST } from '../data'
import { fmt, fmtShort } from '../lib/format'
import { aggregateCustomers } from '../lib/customers'
import { dashboardViewState, getRollup, scopedBucket, topFailures, type Rollup } from '../lib/dashboard'
import { allInvoicesIsEmpty, fetchAllInvoices, gateByActiveEntity, invoicesViewState, shouldFetchInvoices, type AllInvoices } from '../lib/invoices'
import { crossGlyph, docGlyph, downloadGlyph, plusGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

// Tags the envelope with the entity it was fetched for, so `fresh` can gate stale renders.
type FetchedAllInvoices = AllInvoices & { fetchedEntityId: string | undefined }

export function ReportsView({ ctx }: { ctx: PlatformCtx }) {
  const { active } = ctx
  const base = gatewayBase()
  // Same in-house/null-entity resolution as InvoicesList.tsx's own `activeEntityId`.
  const activeEntityId = ctx.mode === 'inhouse' ? undefined : (ctx.active.entityId ?? undefined)

  // Drives the KPI grid + top-customers list + the page's own empty state — same
  // `base ? … : …` narrowing as InvoicesList.tsx:65-68/CustomersView.tsx. `deps`
  // re-fetches on a company switch ([entity-id-restored]: entity_id is a server-side
  // param now, so switching companies needs a real refetch, not just a recompute).
  const list = useAsync<FetchedAllInvoices>(
    () =>
      base
        ? // fetchAllInvoices pages through the whole set (AGGREGATE_PAGE_SIZE/_MAX_PAGES),
          // not just the server's first page (this screen's own un-paged call was the bug).
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
  // window entirely and still row-filters otherwise (its own doc comment covers why:
  // avoids flashing the previous client's KPIs for a frame), same as
  // CustomersView.tsx/InvoicesList.tsx's own `rows`.
  const rows = useMemo(
    () => gateByActiveEntity(list.data?.invoices ?? [], ctx.mode === 'inhouse', ctx.active.entityId),
    [list.data, ctx.mode, ctx.active.entityId],
  )

  // Drives ONLY the Validation summary card below — a separate async ladder from
  // `list` above (two genuinely different endpoints), so a slow/errored rollup fetch
  // never blocks the monetary KPIs this page's empty state is otherwise gated on. No
  // isEmpty predicate, same reason as DashboardActive.tsx:48-56 — a zero-count rollup
  // is 'ready', not empty.
  const roll = useAsync<Rollup>(
    () => (base ? getRollup(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  const rollState = dashboardViewState(base, roll)

  const custList = aggregateCustomers(rows)
  const taxable = rows.reduce((s, r) => s + (r.subtotal != null ? Number(r.subtotal) : 0), 0)
  const outVat = rows.reduce((s, r) => s + (r.vat != null ? Number(r.vat) : 0), 0)
  const totalInvoiced = rows.reduce((s, r) => s + (r.total != null ? Number(r.total) : 0), 0)
  // [wht-sample-restore] (persona-handoff-fix step 6, user-directed reversal of step 3's
  // deletion, see file header): illustrative 5% rate applied to the real `taxable` base
  // above — not a recomputation of the old mock's wht-flag/description-regex logic,
  // which has no real-schema equivalent at all.
  const whtSample = taxable * 0.05
  const reportKpis: { label: string; value: string; color: string; meta?: string }[] = [
    { label: 'Taxable value', value: fmtShort(taxable), color: 'var(--fg-1)' },
    { label: 'Output VAT', value: fmtShort(outVat), color: 'var(--action)' },
    { label: 'Total invoiced', value: fmtShort(totalInvoiced), color: 'var(--fg-1)' },
    // Muted fg-3 (not fg-1 like the four real tiles) + a leading `~`: on a filing-adjacent
    // screen the "SAMPLE" chip alone is easy to miss at a glance — the value itself has to
    // read as subordinate/illustrative too, not just carry an 11px badge beside it.
    { label: 'WHT withheld · 5%', value: '~' + fmtShort(whtSample), color: 'var(--fg-3)', meta: 'SAMPLE' },
    { label: 'Invoices in period', value: String(rows.length), color: 'var(--fg-1)' },
  ]
  const tcMax = Math.max(1, ...custList.map((o) => o.totalNum))
  const topCustomers = custList.slice(0, 5).map((o) => ({ name: o.name, total: fmt(o.totalNum), bar: Math.round((o.totalNum / tcMax) * 100) + '%' }))

  // scopedBucket/topFailures ([dashboard-scope-per-client]) — the SAME helpers
  // DashboardActive.tsx renders its needs-attention KPI and top-failures panel from, not
  // a parallel reimplementation. `bucket` is null only while `roll` hasn't resolved yet
  // (loading/error/no-gateway); the card below never renders numbers derived from it in
  // that window (see the rollState ladder further down).
  const bucket = roll.data ? scopedBucket(ctx.mode === 'inhouse', ctx.active.entityId, roll.data) : null
  const bucketTotal = bucket ? Object.values(bucket.counts).reduce((a, b) => a + b, 0) : 0
  const repPassed = bucket ? bucketTotal - bucket.needs_attention : 0
  const repFail = bucket ? bucket.needs_attention : 0
  const repPassPct = bucketTotal ? Math.round((repPassed / bucketTotal) * 100) : 0
  // top_violations has no per-entity breakdown on the wire (dashboard.go's Rollup) —
  // stays tenant-wide regardless of the selected client, same as DashboardActive's own
  // "Top validation failures" panel; the FIRM-WIDE chip below discloses that honestly.
  const reportFailures = roll.data ? topFailures(roll.data.top_violations) : []

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          TAX REPORTING
        </div>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Reports &amp; analytics</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{active.name} · tax summary, period to date · June 2026</p>
      </div>

      {state === 'loading' && <Loading label="Loading reports…" />}

      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}

      {/* Settling frame after a company switch (see `fresh`'s own comment) — never
          render the previous client's rows or its truncation numbers. */}
      {state === 'ready' && list.data != null && !fresh && <Loading label="Loading reports…" />}

      {/* Covers the no-gateway build ('idle'), a genuinely entity-less-of-invoices tenant
          ('empty', resolved server-side via `entity_id` — [entity-id-restored]), and the
          ready-but-gated-to-zero window (entityId not yet resolved, gateByActiveEntity) —
          same three-way union CustomersView.tsx/InvoicesList.tsx use for their own empty
          rung, plus the `fresh` gate so this never fires on a stale, not-yet-settled
          envelope. */}
      {(state === 'idle' || state === 'empty' || (state === 'ready' && list.data != null && fresh && rows.length === 0)) && (
        <div style={{ background: 'var(--bg-2)', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', padding: 56, display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
          <span style={{ width: 44, height: 44, borderRadius: 'var(--radius-md)', background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{docGlyph}</span>
          <div style={{ fontSize: 16, fontWeight: 600, marginBottom: 4 }}>No data to report yet</div>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: '0 0 20px', maxWidth: 340 }}>Reports populate once {active.short} has validated invoices in the period.</p>
          <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
            <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
          </button>
        </div>
      )}

      {state === 'ready' && list.data != null && fresh && rows.length > 0 && (
        <>
          {list.data.truncated && (
            <div
              data-testid="reports-truncated-notice"
              style={{ padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', color: 'var(--status-amber-text)', fontSize: 12.5, marginBottom: 16 }}
            >
              Showing {list.data.fetched} of {list.data.total} invoices — refine your search to see the rest.
            </div>
          )}
          {/* Five tiles now, not four — the restored WHT tile does not evict "Total
              invoiced" from the slot step 3 put it in. pf-grid-5 mirrors pf-grid-4's own
              collapse rules (platform.css), just one column wider at desktop width. */}
          <div className="pf-grid-5" style={{ display: 'grid', gridTemplateColumns: 'repeat(5, 1fr)', gap: 16, marginBottom: 20 }}>
            {reportKpis.map((k) => (
              <div key={k.label} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '18px 20px' }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
                  <div className="label">{k.label}</div>
                  {/* Same SAMPLE-chip styling TileHead's `meta` renders (DashboardActive.tsx)
                      — this tile row predates that shared-head refactor and has no header
                      strip of its own, so the chip sits inline beside the label instead. */}
                  {k.meta && (
                    <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                      {k.meta}
                    </span>
                  )}
                </div>
                <span className="money" style={{ fontSize: 25, fontWeight: 700, color: k.color }}>
                  {k.value}
                </span>
              </div>
            ))}
          </div>
          <div className="pf-grid-2" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1.3fr) minmax(0, 1fr)', gap: 20, marginBottom: 20 }}>
            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div style={{ padding: '15px 20px', borderBottom: '1px solid var(--line-1)' }}>
                <span className="card-title">Top customers by value</span>
              </div>
              <div style={{ padding: '6px 20px 12px' }}>
                {topCustomers.map((t) => (
                  <div key={t.name} style={{ padding: '11px 0', borderBottom: '1px solid var(--line-1)' }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 16, marginBottom: 7 }}>
                      <span style={{ fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.name}</span>
                      <span className="money" style={{ fontSize: 13, fontWeight: 600, flex: 'none' }}>{t.total}</span>
                    </div>
                    <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden' }}>
                      <div style={{ width: t.bar, height: '100%', background: 'var(--action)', borderRadius: 'var(--radius-sm)' }} />
                    </div>
                  </div>
                ))}
              </div>
            </div>
            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div style={{ padding: '15px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <span className="card-title">Validation summary</span>
                {/* % PASS is derived from `bucket` (scopedBucket), which is null until
                    `roll` resolves — held back a beat rather than flashing "0% PASS". */}
                {rollState === 'ready' && (
                  <span className="mono" style={{ fontSize: 11, color: 'var(--status-green-text)' }}>
                    {repPassPct}% PASS
                  </span>
                )}
              </div>
              <div style={{ padding: '18px 20px' }}>
                {rollState === 'loading' && <Loading label="Loading validation summary…" />}
                {rollState === 'error' && roll.error && <ErrorState error={roll.error} onRetry={roll.run} />}
                {rollState === 'ready' && (
                  <>
                    <div style={{ display: 'flex', gap: 10, marginBottom: 16 }}>
                      <div style={{ flex: 1, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 'var(--radius-input)', padding: '12px 14px' }}>
                        <div className="money" style={{ fontSize: 22, fontWeight: 700, color: 'var(--status-green-text)' }}>{repPassed}</div>
                        <div className="label" style={{ marginTop: 2 }}>
                          Passed
                        </div>
                      </div>
                      <div style={{ flex: 1, background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', borderRadius: 'var(--radius-input)', padding: '12px 14px' }}>
                        <div className="money" style={{ fontSize: 22, fontWeight: 700, color: 'var(--status-red-text)' }}>{repFail}</div>
                        <div className="label" style={{ marginTop: 2 }}>
                          Failing
                        </div>
                      </div>
                    </div>
                    {reportFailures.length > 0 && (
                      <div>
                        {/* FIRM-WIDE: top_violations carries no per-entity breakdown on the
                            wire (see the file-header comment) — disclosed the same way
                            DashboardActive.tsx's own top-failures panel does, so this list
                            is never mistaken for THIS client's own failures alone. */}
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10 }}>
                          <span className="label">Top failures</span>
                          <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)' }}>FIRM-WIDE</span>
                        </div>
                        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                          {reportFailures.map((f) => (
                            <div key={f.ruleKey} style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
                              <span style={{ color: 'var(--status-red-text)', flex: 'none' }}>{crossGlyph}</span>
                              <span style={{ flex: 1, fontSize: 12.5, color: 'var(--fg-2)' }}>{f.label}</span>
                              <span className="money mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--status-red-text)' }}>{f.count}</span>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}
                  </>
                )}
              </div>
            </div>
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '18px 20px' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14 }}>
              <span className="card-title">Export &amp; filings</span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                NRS-READY FORMATS
              </span>
            </div>
            <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
              {EXPORTS_LIST.map((e) => (
                <button key={e.name} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 38 }}>
                  <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span> {e.name}{' '}
                  <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 5px', marginLeft: 2 }}>
                    {e.fmt}
                  </span>
                </button>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  )
}
